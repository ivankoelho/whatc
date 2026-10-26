package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// purgeExecutionEnabled is the master switch of the purge engine. It is a constant on purpose: this release
// ships the engine prepared and OFF, and no configuration, request or environment can turn it on. Enabling
// deletion is a code change that comes with the decisions listed in the retention documentation. (Tests
// build an engine that executes against a FAKE store through a test-only constructor; production code has
// no way to do that.)
const purgeExecutionEnabled = false

// ErrExecutionDisabled: asked to execute while the engine is off.
var ErrExecutionDisabled = errors.New("retention: purge execution is disabled in this release")

// PurgeStore is the only thing that deletes. The engine decides WHAT may go; the store does the removing.
// Both calls are idempotent: removing what is already gone succeeds.
type PurgeStore interface {
	DeleteRecord(ctx context.Context, category string, orgID, id uuid.UUID) error // the database side
	DeleteFile(ctx context.Context, path string) error                            // the stored file
}

// RetentionEngine plans, and (when enabled) executes, the removal of files that retention allows.
//
// Safety rules, all enforced here and not by the caller:
//   - it works one organization and one category at a time, in small batches;
//   - nothing is removed unless it is STILL eligible at the moment of removal: the policy is on, the term
//     has run, and no legal hold, shared reference or quarantine protects it (re-checked per item);
//   - items are claimed with row locks (SKIP LOCKED) and a lease, so two workers never take the same one;
//   - the database record goes before the file (so no record points at a missing file); if the file then
//     fails, the item stays "record_deleted" and only the file is retried;
//   - a failure is recorded and retried with a back-off, never swallowed; a run can be paused and resumed.
type RetentionEngine struct {
	a       *App
	store   PurgeStore
	execute bool
	Batch   int
	Lease   time.Duration
	Now     func() time.Time
}

// RetentionBatchResult counts what one batch did (also kept on the run).
type RetentionBatchResult struct {
	RunID   uuid.UUID `json:"run_id"`
	Planned int       `json:"planned"`
	Deleted int       `json:"deleted"`
	Skipped int       `json:"skipped"`
	Failed  int       `json:"failed"`
	Retried int       `json:"retried"`
}

// NewRetentionEngine builds the engine. In this release it can only plan.
func (a *App) NewRetentionEngine(store PurgeStore) *RetentionEngine {
	return &RetentionEngine{a: a, store: store, execute: purgeExecutionEnabled && store != nil, Batch: 50, Lease: 5 * time.Minute, Now: time.Now}
}

func (e *RetentionEngine) batch() int {
	if e.Batch <= 0 {
		return 50
	}
	return e.Batch
}

// PlanBatch records (idempotently) up to one batch of the files that are eligible now. It touches only the
// engine's own ledger: no file and no record of the product.
func (e *RetentionEngine) PlanBatch(ctx context.Context, orgID uuid.UUID, category string) (RetentionBatchResult, error) {
	var res RetentionBatchResult
	def, ok := retentionCategories[category]
	if !ok {
		return res, fmt.Errorf("unknown retention category %q", category)
	}
	months, _, why := e.a.retentionTerm(orgID, category, 0)
	run := models.RetentionRun{ID: uuid.New(), OrganizationID: orgID, Category: category, Mode: "plan", Status: models.RunRunning, StartedAt: e.Now()}
	res.RunID = run.ID
	if why != "" { // no enabled policy: nothing to plan, and that is recorded
		run.Status = models.RunFinished
		run.Counts = models.JSONB{"reason": why}
		now := e.Now()
		run.FinishedAt = &now
		return res, e.a.DB.Create(&run).Error
	}
	if err := e.a.DB.Create(&run).Error; err != nil {
		return res, err
	}
	cutoff := e.Now().AddDate(0, -months, 0)
	var ids []struct {
		ID   uuid.UUID
		Path string
	}
	q := "SELECT id, path FROM (" + classifiedSQL(def) + ") x WHERE bucket = 'eligible' ORDER BY ref_at, id LIMIT " + fmt.Sprint(e.batch())
	if err := e.a.DB.Raw(q, sql.Named("org", orgID), sql.Named("src", def.source), sql.Named("cutoff", cutoff)).Scan(&ids).Error; err != nil {
		e.finish(&run, models.RunFailed, res, err)
		return res, err
	}
	for _, c := range ids {
		item := models.RetentionItem{ID: uuid.New(), OrganizationID: orgID, Category: category, ResourceID: c.ID, RunID: run.ID, StoragePath: c.Path, Status: models.ItemPlanned}
		r := e.a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&item) // planning twice never queues twice
		if r.Error != nil {
			e.finish(&run, models.RunFailed, res, r.Error)
			return res, r.Error
		}
		res.Planned += int(r.RowsAffected)
	}
	e.finish(&run, models.RunFinished, res, nil)
	return res, nil
}

func (e *RetentionEngine) finish(run *models.RetentionRun, status string, res RetentionBatchResult, err error) {
	now := e.Now()
	upd := map[string]any{"status": status, "finished_at": now, "counts": models.JSONB{
		"planned": res.Planned, "deleted": res.Deleted, "skipped": res.Skipped, "failed": res.Failed, "retried": res.Retried}}
	if err != nil {
		upd["last_error"] = truncateRunes(err.Error(), 250)
	}
	e.a.DB.Model(&models.RetentionRun{}).Where("id = ?", run.ID).Updates(upd)
}

// stillEligible re-runs the classification for ONE item at the moment of removal.
func (e *RetentionEngine) stillEligible(orgID uuid.UUID, category string, id uuid.UUID) bool {
	def := retentionCategories[category]
	months, _, why := e.a.retentionTerm(orgID, category, 0)
	if why != "" {
		return false
	}
	cutoff := e.Now().AddDate(0, -months, 0)
	var bucket string
	e.a.DB.Raw("SELECT bucket FROM ("+classifiedSQL(def)+") x WHERE id = @rid",
		sql.Named("org", orgID), sql.Named("src", def.source), sql.Named("cutoff", cutoff), sql.Named("rid", id)).Scan(&bucket)
	return bucket == bucketEligible
}

// ExecuteBatch removes up to one batch of planned items. It refuses unless execution is enabled.
func (e *RetentionEngine) ExecuteBatch(ctx context.Context, orgID uuid.UUID, category string) (RetentionBatchResult, error) {
	var res RetentionBatchResult
	if !e.execute {
		return res, ErrExecutionDisabled
	}
	if _, ok := retentionCategories[category]; !ok {
		return res, fmt.Errorf("unknown retention category %q", category)
	}
	run := models.RetentionRun{ID: uuid.New(), OrganizationID: orgID, Category: category, Mode: "execute", Status: models.RunRunning, StartedAt: e.Now()}
	res.RunID = run.ID
	if err := e.a.DB.Create(&run).Error; err != nil {
		return res, err
	}
	// Claim with row locks and a lease: concurrent workers get disjoint items; items of a paused run wait.
	var items []models.RetentionItem
	lease := e.Now().Add(e.Lease)
	err := e.a.DB.Raw(`UPDATE retention_items SET status = CASE WHEN status = 'record_deleted' THEN 'record_deleted' ELSE 'deleting' END,
			next_attempt_at = ?, updated_at = NOW()
		WHERE id IN (SELECT i.id FROM retention_items i JOIN retention_runs r ON r.id = i.run_id
			WHERE i.organization_id = ? AND i.category = ? AND r.status <> 'paused'
			  AND i.status IN ('planned','record_deleted','failed') AND (i.next_attempt_at IS NULL OR i.next_attempt_at <= ?)
			ORDER BY i.created_at LIMIT ? FOR UPDATE OF i SKIP LOCKED)
		RETURNING *`, lease, orgID, category, e.Now(), e.batch()).Scan(&items).Error
	if err != nil {
		e.finish(&run, models.RunFailed, res, err)
		return res, err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		e.processItem(ctx, it, &res)
	}
	e.finish(&run, models.RunFinished, res, nil)
	e.a.logAudit(orgID, uuid.Nil, "retention_run", run.ID, models.AuditActionUpdated, nil,
		map[string]any{"category": category, "deleted": res.Deleted, "skipped": res.Skipped, "failed": res.Failed})
	return res, nil
}

func (e *RetentionEngine) processItem(ctx context.Context, it models.RetentionItem, res *RetentionBatchResult) {
	setStatus := func(status, errText string, backoff bool) {
		upd := map[string]any{"status": status, "last_error": errText, "updated_at": e.Now()}
		if backoff {
			upd["attempts"] = gorm.Expr("attempts + 1")
			upd["next_attempt_at"] = e.Now().Add(time.Duration(30<<uint(min(it.Attempts+1, 7))) * time.Second)
		} else {
			upd["next_attempt_at"] = nil
		}
		e.a.DB.Model(&models.RetentionItem{}).Where("id = ?", it.ID).Updates(upd)
	}
	if it.Status != models.ItemRecordGone {
		// Re-check at the moment of removal: a hold placed since planning, a policy switched off, a file now shared.
		if !e.stillEligible(it.OrganizationID, it.Category, it.ResourceID) {
			setStatus(models.ItemSkipped, "no longer eligible", false)
			res.Skipped++
			return
		}
		if err := e.store.DeleteRecord(ctx, it.Category, it.OrganizationID, it.ResourceID); err != nil {
			setStatus(models.ItemFailed, truncateRunes(err.Error(), 250), true)
			res.Failed++
			return
		}
		setStatus(models.ItemRecordGone, "", false) // the record is gone: from here only the file is retried
	} else {
		res.Retried++
	}
	if err := e.store.DeleteFile(ctx, it.StoragePath); err != nil {
		setStatus(models.ItemRecordGone, truncateRunes(err.Error(), 250), true)
		res.Failed++
		return
	}
	setStatus(models.ItemDone, "", false)
	res.Deleted++
	e.a.logAudit(it.OrganizationID, uuid.Nil, "retention_item", it.ResourceID, models.AuditActionDeleted,
		map[string]any{"category": it.Category}, nil)
}

// PauseRuns stops execution for the organization's planned work; ResumeRuns lets it continue.
func (e *RetentionEngine) PauseRuns(orgID uuid.UUID) error {
	return e.a.DB.Model(&models.RetentionRun{}).Where("organization_id = ? AND status IN ?", orgID, []string{models.RunPlanned, models.RunRunning, models.RunFinished}).
		Update("status", models.RunPaused).Error
}

func (e *RetentionEngine) ResumeRuns(orgID uuid.UUID) error {
	return e.a.DB.Model(&models.RetentionRun{}).Where("organization_id = ? AND status = ?", orgID, models.RunPaused).
		Update("status", models.RunFinished).Error
}

// Metrics summarizes the ledger of one organization (for the administration screen and alerts).
func (e *RetentionEngine) Metrics(orgID uuid.UUID) map[string]int64 {
	out := map[string]int64{}
	var rows []struct {
		Status string
		N      int64
	}
	e.a.DB.Model(&models.RetentionItem{}).Select("status, count(*) AS n").Where("organization_id = ?", orgID).Group("status").Scan(&rows)
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out
}

// fsPurgeStore is the production store, written but NOT wired: nothing constructs it in this release.
// It removes the database record by marking it (soft delete / purged_at, per category) and the file from the
// media storage, refusing any path that escapes it.
type fsPurgeStore struct{ a *App }

func (s fsPurgeStore) DeleteFile(_ context.Context, rel string) error {
	if rel == "" {
		return nil
	}
	full, err := s.a.resolveSafeStoragePath(rel)
	if err != nil {
		if errors.Is(err, errStorageFileNotFound) {
			return nil // already gone
		}
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = filepath.Clean(rel)
	return nil
}

func (s fsPurgeStore) DeleteRecord(_ context.Context, category string, orgID, id uuid.UUID) error {
	now := time.Now()
	switch category {
	case models.RetentionOccurrenceFiles:
		return s.a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.OccurrenceFile{}).Error
	case models.RetentionChatAttachments:
		return s.a.DB.Model(&models.ChatAttachment{}).Where("id = ? AND organization_id = ?", id, orgID).Update("purged_at", now).Error
	case models.RetentionOccurrenceInvoices:
		return s.a.DB.Model(&models.OccurrenceDocument{}).Where("id = ? AND organization_id = ?", id, orgID).
			Updates(map[string]any{"attachment_path": "", "attachment_name": "", "attachment_mime": ""}).Error
	case models.RetentionWhatsAppMedia:
		return s.a.DB.Model(&models.Message{}).Where("id = ? AND organization_id = ?", id, orgID).Update("media_url", "").Error
	case models.RetentionCampaignMedia:
		return s.a.DB.Model(&models.BulkMessageCampaign{}).Where("id = ? AND organization_id = ?", id, orgID).Update("header_media_local_path", "").Error
	}
	return fmt.Errorf("unknown retention category %q", category)
}
