package handlers

import (
	"errors"
	"fmt"
	"os"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GetPublicBranding returns the login page's background image URL, or null
// when none is configured. Public — the login page renders before anyone is
// authenticated, so this cannot require auth. The response is deliberately
// minimal: only login_background_url, never created_at/updated_at/id or any
// other internal field.
func (a *App) GetPublicBranding(r *fastglue.Request) error {
	var row models.BrandingSettings
	if err := a.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		a.Log.Error("Failed to load branding settings", "error", err)
		return r.SendEnvelope(map[string]any{"login_background_url": nil})
	}

	if row.LoginBackgroundPath == "" {
		return r.SendEnvelope(map[string]any{"login_background_url": nil})
	}

	url := fmt.Sprintf("/api/branding/login-background?v=%d", row.UpdatedAt.Unix())
	return r.SendEnvelope(map[string]any{"login_background_url": url})
}

// ServeLoginBackground serves the configured background image file. Public,
// same path-traversal/symlink protection as ServeMedia (media.go).
func (a *App) ServeLoginBackground(r *fastglue.Request) error {
	var row models.BrandingSettings
	if err := a.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil || row.LoginBackgroundPath == "" {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No background image configured", nil, "")
	}

	fullPath, err := a.resolveSafeStoragePath(row.LoginBackgroundPath)
	if err != nil {
		switch {
		case errors.Is(err, errStorageConfig):
			a.Log.Error("Storage configuration error", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Storage configuration error", nil, "")
		case errors.Is(err, errStorageFileNotFound):
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "File not found", nil, "")
		default:
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid file path", nil, "")
		}
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		a.Log.Error("Failed to read branding file", "path", fullPath, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}

	contentType := row.LoginBackgroundContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	r.RequestCtx.Response.Header.Set("Content-Type", contentType)
	r.RequestCtx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	r.RequestCtx.SetBody(data)
	return nil
}
