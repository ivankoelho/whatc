package handlers_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
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
