package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shridarpatil/whatomate/internal/audit"
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
		return r.SendEnvelope(map[string]any{"login_background_url": nil, "footer_text": nil, "footer_version": nil})
	}

	resp := map[string]any{"login_background_url": nil, "footer_text": nil, "footer_version": nil}
	if row.LoginBackgroundPath != "" {
		resp["login_background_url"] = fmt.Sprintf("/api/branding/login-background?v=%d", row.UpdatedAt.Unix())
	}
	if row.FooterText != "" {
		resp["footer_text"] = row.FooterText
	}
	if row.FooterVersion != "" {
		resp["footer_version"] = row.FooterVersion
	}
	return r.SendEnvelope(resp)
}

// UpdateBrandingFooter sets the login page's footer text (copyright/rights
// notice) and version string. Text-only, so unlike UploadLoginBackground it
// needs none of that handler's file-I/O locking -- a single UPDATE is
// already atomic.
type UpdateBrandingFooterRequest struct {
	FooterText    *string `json:"footer_text"`
	FooterVersion *string `json:"footer_version"`
}

func (a *App) UpdateBrandingFooter(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	// Same system-wide-singleton reasoning as UploadLoginBackground: this
	// isn't scoped to the caller's org, so the general settings.write
	// permission alone isn't enough to prove they may change it for everyone.
	if !a.IsSuperAdmin(userID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only super admins can change system branding", nil, "")
	}

	var req UpdateBrandingFooterRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	var row models.BrandingSettings
	if err := a.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		a.Log.Error("Failed to load branding settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load branding settings", nil, "")
	}
	old := map[string]any{"footer_text": row.FooterText, "footer_version": row.FooterVersion}

	updates := map[string]any{}
	if req.FooterText != nil {
		row.FooterText = *req.FooterText
		updates["footer_text"] = *req.FooterText
	}
	if req.FooterVersion != nil {
		row.FooterVersion = *req.FooterVersion
		updates["footer_version"] = *req.FooterVersion
	}
	if len(updates) > 0 {
		if err := a.DB.Model(&models.BrandingSettings{}).
			Where("id = ?", models.BrandingSettingsSingletonID).Updates(updates).Error; err != nil {
			a.Log.Error("Failed to update branding footer", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
		}
	}

	audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID),
		models.ResourceSettingsGeneral, orgID, models.AuditActionUpdated, old, updates)

	return r.SendEnvelope(map[string]any{"footer_text": row.FooterText, "footer_version": row.FooterVersion})
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
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	// branding_settings is a system-wide singleton (no organization_id) --
	// requireAuth above only proves the caller can write settings.general in
	// THEIR org, which would let any org's admin overwrite every tenant's
	// login page. Restrict the actual mutation to super admins.
	if !a.IsSuperAdmin(userID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only super admins can change system branding", nil, "")
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

	// branding_settings has no owning org, so there's no "correct" orgID for
	// this entry. We attribute it to the acting super admin's own org (the
	// same (orgID, resourceID=orgID) shape organization.go already uses for
	// resource_type "settings.general") purely so it surfaces in the
	// AuditLogPanel of whichever org they happened to be viewing -- the true
	// affected scope is global, not that one org.
	// "uploaded_at" is present only in the new snapshot, so ComputeChanges
	// always sees a diff on it -- even when old/new login_background_path are
	// identical (replacing a JPEG with a different JPEG keeps the same
	// deterministic per-MIME-type path). Without this, a same-format
	// replacement produces a zero-change diff and LogAudit's own
	// len(changes)==0 guard silently drops the entry, the most common upload
	// case going unaudited.
	audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID),
		models.ResourceSettingsGeneral, orgID, models.AuditActionUpdated,
		map[string]any{"login_background_path": oldRelPath},
		map[string]any{"login_background_path": newRelPath, "uploaded_at": time.Now().UTC()})

	return r.SendEnvelope(map[string]any{
		"login_background_url": fmt.Sprintf("/api/branding/login-background?v=%d", time.Now().Unix()),
	})
}

// DeleteLoginBackground clears the configured background image, reverting
// the login page to its default gradient. Same lock-before-file-I/O shape as
// UploadLoginBackground — a delete racing an upload (or another delete) must
// never leave the database and disk pointing at inconsistent state.
func (a *App) DeleteLoginBackground(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	// Same system-wide-singleton reasoning as UploadLoginBackground.
	if !a.IsSuperAdmin(userID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only super admins can change system branding", nil, "")
	}

	basePath := a.getMediaStoragePath()

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
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}
	oldRelPath := row.LoginBackgroundPath

	if err := tx.Model(&row).Updates(map[string]any{
		"login_background_path":         "",
		"login_background_content_type": "",
	}).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to clear branding settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit branding settings clear", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}

	if oldRelPath != "" {
		if err := os.Remove(filepath.Join(basePath, oldRelPath)); err != nil && !os.IsNotExist(err) {
			a.Log.Error("Failed to remove branding file", "path", oldRelPath, "error", err)
		}
	}

	// Same pragmatic org attribution as UploadLoginBackground -- see comment there.
	audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID),
		models.ResourceSettingsGeneral, orgID, models.AuditActionUpdated,
		map[string]any{"login_background_path": oldRelPath},
		map[string]any{"login_background_path": ""})

	return r.SendEnvelope(map[string]any{"deleted": true})
}
