package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/antimalware"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Antimalware policy, in one place.
//
// Every stored file that reaches a person (download, preview, ZIP export, media) must have a
// file_scans row whose status is "clean". The delivery paths all ask fileStatus / requireCleanFile;
// no other table says "this may be served". No row, a pending row, an error, an infected verdict or a
// skipped one all keep the file closed (fail-closed). A scanner that is down or slow therefore never
// opens anything: it only delays.
//
// Scans are queued as rows, so the same worker handles new uploads, files that existed before the
// policy, retries after an outage and re-checks after a signature update. Small uploads are also
// scanned inline so the common case is "clean" by the time the response arrives.

const (
	scanQuarantineDir   = "quarantine"
	scanMaxAttempts     = 5 // after this many failures the administrators are alerted (retries go on)
	scanLease           = 5 * time.Minute
	scanDefaultInterval = 10 * time.Second
	scanVersionTTL      = time.Minute
)

type scanSettings struct {
	maxBytes    int64
	inlineBytes int64
	timeout     time.Duration
	reuse       time.Duration
	rescanAfter time.Duration
	batch       int
}

func (a *App) scanCfg() scanSettings {
	c := a.Config.Antimalware
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	return scanSettings{
		maxBytes:    int64(pick(c.MaxMB, 25)) << 20,
		inlineBytes: int64(pick(c.InlineMaxKB, 2048)) << 10,
		timeout:     time.Duration(pick(c.TimeoutSeconds, 30)) * time.Second,
		reuse:       time.Duration(pick(c.ReuseHours, 24)) * time.Hour,
		rescanAfter: time.Duration(pick(c.RescanDays, 7)) * 24 * time.Hour,
		batch:       pick(c.BatchSize, 5),
	}
}

// --- state -------------------------------------------------------------------------------------

// fileStatus is the one question every delivery path asks. A file with no row is registered as
// pending on the spot (this is how a path nobody remembered to wire, or a legacy file, still ends up
// queued) and reported as pending. The organization must match: another tenant's row never counts.
func (a *App) fileStatus(orgID uuid.UUID, source string, id uuid.UUID, storagePath string) string {
	var s models.FileScan
	err := a.DB.Select("status").Where("organization_id = ? AND source = ? AND resource_id = ?", orgID, source, id).First(&s).Error
	if err == nil {
		return s.Status
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		now := time.Now()
		_ = a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.FileScan{
			ID: uuid.New(), OrganizationID: orgID, Source: source, ResourceID: id, StoragePath: storagePath,
			Status: models.ScanPending, NextAttemptAt: &now,
		}).Error
	}
	return models.ScanPending // unknown is closed
}

// scanStatuses is fileStatus for a list, without registering anything: missing = pending.
func (a *App) scanStatuses(orgID uuid.UUID, source string, ids []uuid.UUID) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var rows []models.FileScan
	a.DB.Select("resource_id, status").Where("organization_id = ? AND source = ? AND resource_id IN ?", orgID, source, ids).Find(&rows)
	for _, r := range rows {
		out[r.ResourceID] = r.Status
	}
	return out
}

func scanStatusOf(m map[uuid.UUID]string, id uuid.UUID) string {
	if s, ok := m[id]; ok {
		return s
	}
	return models.ScanPending
}

// requireCleanFile answers 423 Locked unless the file has a clean verdict. Call it AFTER the
// permission checks, so a person who may not see the file learns nothing about its scan state.
func (a *App) requireCleanFile(r *fastglue.Request, orgID uuid.UUID, source string, id uuid.UUID, storagePath string) bool {
	st := a.fileStatus(orgID, source, id, storagePath)
	if st == models.ScanClean {
		return true
	}
	msg := map[string]string{
		models.ScanPending:  "This file is still being scanned for malware",
		models.ScanInfected: "This file was blocked: malware was detected",
		models.ScanError:    "This file could not be scanned yet; it will be retried",
		models.ScanSkipped:  "This file was not scanned and cannot be opened",
	}[st]
	if msg == "" {
		msg = "This file has not been cleared"
	}
	_ = r.SendErrorEnvelope(fasthttp.StatusLocked, msg, map[string]any{"scan_status": st}, "")
	return false
}

// registerScanTx queues a file inside the transaction that stores it (or REPLACES a verdict when the
// same resource gets new content: the row goes back to pending before the new bytes are served).
func registerScanTx(tx *gorm.DB, orgID uuid.UUID, source string, id uuid.UUID, storagePath, sha string, size int64) error {
	now := time.Now()
	row := models.FileScan{
		ID: uuid.New(), OrganizationID: orgID, Source: source, ResourceID: id, StoragePath: storagePath,
		SHA256: sha, SizeBytes: size, Status: models.ScanPending, NextAttemptAt: &now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source"}, {Name: "resource_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"storage_path": storagePath, "sha256": sha, "size_bytes": size, "status": models.ScanPending,
			"signature": "", "scanner_version": "", "attempts": 0, "next_attempt_at": now, "last_error": "",
			"scanned_at": nil, "quarantined_at": nil, "alerted_at": nil, "updated_at": now,
		}),
	}).Create(&row).Error
}

// --- scanning ----------------------------------------------------------------------------------

// scanVersionCache remembers the engine's version line briefly, so a batch of scans asks once.
type scanVersionCache struct {
	mu sync.Mutex
	v  string
	at time.Time
}

// ForgetScannerVersion drops the remembered engine version (tests, or a known signature update).
func (a *App) ForgetScannerVersion() {
	a.scanVer.mu.Lock()
	a.scanVer.v = ""
	a.scanVer.mu.Unlock()
}

func (a *App) scannerVersion(ctx context.Context) string {
	if a.Scanner == nil {
		return ""
	}
	c := &a.scanVer
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.v != "" && time.Since(c.at) < scanVersionTTL {
		return c.v
	}
	v, err := a.Scanner.Version(ctx)
	if err != nil || v == "" {
		return c.v // last known (may be empty)
	}
	c.v, c.at = v, time.Now()
	return v
}

// ScanNow scans one file right away when it is small enough (called after an upload commits). Any
// failure just leaves the row for the worker; it never opens the file.
func (a *App) ScanNow(source string, id uuid.UUID) {
	if a.Scanner == nil {
		return
	}
	var row models.FileScan
	if a.DB.Where("source = ? AND resource_id = ? AND status IN ?", source, id, []string{models.ScanPending, models.ScanError}).First(&row).Error != nil {
		return
	}
	if row.SizeBytes > a.scanCfg().inlineBytes {
		return
	}
	a.runScan(context.Background(), row, false)
}

func (a *App) scanTarget(row models.FileScan) bool {
	var table, where string
	switch row.Source {
	case models.ScanSourceOccurrenceFile:
		table, where = "occurrence_files", "deleted_at IS NULL"
	case models.ScanSourceChatAttachment:
		table, where = "chat_attachments", "purged_at IS NULL"
	case models.ScanSourceOccurrenceDocument:
		table, where = "occurrence_documents", "deleted_at IS NULL AND COALESCE(attachment_path,'') <> ''"
	case models.ScanSourceMessageMedia:
		table, where = "messages", "deleted_at IS NULL AND COALESCE(media_url,'') <> ''"
	default:
		return false
	}
	var n int64
	a.DB.Table(table).Where("id = ? AND "+where, row.ResourceID).Count(&n)
	return n > 0
}

// runScan reaches a verdict for one row. rescan = the file already had a clean verdict: only an
// infection (or nothing) changes the row; a scanner failure leaves the old verdict alone.
func (a *App) runScan(ctx context.Context, row models.FileScan, rescan bool) {
	cfg := a.scanCfg()
	if !a.scanTarget(row) { // the file was deleted meanwhile
		a.DB.Where("id = ?", row.ID).Delete(&models.FileScan{})
		return
	}
	fail := func(status, why string) {
		if rescan {
			a.Log.Error("Antimalware re-scan failed", "file_scan", row.ID, "reason", why)
			return
		}
		a.recordScanFailure(row, status, why)
	}
	full, perr := a.resolveSafeStoragePath(row.StoragePath)
	if perr != nil {
		fail(models.ScanError, "file not found in storage")
		return
	}
	f, oerr := os.Open(full)
	if oerr != nil {
		fail(models.ScanError, "cannot open file")
		return
	}
	defer func() { _ = f.Close() }()
	st, serr := f.Stat()
	if serr != nil || !st.Mode().IsRegular() {
		fail(models.ScanError, "not a regular file")
		return
	}
	if st.Size() > cfg.maxBytes {
		fail(models.ScanSkipped, fmt.Sprintf("file is %d bytes, over the scanner limit", st.Size()))
		return
	}
	// Hash first: it identifies the content and lets an identical, recently scanned file reuse its
	// verdict (same organization only, same signature database, within the reuse window).
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		fail(models.ScanError, "cannot read file")
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fail(models.ScanError, "cannot read file")
		return
	}
	if a.Scanner == nil {
		fail(models.ScanError, "antimalware is not configured")
		return
	}
	sctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	version := a.scannerVersion(sctx)

	if prior, ok := a.priorVerdict(row, sum, version, cfg.reuse); ok {
		_ = f.Close() // a verdict that quarantines renames the file: it must not be held open (Windows)
		a.finishScan(row, prior.Status, prior.Signature, version, sum, st.Size(), rescan, "reused")
		return
	}
	res, err := a.Scanner.Scan(sctx, f)
	_ = f.Close() // see above: quarantine moves the file
	switch {
	case err == nil && res.Infected:
		a.finishScan(row, models.ScanInfected, res.Signature, version, sum, st.Size(), rescan, "")
	case err == nil:
		a.finishScan(row, models.ScanClean, "", version, sum, st.Size(), rescan, "")
	case errors.Is(err, antimalware.ErrTooLarge):
		fail(models.ScanSkipped, "file is over the engine's size limit")
	default:
		fail(models.ScanError, truncateRunes(err.Error(), 250))
	}
}

// priorVerdict finds an earlier verdict for IDENTICAL content in the SAME organization. A clean one
// counts only for the same signature database and while fresh; an infected one always counts.
func (a *App) priorVerdict(row models.FileScan, sum, version string, window time.Duration) (models.FileScan, bool) {
	var p models.FileScan
	if a.DB.Where("organization_id = ? AND sha256 = ? AND id <> ? AND status = ?", row.OrganizationID, sum, row.ID, models.ScanInfected).
		Order("scanned_at DESC").First(&p).Error == nil {
		return p, true
	}
	if version != "" && a.DB.Where("organization_id = ? AND sha256 = ? AND id <> ? AND status = ? AND scanner_version = ? AND scanned_at > ?",
		row.OrganizationID, sum, row.ID, models.ScanClean, version, time.Now().Add(-window)).Order("scanned_at DESC").First(&p).Error == nil {
		return p, true
	}
	return p, false
}

func (a *App) finishScan(row models.FileScan, status, signature, version, sum string, size int64, rescan bool, note string) {
	now := time.Now()
	if rescan && status == models.ScanClean { // still clean: just record the fresher check
		a.DB.Model(&models.FileScan{}).Where("id = ? AND status = ?", row.ID, models.ScanClean).
			Updates(map[string]any{"scanner_version": version, "scanned_at": now, "updated_at": now})
		return
	}
	upd := map[string]any{
		"status": status, "signature": signature, "scanner_version": version, "sha256": sum, "size_bytes": size,
		"scanned_at": now, "attempts": 0, "next_attempt_at": nil, "last_error": note, "updated_at": now,
	}
	// A verdict only lands on a row that is still waiting (or being re-checked): a replaced file (new
	// content, row reset to pending by its upload) is not touched by a stale scan of the old bytes.
	res := a.DB.Model(&models.FileScan{}).
		Where("id = ? AND storage_path = ? AND status IN ?", row.ID, row.StoragePath,
			[]string{models.ScanPending, models.ScanError, models.ScanClean}).Updates(upd)
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	if status == models.ScanInfected {
		a.quarantine(row)
		a.alertScan(row, "infected", signature)
	}
	a.auditScan(row, status, signature)
}

func (a *App) recordScanFailure(row models.FileScan, status, why string) {
	now := time.Now()
	attempts := row.Attempts + 1
	backoff := time.Duration(30<<uint(min(attempts, 7))) * time.Second
	if backoff > time.Hour {
		backoff = time.Hour
	}
	next := now.Add(backoff)
	upd := map[string]any{"status": status, "attempts": attempts, "next_attempt_at": next, "last_error": why, "updated_at": now}
	if status == models.ScanSkipped {
		upd["next_attempt_at"] = nil
		upd["scanned_at"] = now
	}
	res := a.DB.Model(&models.FileScan{}).Where("id = ? AND storage_path = ? AND status IN ?", row.ID, row.StoragePath,
		[]string{models.ScanPending, models.ScanError}).Updates(upd)
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	a.Log.Warn("Antimalware scan did not reach a verdict", "file_scan", row.ID, "source", row.Source, "status", status, "attempts", attempts, "reason", why)
	if (attempts == scanMaxAttempts || status == models.ScanSkipped) && row.AlertedAt == nil {
		a.DB.Model(&models.FileScan{}).Where("id = ?", row.ID).Update("alerted_at", now)
		a.alertScan(row, status, why)
		a.auditScan(row, status, why)
	}
}

// quarantine moves an infected file out of every served path. The row keeps pointing at the new
// location; the file's own table still names the old path, which no longer exists.
func (a *App) quarantine(row models.FileScan) {
	full, err := a.resolveSafeStoragePath(row.StoragePath)
	if err != nil {
		return
	}
	rel := filepath.Join(scanQuarantineDir, row.OrganizationID.String(), row.ID.String()+".quarantined")
	dst := filepath.Join(a.getMediaStoragePath(), rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		a.Log.Error("Quarantine directory failed", "error", err)
		return
	}
	if err := os.Rename(full, dst); err != nil {
		a.Log.Error("Could not quarantine an infected file; it stays blocked by its verdict", "file_scan", row.ID, "error", err)
		return
	}
	_ = os.Chmod(dst, 0o600)
	now := time.Now()
	a.DB.Model(&models.FileScan{}).Where("id = ?", row.ID).Updates(map[string]any{"storage_path": rel, "quarantined_at": now})
}

func (a *App) auditScan(row models.FileScan, status, detail string) {
	a.logAudit(row.OrganizationID, uuid.Nil, "file_scan", row.ID, models.AuditActionUpdated,
		map[string]any{"status": models.ScanPending},
		map[string]any{"status": status, "source": row.Source, "resource_id": row.ResourceID.String(), "detail": detail})
}

// alertScan tells the organization's administrators (never another organization's). The audit entry
// is the durable record; the WebSocket event reaches whoever is online.
func (a *App) alertScan(row models.FileScan, kind, detail string) {
	a.Log.Error("Antimalware alert", "kind", kind, "source", row.Source, "file_scan", row.ID, "org", row.OrganizationID, "detail", detail)
	if a.WSHub == nil {
		return
	}
	var members []uuid.UUID
	a.DB.Table("user_organizations").Where("organization_id = ? AND deleted_at IS NULL", row.OrganizationID).Pluck("user_id", &members)
	admins := make([]uuid.UUID, 0, len(members))
	for _, u := range members {
		if a.HasPermission(u, models.ResourceAuditLogs, models.ActionRead, row.OrganizationID) {
			admins = append(admins, u)
		}
	}
	a.WSHub.BroadcastToUsers(row.OrganizationID, admins, websocket.WSMessage{
		Type:    "file_scan_alert",
		Payload: map[string]any{"kind": kind, "source": row.Source, "id": row.ResourceID, "detail": detail},
	})
}

// scanBuffer scans bytes that are only read in memory (never stored). No engine, or no verdict, is an
// error: the caller must not touch the bytes then.
func (a *App) scanBuffer(ctx context.Context, data []byte) (antimalware.Result, error) {
	if a.Scanner == nil {
		return antimalware.Result{}, errors.New("antimalware is not configured")
	}
	sctx, cancel := context.WithTimeout(ctx, a.scanCfg().timeout)
	defer cancel()
	return a.Scanner.Scan(sctx, bytes.NewReader(data))
}

// --- worker ------------------------------------------------------------------------------------

// RunFileScanWorker drains the queue at a gentle pace until ctx ends: register files that have no
// row, scan the pending and failed ones (back-off decides when), and re-check old clean ones after a
// signature update. Safe to run on several instances: rows are leased.
func (a *App) RunFileScanWorker(ctx context.Context) {
	t := time.NewTicker(scanDefaultInterval)
	defer t.Stop()
	for {
		a.FileScanTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// FileScanTick is one pass of the worker (exported for tests).
func (a *App) FileScanTick(ctx context.Context) {
	cfg := a.scanCfg()
	if _, err := database.RegisterUnscannedFiles(a.DB, 200); err != nil {
		a.Log.Error("Registering unscanned files failed", "error", err)
	}
	if a.Scanner == nil {
		return
	}
	for _, row := range a.claimScans(cfg.batch) {
		if ctx.Err() != nil {
			return
		}
		a.runScan(ctx, row, false)
	}
	a.rescanStale(ctx, cfg)
}

// claimScans leases up to n due rows so a second worker does not take the same ones.
func (a *App) claimScans(n int) []models.FileScan {
	var rows []models.FileScan
	err := a.DB.Raw(`UPDATE file_scans SET next_attempt_at = ?, updated_at = NOW()
		WHERE id IN (SELECT id FROM file_scans
			WHERE status IN ('pending','error') AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
			ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED)
		RETURNING *`, time.Now().Add(scanLease), n).Scan(&rows).Error
	if err != nil {
		a.Log.Error("Claiming scans failed", "error", err)
	}
	return rows
}

// rescanStale re-checks clean files scanned with an older signature database, oldest first and a few
// at a time, so an update never floods the engine. A new infection found here quarantines the file.
func (a *App) rescanStale(ctx context.Context, cfg scanSettings) {
	version := a.scannerVersion(ctx)
	if version == "" {
		return
	}
	var rows []models.FileScan
	a.DB.Where("status = ? AND scanner_version <> ? AND scanned_at < ?", models.ScanClean, version, time.Now().Add(-cfg.rescanAfter)).
		Order("scanned_at").Limit(cfg.batch).Find(&rows)
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		a.runScan(ctx, row, true)
	}
}
