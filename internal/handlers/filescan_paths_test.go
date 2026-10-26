package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
