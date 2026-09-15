package handlers_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

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

func TestUploadLoginBackground_AcceptsRealImageDespiteMismatchedHeader(t *testing.T) {
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

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	// branding_settings is a global singleton row shared by every test in
	// this file (EnsureBrandingSettingsRow is ON CONFLICT DO NOTHING, so it
	// never resets an existing row) — snapshot the pre-call state rather
	// than assuming it starts empty, so this test is order-independent.
	var before models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&before).Error)

	// The multipart part below declares Content-Type: image/jpeg, but the
	// bytes are plain text — proves validation is by sniffed content, not
	// by the header the client sent.
	req := newMultipartUploadRequestWithContentType(t, "file", "bg.jpg", []byte("this is not an image"), "image/jpeg")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, before.LoginBackgroundPath, row.LoginBackgroundPath, "a rejected upload must not touch the stored config")
	assert.Equal(t, before.LoginBackgroundContentType, row.LoginBackgroundContentType, "a rejected upload must not touch the stored config")
}

func TestUploadLoginBackground_RejectsOversizedFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

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
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

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

// jpegMagicBytes/pngMagicBytes return the minimum byte sequence
// http.DetectContentType needs to identify each format for real —
// not a full valid image, just enough of the real file signature.
func jpegMagicBytes() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
}

func pngMagicBytes() []byte {
	return append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 32)...)
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
