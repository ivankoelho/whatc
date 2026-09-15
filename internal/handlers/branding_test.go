package handlers_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func TestGetPublicBranding_NullWhenNotConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"login_background_url":null`)
	assert.NotContains(t, body, "created_at", "response must never leak internal fields")
	assert.NotContains(t, body, "updated_at")
}

func TestGetPublicBranding_ReturnsURLWhenConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "branding/login-background.jpg",
			"login_background_content_type": "image/jpeg",
		}).Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, "/api/branding/login-background?v=")
}

func TestGetPublicBranding_RequiresNoAuthentication(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	// Deliberately no SetAuthContext call — proves the endpoint is genuinely
	// public, not just unchecked.
	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func TestServeLoginBackground_404WhenNotConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestServeLoginBackground_ServesConfiguredFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "branding"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "branding", "login-background.jpg"), []byte("fake-jpeg-bytes"), 0644))
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "branding/login-background.jpg",
			"login_background_content_type": "image/jpeg",
		}).Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, []byte("fake-jpeg-bytes"), testutil.GetResponseBody(req))
}

func TestServeLoginBackground_RejectsPathTraversal(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("should not be reachable"), 0644))

	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Update("login_background_path", "../"+filepath.Base(filepath.Dir(outside))+"/secret.txt").Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestServeLoginBackground_RejectsSymlink(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "branding"), 0755))

	// Real file outside storage.
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("contents"), 0644))

	// Symlink inside storage pointing to outside file.
	link := filepath.Join(dir, "branding", "linked.txt")
	require.NoError(t, os.Symlink(target, link))

	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Update("login_background_path", "branding/linked.txt").Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req),
		"symlinked background files must be rejected to prevent reading arbitrary host files")
}

func TestUploadLoginBackground_RejectedForUserWithoutPermission(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

// TestUploadLoginBackground_RejectedForOrgAdminNotSuperAdmin proves the
// upload endpoint now enforces the super-admin gate on TOP of the
// settings.general:write permission check -- an org admin who would have
// passed the old check alone must still be rejected, because
// branding_settings is a system-wide singleton, not scoped to their org.
func TestUploadLoginBackground_RejectedForOrgAdminNotSuperAdmin(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestUploadLoginBackground_AcceptsRealImageDespiteMismatchedHeader(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	req := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, "image/jpeg", row.LoginBackgroundContentType)
	assert.Equal(t, filepath.Join("branding", "login-background.jpg"), row.LoginBackgroundPath)
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))
}

func TestUploadLoginBackground_RejectsSpoofedContentType(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	// branding_settings is a global singleton row shared by every test in
	// this file (EnsureBrandingSettingsRow is ON CONFLICT DO NOTHING, so it
	// never resets an existing row) — explicitly clear it to a known state
	// (same technique TestDeleteLoginBackground_NoOpWhenAlreadyEmpty uses)
	// rather than snapshotting whatever a previous test left behind, so this
	// test's regression guard doesn't depend on execution order.
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "",
			"login_background_content_type": "",
		}).Error)

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	// The multipart part below declares Content-Type: image/jpeg, but the
	// bytes are plain text — proves validation is by sniffed content, not
	// by the header the client sent.
	req := newMultipartUploadRequestWithContentType(t, "file", "bg.jpg", []byte("this is not an image"), "image/jpeg")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Empty(t, row.LoginBackgroundPath, "a rejected upload must not touch the stored config")
	assert.Empty(t, row.LoginBackgroundContentType, "a rejected upload must not touch the stored config")
}

func TestUploadLoginBackground_RejectsOversizedFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	oversized := append(jpegMagicBytes(), make([]byte, 6<<20)...) // >5MB
	req := newMultipartUploadRequest(t, "file", "bg.jpg", oversized)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	entries, err := os.ReadDir(filepath.Join(dir, "branding"))
	if err == nil {
		for _, e := range entries {
			assert.NotContains(t, e.Name(), ".tmp", "an oversized upload must not leave a temp file behind")
		}
	}
}

func TestUploadLoginBackground_ExtensionChangeLeavesNoOrphan(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	first := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	second := newMultipartUploadRequest(t, "file", "bg.png", pngMagicBytes())
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(second))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second))

	assert.NoFileExists(t, filepath.Join(dir, "branding", "login-background.jpg"),
		"the old extension's file must be gone after a successful switch")
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.png"))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, filepath.Join("branding", "login-background.png"), row.LoginBackgroundPath)
}

// TestUploadLoginBackground_SameFormatReplacementIsAudited proves the fix for
// the re-review finding: replacing a JPEG with a DIFFERENT JPEG keeps the
// same deterministic "branding/login-background.jpg" path, so
// login_background_path never changes between old and new snapshots. Before
// the fix, ComputeChanges saw zero diffs and LogAudit's own
// len(changes)==0 guard silently dropped the entry -- the most common
// upload case (swap one image for another of the same type) went unaudited.
func TestUploadLoginBackground_SameFormatReplacementIsAudited(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	first := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	// Wait for the first upload's (real, path-empty-to-populated) audit entry
	// so the count below isolates the second upload's entry specifically.
	require.Eventually(t, func() bool {
		var count int64
		app.DB.Model(&models.AuditLog{}).
			Where("resource_type = ? AND resource_id = ? AND user_id = ?",
				models.ResourceSettingsGeneral, org.ID, user.ID).
			Count(&count)
		return count == 1
	}, 3*time.Second, 50*time.Millisecond)

	// A DIFFERENT JPEG, same extension -- login_background_path stays
	// "branding/login-background.jpg" on both sides of the diff.
	second := newMultipartUploadRequest(t, "file", "bg2.jpg", differentJpegMagicBytes())
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(second))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	require.Equal(t, filepath.Join("branding", "login-background.jpg"), row.LoginBackgroundPath,
		"precondition: the path must be unchanged for this test to prove anything")

	require.Eventually(t, func() bool {
		var count int64
		app.DB.Model(&models.AuditLog{}).
			Where("resource_type = ? AND resource_id = ? AND user_id = ?",
				models.ResourceSettingsGeneral, org.ID, user.ID).
			Count(&count)
		return count == 2
	}, 3*time.Second, 50*time.Millisecond, "same-format replacement must still write a new audit entry")

	var latest models.AuditLog
	require.NoError(t, app.DB.
		Where("resource_type = ? AND resource_id = ? AND user_id = ?",
			models.ResourceSettingsGeneral, org.ID, user.ID).
		Order("created_at DESC").First(&latest).Error)

	foundUploadedAt := false
	for _, c := range latest.Changes {
		if m, ok := c.(map[string]any); ok && m["field"] == "uploaded_at" {
			foundUploadedAt = true
		}
	}
	assert.True(t, foundUploadedAt, "the new audit entry's diff must contain the always-differing uploaded_at field")
}

// jpegMagicBytes/pngMagicBytes return the minimum byte sequence
// http.DetectContentType needs to identify each format for real —
// not a full valid image, just enough of the real file signature.
func jpegMagicBytes() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
}

func pngMagicBytes() []byte {
	return append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 32)...)
}

// differentJpegMagicBytes returns a real JPEG signature (so it sniffs to the
// same image/jpeg content type as jpegMagicBytes) with different trailing
// bytes -- a genuinely different file, same format.
func differentJpegMagicBytes() []byte {
	tail := make([]byte, 32)
	for i := range tail {
		tail[i] = 0xAA
	}
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, tail...)
}

func newMultipartUploadRequest(t *testing.T, fieldName, filename string, content []byte) *fastglue.Request {
	t.Helper()
	return newMultipartUploadRequestWithContentType(t, fieldName, filename, content, "application/octet-stream")
}

func newMultipartUploadRequestWithContentType(t *testing.T, fieldName, filename string, content []byte, contentType string) *fastglue.Request {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, filename))
	partHeader.Set("Content-Type", contentType)
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())

	return &fastglue.Request{RequestCtx: ctx}
}

func TestDeleteLoginBackground_RejectedForUserWithoutPermission(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestDeleteLoginBackground_RejectedForOrgAdminNotSuperAdmin(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestDeleteLoginBackground_ClearsConfigAndRemovesFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	uploadReq := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(uploadReq, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(uploadReq))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(uploadReq))
	require.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	deleteReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(deleteReq, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(deleteReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(deleteReq))

	assert.NoFileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Empty(t, row.LoginBackgroundPath)
	assert.Empty(t, row.LoginBackgroundContentType)

	getReq := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(getReq))
	assert.Contains(t, string(testutil.GetResponseBody(getReq)), `"login_background_url":null`)
}

func TestDeleteLoginBackground_NoOpWhenAlreadyEmpty(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	// Explicitly clear the row to ensure a clean starting state for this test,
	// since the singleton row is shared across all tests in this file and
	// a previous test may have left it populated.
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "",
			"login_background_content_type": "",
		}).Error)

	org := testutil.CreateTestOrganization(t, app.DB)
	// getUserPermissionsCached requires a role even for a super admin (it
	// looks up the role before checking the IsSuperAdmin flag) -- any role
	// works here since IsSuperAdmin short-circuits the actual permission set.
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID), testutil.WithSuperAdmin())

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "deleting an already-empty config must succeed, not error")
}
