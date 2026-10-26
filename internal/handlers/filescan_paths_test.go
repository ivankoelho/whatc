package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/shridarpatil/whatomate/internal/safehttp"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sync"
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
	"github.com/zerodha/fastglue"
)

// Closing the remaining paths: bytes that are only PROCESSED or FORWARDED are scanned in memory before
// anything touches them; files that are STORED and served carry a verdict row. In these tests the App
// has no WhatsApp client: a path that forwarded an unscanned (or infected) upload to Meta would panic,
// so a clean 422/503 is also the proof that nothing was forwarded.

func eicarJPEG() []byte { return append(jpegMagicBytes(), []byte(eicarTest)...) }

type pathWorld struct {
	app   *handlers.App
	org   *models.Organization
	admin *models.User
	super *models.User
}

func newPathWorld(t *testing.T) *pathWorld {
	t.Helper()
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	app.Config.Calling.AudioDir = t.TempDir()
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	return &pathWorld{
		app: app, org: org,
		admin: testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID)),
		super: testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithSuperAdmin()),
	}
}

func (w *pathWorld) post(t *testing.T, h func(*fastglue.Request) error, u *models.User, field, name, mime string, data []byte, path map[string]string, query string, fields map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hd := make(textproto.MIMEHeader)
	hd.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, name))
	hd.Set("Content-Type", mime)
	part, err := mw.CreatePart(hd)
	require.NoError(t, err)
	_, _ = part.Write(data)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	require.NoError(t, mw.Close())
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(mw.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())
	if query != "" {
		ctx.Request.URI().SetQueryString(query)
	}
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

// Every path that forwards or processes an upload: infected is refused, no verdict is refused, and
// nothing reaches Meta, ffmpeg or the importer in either case.
func TestScanIngress_RefusesInfectedAndUnscannedUploads(t *testing.T) {
	w := newPathWorld(t)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	w.app.DB.Model(acct).Update("app_id", "123456")
	tmpl := testutil.CreateTestTemplate(t, w.app.DB, w.org.ID, acct.Name)
	w.app.DB.Model(tmpl).Update("header_type", "IMAGE")
	camp := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, WhatsAppAccount: acct.Name,
		Name: "c", TemplateID: tmpl.ID, Status: models.CampaignStatusDraft, CreatedBy: w.admin.ID}
	require.NoError(t, w.app.DB.Create(camp).Error)

	type path struct {
		name string
		call func(data []byte) (int, []byte)
	}
	paths := []path{
		{"branding logo", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UploadLoginBackground, w.super, "file", "bg.jpg", "image/jpeg", d, nil, "", nil)
		}},
		{"template media (to Meta)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UploadTemplateMedia, w.admin, "file", "h.jpg", "image/jpeg", d, nil, "", map[string]string{"account": acct.Name})
		}},
		{"campaign media (to Meta)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UploadCampaignMedia, w.admin, "file", "h.jpg", "image/jpeg", d, map[string]string{"id": camp.ID.String()}, "", nil)
		}},
		{"profile picture (to Meta)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UpdateProfilePicture, w.admin, "file", "p.jpg", "image/jpeg", d, map[string]string{"id": acct.ID.String()}, "", nil)
		}},
		{"conversation media (to the customer)", func(d []byte) (int, []byte) {
			c := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
			return w.post(t, w.app.SendMediaMessage, w.admin, "file", "x.jpg", "image/jpeg", d, map[string]string{"id": c.ID.String()}, "", map[string]string{"type": "image", "contact_id": c.ID.String()})
		}},
		{"IVR audio (to ffmpeg)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UploadIVRAudio, w.admin, "file", "a.ogg", "audio/ogg", d, nil, "", nil)
		}},
		{"hold music (to ffmpeg)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.UploadOrgAudio, w.admin, "file", "a.ogg", "audio/ogg", d, nil, "type=hold_music", nil)
		}},
		{"CSV import (to the parser)", func(d []byte) (int, []byte) {
			return w.post(t, w.app.ImportData, w.admin, "file", "c.csv", "text/csv", d, nil, "", map[string]string{"table": "contacts"})
		}},
	}
	scanner := scannerOf(w.app)
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			scanner.set(func(s *fakeScanner) { s.down = false })
			payload := eicarJPEG()
			if p.name == "CSV import (to the parser)" {
				payload = []byte("phone_number\n5511999990000\n" + eicarTest + "\n")
			}
			if p.name[:3] == "IVR" || p.name[:4] == "hold" {
				payload = append([]byte("OggS"), []byte(eicarTest)...)
			}
			code, body := p.call(payload)
			assert.Equal(t, fasthttp.StatusUnprocessableEntity, code, "infected: %s", body)

			scanner.set(func(s *fakeScanner) { s.down = true }) // no verdict at all
			clean := jpegMagicBytes()
			if p.name == "CSV import (to the parser)" {
				clean = []byte("phone_number\n5511999990000\n")
			}
			if p.name[:3] == "IVR" || p.name[:4] == "hold" {
				clean = []byte("OggS-clean")
			}
			code, body = p.call(clean)
			assert.Equal(t, fasthttp.StatusServiceUnavailable, code, "engine down: %s", body)
		})
	}
	var ingress int64
	w.app.DB.Model(&models.AuditLog{}).Where("resource_type = ?", "file_scan_ingress").Count(&ingress)
	assert.GreaterOrEqual(t, ingress, int64(len(paths)), "each infected upload is audited")
}

// A clean CSV is imported; a clean branding image is published together with its verdict and served.
func TestScanIngress_CleanUploadsProceed(t *testing.T) {
	w := newPathWorld(t)
	code, body := w.post(t, w.app.ImportData, w.admin, "file", "c.csv", "text/csv", []byte("phone_number\n5511999990001\n"), nil, "", map[string]string{"table": "contacts"})
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	var n int64
	w.app.DB.Model(&models.Contact{}).Where("organization_id = ? AND phone_number = ?", w.org.ID, "5511999990001").Count(&n)
	assert.EqualValues(t, 1, n)

	code, body = w.post(t, w.app.UploadLoginBackground, w.super, "file", "bg.jpg", "image/jpeg", jpegMagicBytes(), nil, "", nil)
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	var row models.FileScan
	require.NoError(t, w.app.DB.Where("source = ? AND organization_id = ?", models.ScanSourceBranding, uuid.Nil).First(&row).Error)
	assert.Equal(t, models.ScanClean, row.Status)
	req := testutil.NewGETRequest(t)
	require.NoError(t, w.app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "published with its verdict, served at once")
}

// The local preview copy of a campaign's media follows the same rule as any stored file.
func TestScanGate_CampaignPreviewAndIVRAudio(t *testing.T) {
	w := newPathWorld(t)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	tmpl := testutil.CreateTestTemplate(t, w.app.DB, w.org.ID, acct.Name)
	rel := filepath.Join("campaigns", "legacy.jpg")
	require.NoError(t, os.MkdirAll(filepath.Join(w.app.Config.Storage.LocalPath, "campaigns"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(w.app.Config.Storage.LocalPath, rel), jpegMagicBytes(), 0o644))
	camp := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, WhatsAppAccount: acct.Name,
		Name: "legacy", TemplateID: tmpl.ID, Status: models.CampaignStatusDraft, CreatedBy: w.admin.ID, HeaderMediaLocalPath: rel, HeaderMediaMimeType: "image/jpeg"}
	require.NoError(t, w.app.DB.Create(camp).Error)
	serve := func() int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, w.org.ID, w.admin.ID)
		testutil.SetPathParam(req, "id", camp.ID.String())
		require.NoError(t, w.app.ServeCampaignMedia(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusLocked, serve(), "a copy that predates the policy has no verdict")
	tick(t, w.app, w.org.ID)
	assert.Equal(t, fasthttp.StatusOK, serve())

	// IVR audio: closed without a verdict, open after the scan, and another organization never opens it.
	name := "legacy-greeting.ogg"
	require.NoError(t, os.WriteFile(filepath.Join(w.app.Config.Calling.AudioDir, name), []byte("OggS-audio"), 0o644))
	play := func(u *models.User) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, u.OrganizationID, u.ID)
		testutil.SetPathParam(req, "filename", name)
		require.NoError(t, w.app.ServeIVRAudio(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusLocked, play(w.admin))
	tick(t, w.app, w.org.ID)
	assert.Equal(t, fasthttp.StatusOK, play(w.admin))
	other := newIsoWorld(t)
	orole := testutil.CreateAdminRole(t, w.app.DB, other.org.ID)
	foreign := testutil.CreateTestUser(t, w.app.DB, other.org.ID, testutil.WithRoleID(&orole.ID))
	assert.Equal(t, fasthttp.StatusLocked, play(foreign), "the verdict is the uploader organization's")
}

// Two workers on the same queue never scan the same file twice, and nothing is left behind.
func TestScanQueue_ConcurrentWorkersTakeEachFileOnce(t *testing.T) {
	app := newTestApp(t)
	dropLeftoverMedia(t, app)
	root := t.TempDir()
	app.Config.Storage.LocalPath = root
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	const n = 24
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("images/f%02d.jpg", i)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "images"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(fmt.Sprintf("\xFF\xD8\xFF\xE0distinct-%d", i)), 0o644))
		require.NoError(t, app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: rel, Status: models.MessageStatusDelivered}).Error)
	}
	app.Config.Antimalware.BatchSize = 5
	// another instance on the same database, with its own engine
	second := &handlers.App{Config: app.Config, DB: app.DB, Log: app.Log, Redis: app.Redis, Scanner: newFakeScanner()}
	var wg sync.WaitGroup
	for _, a := range []*handlers.App{app, second} {
		wg.Add(1)
		go func(a *handlers.App) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				a.FileScanTick(context.Background())
				time.Sleep(5 * time.Millisecond)
			}
		}(a)
	}
	wg.Wait()
	var clean int64
	app.DB.Model(&models.FileScan{}).Where("organization_id = ? AND status = ?", org.ID, models.ScanClean).Count(&clean)
	assert.EqualValues(t, n, clean, "every file was cleared")
	assert.Equal(t, n, scannerOf(app).count()+second.Scanner.(*fakeScanner).count(), "and each was scanned exactly once across both workers")
}

// What the administrators can see: engine state, signature age and a stale alert flag, counts, backlog
// and average scan time; restricted to settings.general readers and to their own organization.
func TestAntimalwareStatus_ObservabilityAndAccess(t *testing.T) {
	w := newPathWorld(t)
	cleanCase := w.app
	_ = cleanCase
	media := withStorageDir(t, w.app, "images/x.jpg", []byte("\xFF\xD8\xFF\xE0observe"))
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: media, Status: models.MessageStatusDelivered}).Error)
	tick(t, w.app, w.org.ID)

	type status struct {
		Data struct {
			Reachable   bool   `json:"reachable"`
			Engine      string `json:"engine"`
			Stale       bool   `json:"signatures_stale"`
			AgeHours    int    `json:"signatures_age_hours"`
			Performance struct {
				Scans24h  int64 `json:"scans_24h"`
				AvgScanMs int64 `json:"avg_scan_ms"`
			} `json:"performance"`
			Backlog struct {
				Pending       int64 `json:"pending"`
				Errors        int64 `json:"errors"`
				OldUnverified int64 `json:"old_unverified"`
			} `json:"backlog"`
		} `json:"data"`
	}
	get := func(u *models.User) (int, status) {
		code, raw := chatDo(t, w.app.AntimalwareStatus, u.OrganizationID, u.ID, nil, nil, nil)
		var s status
		_ = json.Unmarshal(raw, &s)
		return code, s
	}
	// fresh signatures: not stale
	scannerOf(w.app).set(func(s *fakeScanner) {
		s.version = "ClamAV 1.5.4/28164/" + time.Now().UTC().Add(-2*time.Hour).Format("Mon Jan _2 15:04:05 2006")
	})
	code, s := get(w.admin)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.True(t, s.Data.Reachable)
	assert.False(t, s.Data.Stale)
	assert.LessOrEqual(t, s.Data.AgeHours, 3)
	assert.EqualValues(t, 1, s.Data.Performance.Scans24h, "the scan was timed")
	assert.EqualValues(t, 0, s.Data.Backlog.Pending)

	// old signatures: flagged stale
	scannerOf(w.app).set(func(s *fakeScanner) { s.version = "ClamAV 1.5.4/20000/Mon Jan  5 06:00:00 2026" })
	_, s = get(w.admin)
	assert.True(t, s.Data.Stale)

	// engine down: unreachable, and the backlog counts what cannot be cleared
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	media2 := withStorageDir(t, w.app, "images/y.jpg", []byte("\xFF\xD8\xFF\xE0observe-2"))
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: media2, Status: models.MessageStatusDelivered}).Error)
	tick(t, w.app, w.org.ID)
	_, s = get(w.admin)
	assert.False(t, s.Data.Reachable)
	assert.EqualValues(t, 1, s.Data.Backlog.Errors)

	// only administrators who may read settings; and never another organization's numbers
	agentRole := testutil.CreateAgentRole(t, w.app.DB, w.org.ID)
	agent := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&agentRole.ID))
	code, _ = get(agent)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	other := newIsoWorld(t)
	orole := testutil.CreateAdminRole(t, w.app.DB, other.org.ID)
	foreign := testutil.CreateTestUser(t, w.app.DB, other.org.ID, testutil.WithRoleID(&orole.ID))
	_, s = get(foreign)
	assert.EqualValues(t, 0, s.Data.Backlog.Errors)
	assert.EqualValues(t, 0, s.Data.Performance.Scans24h)
}

// The migration diagnostic is read-only and tells the truth about what has no verdict.
func TestScanReport_IsReadOnlyAndCountsWhatHasNoVerdict(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	clean := w.upl(t, w.logPorto, "limpo.pdf", pdfBytes())
	legacy := w.upl(t, w.logPorto, "antigo.pdf", append(pdfBytes(), 'L'))
	require.NoError(t, w.app.DB.Where("resource_id = ?", legacy.ID).Delete(&models.FileScan{}).Error) // predates the policy
	_ = clean
	// WhatsApp media: its size is only known on disk
	media := withStorageDir(t, w.app, "images/m.jpg", []byte("\xFF\xD8\xFF\xE0twelve-bytes"))
	contact := testutil.CreateTestContact(t, w.app.DB, w.logPorto.OrganizationID)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.logPorto.OrganizationID, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: media, Status: models.MessageStatusDelivered}).Error)

	var before int64
	w.app.DB.Model(&models.FileScan{}).Count(&before)
	rep, err := database.BuildScanReport(w.app.DB, w.app.Config.Storage.LocalPath, true)
	require.NoError(t, err)
	var after int64
	w.app.DB.Model(&models.FileScan{}).Count(&after)
	assert.Equal(t, before, after, "the report writes nothing")

	by := map[string]database.ScanReportLine{}
	for _, l := range rep.Lines {
		by[l.Source] = l
	}
	of := by[models.ScanSourceOccurrenceFile]
	assert.GreaterOrEqual(t, of.Total, int64(2))
	assert.GreaterOrEqual(t, of.Unregistered, int64(1), "the legacy file has no row")
	assert.GreaterOrEqual(t, of.NoVerdict, int64(1))
	assert.Positive(t, of.NoVerdictB)
	mm := by[models.ScanSourceMessageMedia]
	assert.GreaterOrEqual(t, mm.Total, int64(1))
	assert.Positive(t, mm.Bytes, "sizes of files not measured in the database come from disk")
	assert.Equal(t, rep.NoVerdict, of.NoVerdict+by[models.ScanSourceChatAttachment].NoVerdict+by[models.ScanSourceOccurrenceDocument].NoVerdict+mm.NoVerdict+by[models.ScanSourceCampaignMedia].NoVerdict)

	// the arithmetic of the estimate: 1 800 files at 5 per 10 s is one hour; 36 GB at the assumed 10 MB/s is longer
	e := database.EstimateScan(database.ScanReport{NoVerdict: 1800, NoVerdictB: 1 << 20}, 5, 10*time.Second)
	assert.Equal(t, time.Hour, e.ByWorker.Round(time.Second))
	assert.True(t, e.EngineAssumed)
	e = database.EstimateScan(database.ScanReport{NoVerdict: 10, NoVerdictB: 36 << 30}, 5, 10*time.Second)
	assert.Greater(t, e.ByEngine, e.ByWorker)
	assert.Equal(t, e.ByEngine, e.Total, "the slower bound decides")
}

// The conversation screen learns which media are still being checked, with the media's own visibility
// and without anything about the scanner.
func TestMediaScanStatus_FollowsMediaVisibility(t *testing.T) {
	app := newTestApp(t)
	dropLeftoverMedia(t, app)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "media-reader", []string{"contacts:read"})
	reader := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	mk := func(rel string, b []byte) *models.Message {
		p := withStorageDir(t, app, rel, b)
		m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: p, Status: models.MessageStatusDelivered}
		require.NoError(t, app.DB.Create(m).Error)
		return m
	}
	waiting := mk("images/a.jpg", []byte("\xFF\xD8\xFF\xE0a"))
	text := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Status: models.MessageStatusDelivered}
	require.NoError(t, app.DB.Create(text).Error)

	ask := func(u *models.User, ids ...uuid.UUID) map[string]string {
		strs := ""
		for i, id := range ids {
			if i > 0 {
				strs += ","
			}
			strs += id.String()
		}
		req := testutil.NewGETRequest(t)
		req.RequestCtx.QueryArgs().Parse("ids=" + strs)
		testutil.SetAuthContext(req, u.OrganizationID, u.ID)
		require.NoError(t, app.MediaScanStatus(req))
		var env struct {
			Data struct {
				Statuses map[string]string `json:"statuses"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
		return env.Data.Statuses
	}
	got := ask(reader, waiting.ID, text.ID, uuid.New())
	assert.Equal(t, map[string]string{waiting.ID.String(): models.ScanPending}, got, "no row yet is pending; non-media and unknown ids are absent")
	tick(t, app, org.ID)
	assert.Equal(t, models.ScanClean, ask(reader, waiting.ID)[waiting.ID.String()])

	// another organization sees nothing of it
	other := newIsoWorld(t)
	orole := testutil.CreateTestRoleWithKeys(t, app.DB, other.org.ID, "media-reader", []string{"contacts:read"})
	foreign := testutil.CreateTestUser(t, app.DB, other.org.ID, testutil.WithRoleID(&orole.ID))
	assert.Empty(t, ask(foreign, waiting.ID))
}

type fakeFetcher struct {
	res   *safehttp.Result
	err   error
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, u string, _ int64) (*safehttp.Result, error) {
	f.calls = append(f.calls, u)
	return f.res, f.err
}

// A template header typed as a URL goes through the safe client, never a raw http.Get; what comes back is
// scanned before WhatsApp sees it, and refusals say nothing about the server's network.
func TestTemplateHeaderURL_UsesTheSafeFetcherAndTheScanner(t *testing.T) {
	w := newPathWorld(t)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	tmpl := testutil.CreateTestTemplate(t, w.app.DB, w.org.ID, acct.Name)
	require.NoError(t, w.app.DB.Model(tmpl).Update("header_type", "IMAGE").Error)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	send := func(url string) (int, string) {
		req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "template_id": tmpl.ID.String(), "header_media_url": url, "template_params": map[string]string{"1": "Ana"}})
		testutil.SetAuthContext(req, w.admin.OrganizationID, w.admin.ID)
		require.NoError(t, w.app.SendTemplateMessage(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	ff := &fakeFetcher{}
	w.app.MediaFetcher = ff

	ff.err = fmt.Errorf("%w: 10.0.0.5", safehttp.ErrBlockedAddress)
	code, body := send("https://intranet.example/logo.png")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.NotContains(t, body, "10.0.0.5", "the answer does not reveal the internal address")
	assert.Equal(t, []string{"https://intranet.example/logo.png"}, ff.calls)

	ff.err = safehttp.ErrTooLarge
	code, _ = send("https://cdn.example/huge.png")
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, code)

	// fetched bytes are still scanned: infected is refused, and with the engine down nothing is forwarded
	// (the App has no WhatsApp client: forwarding would panic)
	ff.err, ff.res = nil, &safehttp.Result{Data: eicarJPEG(), ContentType: "image/jpeg"}
	code, _ = send("https://cdn.example/evil.jpg")
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, code)
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	ff.res = &safehttp.Result{Data: jpegMagicBytes(), ContentType: "image/jpeg"}
	code, _ = send("https://cdn.example/ok.jpg")
	assert.Equal(t, fasthttp.StatusServiceUnavailable, code)
}

// Telephony plays only audio with a clean verdict, decided from memory during the call; older files are
// pre-verified in the background.
func TestAudioGate_LegacyAudioIsPreVerifiedAndNeverScannedDuringACall(t *testing.T) {
	w := newPathWorld(t)
	dir := w.app.Config.Calling.AudioDir
	uniq := uuid.NewString()[:8] // the shared test database keeps verdicts of other tests: names must be unique
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, uniq+"-"+name)
		require.NoError(t, os.WriteFile(p, b, 0o644))
		return p
	}
	gate := w.app.AudioGate()
	w.app.Config.Antimalware.BatchSize = 100 // rows of other tests (system-owned) share the queue
	legacy := put("legacy-greeting.ogg", []byte("OggS-old-greeting"))
	evil := put("legacy-evil.ogg", append([]byte("OggS"), []byte(eicarTest)...))
	orgB := newIsoWorld(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "org_"+orgB.org.ID.String()+"_hold_music.ogg"), []byte("OggS-other-org"), 0o644)) // the org id is already unique

	// before any verification: closed, and asking costs nothing (no scan, no I/O)
	scannerOf(w.app).set(func(s *fakeScanner) { s.delay = 5 * time.Second })
	began := time.Now()
	assert.False(t, gate.Approved(legacy))
	assert.Less(t, time.Since(began), 50*time.Millisecond, "a call never waits for a scan")
	scannerOf(w.app).set(func(s *fakeScanner) { s.delay = 0 })

	// the engine is down: still closed after the background pass
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = true })
	tick(t, w.app, uuid.Nil, orgB.org.ID)
	assert.False(t, gate.Approved(legacy), "no verdict, no playback")
	scannerOf(w.app).set(func(s *fakeScanner) { s.down = false })

	// the engine is back: the older file is cleared in the background and joins the gate; the infected one never does
	tick(t, w.app, uuid.Nil, orgB.org.ID)
	assert.True(t, gate.Approved(legacy))
	assert.False(t, gate.Approved(evil))
	var row models.FileScan
	// quarantine moved the row's path: find it by its resource id
	require.NoError(t, w.app.DB.Where("source = ? AND organization_id = ? AND status = ? AND storage_path LIKE ?", models.ScanSourceIVRAudio, uuid.Nil, models.ScanInfected, "quarantine%").First(&row).Error)
	assert.Equal(t, models.ScanInfected, row.Status)
	_, err := os.Stat(evil)
	assert.True(t, os.IsNotExist(err), "the infected audio was moved to quarantine")
	var orgRow models.FileScan
	require.NoError(t, w.app.DB.Where("source = ? AND storage_path = ?", models.ScanSourceIVRAudio, "org_"+orgB.org.ID.String()+"_hold_music.ogg").First(&orgRow).Error)
	assert.Equal(t, orgB.org.ID, orgRow.OrganizationID, "organization audio is registered to its organization")

	// paths that are not plain files of the audio directory are never approved, whatever their name
	assert.False(t, gate.Approved(filepath.Join(dir, "..", uniq+"-legacy-greeting.ogg")))
	assert.False(t, gate.Approved(filepath.Join(dir, "sub", uniq+"-legacy-greeting.ogg")))
	assert.False(t, gate.Approved(filepath.Join(t.TempDir(), uniq+"-legacy-greeting.ogg")))

	// a withdrawn verdict leaves the gate on the next refresh
	require.NoError(t, w.app.DB.Model(&models.FileScan{}).Where("source = ? AND storage_path = ?", models.ScanSourceIVRAudio, uniq+"-legacy-greeting.ogg").
		Update("status", models.ScanInfected).Error)
	gate.Refresh()
	assert.False(t, gate.Approved(legacy))

	// the editor's download follows the same verdict, for the organization that may see it
	foreignUser := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&testutil.CreateAdminRole(t, w.app.DB, w.org.ID).ID))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, foreignUser.OrganizationID, foreignUser.ID)
	testutil.SetPathParam(req, "filename", "org_"+orgB.org.ID.String()+"_hold_music.ogg")
	require.NoError(t, w.app.ServeIVRAudio(req))
	assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(req), "another organization's audio is not opened by name")
}

// Campaigns and template sends accept only Meta media ids this server uploaded after a scan; older campaigns
// are reported, not invalidated; an infected campaign is not sent (worker test).
func TestCampaignMedia_OnlyServerUploadedIdsAreAccepted(t *testing.T) {
	w := newPathWorld(t)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	tmpl := testutil.CreateTestTemplate(t, w.app.DB, w.org.ID, acct.Name)
	require.NoError(t, w.app.DB.Model(tmpl).Update("header_type", "IMAGE").Error)
	create := func(mediaID string) int {
		req := testutil.NewJSONRequest(t, map[string]any{"name": "c-" + uuid.NewString()[:6], "whatsapp_account": acct.Name,
			"template_id": tmpl.ID.String(), "header_media_id": mediaID})
		testutil.SetAuthContext(req, w.admin.OrganizationID, w.admin.ID)
		require.NoError(t, w.app.CreateCampaign(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, create("media-from-somewhere-else"), "an id WhatC never uploaded is refused")
	assert.NotEqual(t, fasthttp.StatusBadRequest, create(""), "no id is fine: the upload endpoint sets it after the scan")
	require.NoError(t, w.app.DB.Create(&models.MetaMedia{ID: uuid.New(), OrganizationID: w.org.ID, MediaID: "media-uploaded-here", Source: "campaign_media"}).Error)
	assert.NotEqual(t, fasthttp.StatusBadRequest, create("media-uploaded-here"))
	// another organization's upload does not vouch for this one
	other := newIsoWorld(t)
	require.NoError(t, w.app.DB.Create(&models.MetaMedia{ID: uuid.New(), OrganizationID: other.org.ID, MediaID: "media-of-another-org", Source: "campaign_media"}).Error)
	assert.Equal(t, fasthttp.StatusBadRequest, create("media-of-another-org"))

	// the template send's "pre-uploaded" option follows the same rule (no WhatsApp client here: reaching Meta would panic)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "template_id": tmpl.ID.String(),
		"header_media_id": "media-from-somewhere-else", "template_params": map[string]string{"1": "Ana"}})
	testutil.SetAuthContext(req, w.admin.OrganizationID, w.admin.ID)
	require.NoError(t, w.app.SendTemplateMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	// older campaigns with an id nobody uploaded here are reported to the administrators, and stay untouched
	old := &models.BulkMessageCampaign{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, WhatsAppAccount: acct.Name,
		Name: "antiga", TemplateID: tmpl.ID, Status: models.CampaignStatusDraft, CreatedBy: w.admin.ID, HeaderMediaID: "media-legacy"}
	require.NoError(t, w.app.DB.Create(old).Error)
	code, raw := chatDo(t, w.app.AntimalwareStatus, w.org.ID, w.admin.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Contains(t, string(raw), `"campaigns_unverified_media":1`)
	var still models.BulkMessageCampaign
	require.NoError(t, w.app.DB.First(&still, "id = ?", old.ID).Error)
	assert.Equal(t, "media-legacy", still.HeaderMediaID, "never invalidated")
}
