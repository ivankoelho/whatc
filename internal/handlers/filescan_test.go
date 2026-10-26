package handlers_test

import (
	"strings"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Antimalware policy: a file reaches a person only after a clean verdict. The engine is the fake in
// scanner_fake_test.go (EICAR = infected); nothing here runs real malware or needs clamd.

func scannerOf(app *handlers.App) *fakeScanner { return app.Scanner.(*fakeScanner) }

func eicarPDF() []byte { return append(pdfBytes(), []byte("\n"+eicarTest)...) }

func scanRow(t *testing.T, app *handlers.App, source string, id uuid.UUID) models.FileScan {
	t.Helper()
	var row models.FileScan
	require.NoError(t, app.DB.Where("source = ? AND resource_id = ?", source, id).First(&row).Error)
	return row
}

// tick runs one worker pass for the given organizations: their pending and failed files are made due, and
// everything else (rows other tests left in the shared database) is pushed out of the way, so the batch
// reaches this test's files.
func tick(t *testing.T, app *handlers.App, orgs ...uuid.UUID) {
	t.Helper()
	_, err := database.RegisterUnscannedFiles(app.DB, 1000)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(&models.FileScan{}).Where("status IN ? AND organization_id NOT IN ?", []string{models.ScanPending, models.ScanError}, orgs).
		Update("next_attempt_at", time.Now().Add(time.Hour)).Error)
	require.NoError(t, app.DB.Model(&models.FileScan{}).Where("status IN ? AND organization_id IN ?", []string{models.ScanPending, models.ScanError}, orgs).
		Update("next_attempt_at", time.Now().Add(-time.Second)).Error)
	app.FileScanTick(context.Background())
}

func (w *chatWorld) upl(t *testing.T, u *models.User, name string, data []byte) handlers.OccurrenceFileResponse {
	t.Helper()
	code, raw := w.fileUpload(t, u, w.occID, name, data)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	return uploaded(t, raw).File
}

func (w *chatWorld) dl(t *testing.T, u *models.User, fid uuid.UUID, query string) int {
	t.Helper()
	code, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, u, w.occID, fid.String(), query)
	return code
}

// 1 + 13: clean is released after the scan, and only to those who may read it.
func TestScan_CleanFileIsReleasedAfterScan(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	f := w.upl(t, w.logPorto, "orçamento.pdf", pdfBytes())
	assert.Equal(t, models.ScanClean, f.ScanStatus)
	assert.True(t, f.CanDownload)
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, ""))
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, "inline=1"))
	row := scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID)
	assert.Equal(t, "fake/1", row.ScannerVersion, "the signature database used is recorded")
	assert.NotNil(t, row.ScannedAt)

	// 13: clean is not enough without permission.
	assert.Equal(t, fasthttp.StatusForbidden, w.dl(t, w.sac, f.ID, ""))
}

// 2 + 14: infected is blocked, quarantined and audited.
func TestScan_InfectedIsBlockedQuarantinedAndAudited(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	f := w.upl(t, w.logPorto, "boleto.pdf", eicarPDF())
	assert.Equal(t, models.ScanInfected, f.ScanStatus)
	assert.False(t, f.CanDownload)
	assert.False(t, f.CanPreview)
	for _, u := range []*models.User{w.logPorto, w.supPorto, w.supSAC} {
		assert.Equal(t, fasthttp.StatusLocked, w.dl(t, u, f.ID, ""), u.FullName)
		assert.Equal(t, fasthttp.StatusLocked, w.dl(t, u, f.ID, "inline=1"), u.FullName)
	}
	row := scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID)
	assert.Equal(t, "Eicar-Test-Signature", row.Signature)
	require.NotNil(t, row.QuarantinedAt)
	var stored models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&stored, "id = ?", f.ID).Error)
	_, err := os.Stat(filepath.Join(w.app.Config.Storage.LocalPath, stored.StoragePath))
	assert.True(t, os.IsNotExist(err), "the infected file is no longer on a served path")
	_, err = os.Stat(filepath.Join(w.app.Config.Storage.LocalPath, row.StoragePath))
	assert.NoError(t, err, "it is kept, in quarantine")
	assert.Contains(t, row.StoragePath, "quarantine")
	var n int64
	w.app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ?", "file_scan", row.ID).Count(&n)
	assert.EqualValues(t, 1, n, "the finding is audited")
}

// 3 + 4: no verdict (engine down, or too slow) keeps the file closed.
func TestScan_EngineDownOrSlowKeepsFileBlocked(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	f := w.upl(t, w.logPorto, "a.pdf", pdfBytes())
	assert.Equal(t, models.ScanError, f.ScanStatus)
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""))

	scannerOf(w.app).set(func(s *fakeScanner) { s.down = false; s.delay = 5 * time.Second })
	w.app.Config.Antimalware.TimeoutSeconds = 1
	start := time.Now()
	g := w.upl(t, w.supPorto, "b.pdf", append(pdfBytes(), 'x'))
	assert.Less(t, time.Since(start), 4*time.Second, "the scan gives up at its timeout")
	assert.Equal(t, models.ScanError, g.ScanStatus)
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.supPorto, g.ID, ""))
	row := scanRow(t, w.app, models.ScanSourceOccurrenceFile, g.ID)
	assert.Contains(t, row.LastError, "deadline")
}

// 5 + 6: a file that predates the policy (no row at all) is closed on every route, then scanned
// gradually; the backfill registers it.
func TestScan_LegacyFileWithoutVerdictIsBlockedThenScanned(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	f := w.upl(t, w.logPorto, "antigo.pdf", pdfBytes())
	require.NoError(t, w.app.DB.Where("resource_id = ?", f.ID).Delete(&models.FileScan{}).Error) // as if it never had one

	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""), "direct download by id does not skip the check")
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, "inline=1"), "nor does the preview")
	_, raw, _ := w.fileCall(t, w.app.ListOccurrenceFiles, w.logPorto, w.occID, "", "")
	assert.Contains(t, string(raw), `"scan_status":"pending"`)
	assert.Contains(t, string(raw), `"can_download":false`)

	// The backfill (also run by the worker) registers it as pending; the worker then scans it.
	require.NoError(t, w.app.DB.Where("resource_id = ?", f.ID).Delete(&models.FileScan{}).Error)
	n, err := database.RegisterUnscannedFiles(w.app.DB, 100)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	assert.Equal(t, models.ScanPending, scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID).Status)
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""))
	tick(t, w.app, w.logPorto.OrganizationID)
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, ""))
}

// 8: the ZIP only carries clean files and says what it left out.
func TestScan_ZipExportOmitsFilesNotCleared(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	clean := w.upl(t, w.logPorto, "limpo.pdf", pdfBytes())
	bad := w.upl(t, w.logPorto, "infectado.pdf", eicarPDF())
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	waiting := w.upl(t, w.logPorto, "esperando.pdf", append(pdfBytes(), 'z'))
	require.Equal(t, models.ScanError, waiting.ScanStatus)

	code, body, _ := w.export(t, w.logPorto, w.occID, []string{"summary", "documents"}, "zip")
	require.Equal(t, fasthttp.StatusOK, code)
	names := []string{}
	for name := range unzip(t, body) {
		names = append(names, name)
	}
	joined := ""
	for _, n := range names {
		joined += n + "|"
	}
	assert.Contains(t, joined, clean.ID.String()[:8]+"-limpo.pdf")
	assert.NotContains(t, joined, bad.ID.String()[:8])
	assert.NotContains(t, joined, waiting.ID.String()[:8])
	// the audit entry is written a moment after the response
	require.Eventually(t, func() bool {
		var al models.AuditLog
		if w.app.DB.Where("resource_type = ? AND organization_id = ?", "occurrence_export", w.logPorto.OrganizationID).
			Order("created_at DESC").First(&al).Error != nil {
			return false
		}
		return strings.Contains(string(scanJSON(al)), `"documents_held":2`)
	}, 3*time.Second, 50*time.Millisecond)
}

func scanJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// 9: a file imported from Nextcloud goes through the same scan.
func TestScan_NextcloudImportIsScanned(t *testing.T) {
	w := newFilesWorld(t)
	w.app.Config.App.AllowPrivateNextcloud = true
	w.run()
	srv := fakeNextcloud(t, append(tinyPNG(t), []byte(eicarTest)...)) // a PNG with the test string after IEND
	org := w.logPorto.OrganizationID
	admin := chatUser(t, w.app, org, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	code, raw := chatDo(t, w.app.PutNextcloudSettings, org, admin.ID, map[string]any{"base_url": srv.URL}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	code, raw = chatDo(t, w.app.ConnectNextcloud, org, w.logPorto.ID, map[string]any{"username": "alice", "app_password": "app-secret"}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	code, raw = chatDo(t, w.app.AttachNextcloudToOccurrence, org, w.logPorto.ID, map[string]any{"path": "/foto obra.png"},
		map[string]string{"id": w.occID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	f := uploaded(t, raw).File
	assert.Equal(t, models.ScanInfected, f.ScanStatus, "the imported bytes were scanned")
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""))
}

// 10: a Chat Interno attachment is scanned; nothing opens it without a clean verdict (range, ETag and
// the uploader's own pending file included).
func TestScan_ChatAttachmentPassesTheScanner(t *testing.T) {
	f := newPhaseA(t)
	f.app.Config.Storage.LocalPath = t.TempDir()
	before := scannerOf(f.app).count()
	code, att, raw := f.upload(t, f.owner, f.ch, "foto.png", tinyPNG(t))
	require.Equal(t, fasthttp.StatusOK, code, raw)
	assert.Equal(t, models.ScanClean, att.ScanStatus)
	assert.Greater(t, scannerOf(f.app).count(), before, "it went through the scanner")
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))

	// infected
	code, bad, raw := f.upload(t, f.owner, f.ch, "mau.png", append(tinyPNG(t), []byte(eicarTest)...))
	require.Equal(t, fasthttp.StatusOK, code, raw)
	assert.Equal(t, models.ScanInfected, bad.ScanStatus)
	assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, bad.ID, "")))
	assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, bad.ID, "bytes=0-3")), "a range request is no way around")

	// a validator for a file that was never cleared must not be answered 304 either
	var stored models.ChatAttachment
	require.NoError(t, f.app.DB.First(&stored, "id = ?", bad.ID).Error)
	req := f.downloadIfNoneMatch(t, f.owner, bad.ID, `"`+stored.SHA256+`"`, "")
	assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(req))
}

// dropLeftoverMedia clears media messages other tests left in the shared test database, so the worker
// (which takes the oldest due rows first) reaches this test's files.
func dropLeftoverMedia(t *testing.T, app *handlers.App) {
	t.Helper()
	require.NoError(t, app.DB.Exec("DELETE FROM file_scans").Error)
	require.NoError(t, app.DB.Exec("DELETE FROM messages WHERE COALESCE(media_url,'') <> ''").Error)
}

// 11: WhatsApp media is closed until scanned (lazily queued if nobody registered it).
func TestScan_WhatsAppMediaIsScanned(t *testing.T) {
	app := newTestApp(t)
	dropLeftoverMedia(t, app)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "media-reader", []string{"contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	serve := func(m *models.Message) (int, string) {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "message_id", m.ID.String())
		require.NoError(t, app.ServeMedia(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	mk := func(rel string, content []byte) *models.Message {
		p := withStorageDir(t, app, rel, content)
		m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: p, Status: models.MessageStatusDelivered}
		require.NoError(t, app.DB.Create(m).Error)
		return m
	}
	good := mk("images/ok.jpg", []byte("\xFF\xD8\xFF\xE0fine"))
	code, _ := serve(good)
	assert.Equal(t, fasthttp.StatusLocked, code, "customer media is closed until scanned")
	tick(t, app, org.ID)
	code, body := serve(good)
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "\xFF\xD8\xFF\xE0fine", body)

	evil := mk("images/evil.jpg", []byte("\xFF\xD8\xFF\xE0"+eicarTest))
	serve(evil)
	tick(t, app, org.ID)
	code, _ = serve(evil)
	assert.Equal(t, fasthttp.StatusLocked, code)
	assert.Equal(t, models.ScanInfected, scanRow(t, app, models.ScanSourceMessageMedia, evil.ID).Status)
}

// 12: another organization never reads a verdict, or a file, that is not its own.
func TestScan_OrganizationsAreIsolated(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	f := w.upl(t, w.logPorto, "a.pdf", pdfBytes())
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, append([]string{"settings.general:read", "settings.general:write", "occurrences:read"}, chatUserKeys...))
	assert.NotEqual(t, fasthttp.StatusOK, w.dl(t, foreign, f.ID, ""))

	// A "clean" row that belongs to another organization does not release this organization's file.
	require.NoError(t, w.app.DB.Model(&models.FileScan{}).Where("resource_id = ?", f.ID).
		Updates(map[string]any{"organization_id": other.org.ID}).Error)
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""))

	// The administrators' status shows only their own organization.
	code, raw := chatDo(t, w.app.AntimalwareStatus, other.org.ID, foreign.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.NotContains(t, string(raw), f.ID.String())
	assert.Contains(t, string(raw), `"configured":true`)
}

// 15: reprocessing after the engine recovers (automatic, and by the administrator's button).
func TestScan_RecoversAfterTheEngineComesBack(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	f := w.upl(t, w.logPorto, "a.pdf", pdfBytes())
	assert.Equal(t, models.ScanError, f.ScanStatus)
	tick(t, w.app, w.logPorto.OrganizationID) // still down: stays closed, attempts grow
	row := scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID)
	assert.Equal(t, models.ScanError, row.Status)
	assert.GreaterOrEqual(t, row.Attempts, 2)
	assert.True(t, row.NextAttemptAt.After(time.Now()), "backs off before trying again")
	assert.Equal(t, fasthttp.StatusLocked, w.dl(t, w.logPorto, f.ID, ""))

	scannerOf(w.app).set(func(s *fakeScanner) { s.down = false })
	tick(t, w.app, w.logPorto.OrganizationID)
	assert.Equal(t, models.ScanClean, scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID).Status)
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, ""))

	// The retry button re-queues failures at once and is for administrators only.
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	g := w.upl(t, w.supPorto, "b.pdf", append(pdfBytes(), 'q'))
	assert.Equal(t, models.ScanError, g.ScanStatus)
	org := w.logPorto.OrganizationID
	admin := chatUser(t, w.app, org, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	code, _ := chatDo(t, w.app.RetryAntimalwareScans, org, w.logPorto.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, raw := chatDo(t, w.app.RetryAntimalwareScans, org, admin.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Contains(t, string(raw), `"queued":1`)
}

// 14: a persistent failure is audited (and alerted) once, not on every retry.
func TestScan_PersistentFailureIsAuditedOnce(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	f := w.upl(t, w.logPorto, "a.pdf", pdfBytes())
	for i := 0; i < 8; i++ {
		tick(t, w.app, w.logPorto.OrganizationID)
	}
	row := scanRow(t, w.app, models.ScanSourceOccurrenceFile, f.ID)
	assert.NotNil(t, row.AlertedAt)
	var n int64
	w.app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ?", "file_scan", row.ID).Count(&n)
	assert.EqualValues(t, 1, n)
}

// Identical content reuses a verdict only inside the same organization and the same signature
// database; a new database re-checks clean files and quarantines what it now recognises.
func TestScan_ReuseIsBoundAndSignatureUpdatesRecheck(t *testing.T) {
	app := newTestApp(t)
	dropLeftoverMedia(t, app)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	contactA := testutil.CreateTestContact(t, app.DB, orgA.ID)
	contactB := testutil.CreateTestContact(t, app.DB, orgB.ID)
	// every file lives under one storage root
	root := t.TempDir()
	app.Config.Storage.LocalPath = root
	put := func(rel string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("\xFF\xD8\xFF\xE0same-bytes"), 0o644))
	}
	add := func(org, contact uuid.UUID, rel string) *models.Message {
		put(rel)
		m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ContactID: contact,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: rel, Status: models.MessageStatusDelivered}
		require.NoError(t, app.DB.Create(m).Error)
		return m
	}
	a1 := add(orgA.ID, contactA.ID, "images/a1.jpg")
	tick(t, app, orgA.ID, orgB.ID)
	calls := scannerOf(app).count()
	require.Equal(t, models.ScanClean, scanRow(t, app, models.ScanSourceMessageMedia, a1.ID).Status)

	a2 := add(orgA.ID, contactA.ID, "images/a2.jpg") // same content, same org, same signatures
	tick(t, app, orgA.ID, orgB.ID)
	assert.Equal(t, models.ScanClean, scanRow(t, app, models.ScanSourceMessageMedia, a2.ID).Status)
	assert.Equal(t, calls, scannerOf(app).count(), "the same content in the same organization is not scanned twice")

	b1 := add(orgB.ID, contactB.ID, "images/b1.jpg") // same content, ANOTHER organization
	tick(t, app, orgA.ID, orgB.ID)
	assert.Equal(t, models.ScanClean, scanRow(t, app, models.ScanSourceMessageMedia, b1.ID).Status)
	assert.Greater(t, scannerOf(app).count(), calls, "another organization never borrows a verdict")

	// New signatures: clean files older than the window are re-checked; the engine now flags them.
	calls = scannerOf(app).count()
	scannerOf(app).set(func(s *fakeScanner) { s.version = "fake/2" })
	app.ForgetScannerVersion()
	app.Config.Antimalware.RescanDays = 1
	require.NoError(t, app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanClean).
		Update("scanned_at", time.Now().Add(-48*time.Hour)).Error)
	require.NoError(t, os.WriteFile(filepath.Join(root, "images/a1.jpg"), []byte("\xFF\xD8\xFF\xE0"+eicarTest), 0o644))
	tick(t, app, orgA.ID, orgB.ID)
	assert.Greater(t, scannerOf(app).count(), calls)
	assert.Equal(t, models.ScanInfected, scanRow(t, app, models.ScanSourceMessageMedia, a1.ID).Status, "a re-check that finds something blocks the file")
}

// The invoice/coupon attachment reuses its path when replaced: the old "clean" never covers new bytes.
func TestScan_DocumentAttachmentVerdictFollowsTheContent(t *testing.T) {
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{
		"documents": []map[string]any{{"type": "nf", "number": "777", "items": []map[string]any{{"description": "Piso"}}}},
	})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&occ).Error)
	var doc models.OccurrenceDocument
	require.NoError(t, app.DB.Where("occurrence_id = ?", occ.ID).First(&doc).Error)

	attach := func(data []byte) {
		up := newMultipartUploadRequestWithContentType(t, "file", "nota.pdf", data, "application/pdf")
		testutil.SetAuthContext(up, org.ID, user.ID)
		testutil.SetPathParam(up, "id", occ.ID.String())
		testutil.SetPathParam(up, "docId", doc.ID.String())
		require.NoError(t, app.UploadOccurrenceDocumentAttachment(up))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(up))
	}
	serve := func() int {
		get := testutil.NewGETRequest(t)
		testutil.SetAuthContext(get, org.ID, user.ID)
		testutil.SetPathParam(get, "id", occ.ID.String())
		testutil.SetPathParam(get, "docId", doc.ID.String())
		require.NoError(t, app.ServeOccurrenceDocumentAttachment(get))
		return testutil.GetResponseStatusCode(get)
	}
	attach(pdfBytes())
	assert.Equal(t, fasthttp.StatusOK, serve())

	scannerOf(app).set(func(s *fakeScanner) { s.down = true })
	attach(append(pdfBytes(), 'x')) // new bytes, no verdict yet
	assert.Equal(t, fasthttp.StatusLocked, serve(), "the previous clean verdict does not cover the replacement")

	scannerOf(app).set(func(s *fakeScanner) { s.down = false })
	attach(eicarPDF())
	assert.Equal(t, fasthttp.StatusLocked, serve())
	assert.Equal(t, models.ScanInfected, scanRow(t, app, models.ScanSourceOccurrenceDocument, doc.ID).Status)
}
