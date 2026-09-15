package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
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

// brandingAllowedContentTypes are the image types this endpoint accepts —
// the same image subset campaigns.go already allows for media uploads
// (internal/handlers/campaigns.go:887-889).
var brandingAllowedContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// brandingMaxUploadSize matches the 5MB precedent already used for audio
// uploads in this codebase's settings (SettingsView.vue's hold music/
// ringback upload UI advertises the same limit).
const brandingMaxUploadSize = 5 << 20

// UploadLoginBackground replaces the login page's background image.
//
// Five things this deliberately gets right, in order:
//  1. Real content-type detection (http.DetectContentType on the actual
//     bytes) — never trusts the multipart Content-Type header.
//  2. Atomic write: writes to a temp file, only os.Rename's it into place
//     once the write is complete and validated.
//  3. Extension changes safely: the new file's name already reflects its
//     real extension; the OLD file (possibly a different extension) is
//     deleted only after the database transaction below commits.
//  4. Concurrency: the row lock is taken BEFORE any file I/O, and held
//     across the whole read-old-path/write-new-file/update-pointer
//     sequence — not just the final UPDATE. A second concurrent upload
//     blocks until the first fully finishes and sees fresh state.
//  5. Size-bounded before any disk write (5MB).
func (a *App) UploadLoginBackground(r *fastglue.Request) error {
	_, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	fileHeader, err := r.RequestCtx.FormFile("file")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No file provided", nil, "")
	}

	file, err := fileHeader.Open()
	if err != nil {
		a.Log.Error("Failed to open uploaded file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to open uploaded file", nil, "")
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, brandingMaxUploadSize+1))
	if err != nil {
		a.Log.Error("Failed to read uploaded file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}
	if len(data) > brandingMaxUploadSize {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "File too large. Maximum size is 5MB", nil, "")
	}

	// Real content-type detection — the point #3 the design review insisted
	// on: never trust what the client's multipart header claims.
	sniffed := http.DetectContentType(data)
	if !brandingAllowedContentTypes[sniffed] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported file type: "+sniffed, nil, "")
	}

	if err := a.ensureMediaDir("branding"); err != nil {
		a.Log.Error("Failed to create branding directory", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Storage configuration error", nil, "")
	}

	ext := getExtensionFromMimeType(sniffed)
	newRelPath := filepath.Join("branding", "login-background"+ext)
	basePath := a.getMediaStoragePath()
	finalPath := filepath.Join(basePath, newRelPath)
	tmpPath := finalPath + ".tmp"

	// Lock BEFORE any file I/O — a second concurrent upload must wait here,
	// not race the file write below. Mirrors agent_transfers.go's FOR UPDATE
	// pick pattern, without SKIP LOCKED (this is a singleton, not a queue:
	// the second request must wait and see fresh state, never skip).
	tx := a.DB.Begin()
	defer func() {
		if rec := recover(); rec != nil {
			tx.Rollback()
		}
	}()

	var row models.BrandingSettings
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to lock branding settings row", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}
	oldRelPath := row.LoginBackgroundPath

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		tx.Rollback()
		a.Log.Error("Failed to write temp branding file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		tx.Rollback()
		_ = os.Remove(tmpPath)
		a.Log.Error("Failed to finalize branding file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}

	if err := tx.Model(&row).Updates(map[string]any{
		"login_background_path":         newRelPath,
		"login_background_content_type": sniffed,
	}).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to update branding settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit branding settings update", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}

	// Only after commit: the database now points at the new file, so the
	// previous one (possibly a different extension) is safe to remove.
	// Best-effort — the database is already the source of truth even if
	// this fails; a log line is enough, the request already succeeded.
	if oldRelPath != "" && oldRelPath != newRelPath {
		if err := os.Remove(filepath.Join(basePath, oldRelPath)); err != nil && !os.IsNotExist(err) {
			a.Log.Error("Failed to remove previous branding file", "path", oldRelPath, "error", err)
		}
	}

	return r.SendEnvelope(map[string]any{
		"login_background_url": fmt.Sprintf("/api/branding/login-background?v=%d", time.Now().Unix()),
	})
}
