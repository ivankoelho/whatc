package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// File attachments and voice messages in the internal chat
// (docs/superpowers/specs/2026-10-17-chat-interno-arquivos-e-audio-design.md).
//
//	POST   channels/{id}/attachments        upload one file -> pending attachment
//	DELETE channels/{id}/attachments/{aid}  discard my pending attachment
//	GET    attachments/{id}                 download (membership checked on every request)
//	POST   channels/{id}/messages           {attachment_ids} fixes them to the new message

var chatUploadsPerMinute = 30 // var so tests can lower it

const (
	chatPendingGrace    = time.Hour           // pending attachments older than this are discarded
	chatDeletedGrace    = 7 * 24 * time.Hour  // files of deleted messages are removed after this
	chatOrphanGrace     = 24 * time.Hour      // files without a row are removed after this
	chatAttachmentsRoot = "team-chat"
)

var (
	errChatFileTooLarge = &chatHTTPError{http.StatusRequestEntityTooLarge, "File too large", "file_too_large"}
	errChatFileType     = &chatHTTPError{http.StatusUnsupportedMediaType, "This type of file is not allowed", "file_type_not_allowed"}
	errChatFileInvalid  = &chatHTTPError{http.StatusBadRequest, "The file is invalid", "invalid_file"}
	errChatTooManyAtt   = &chatHTTPError{http.StatusBadRequest, "Too many attachments", "too_many_attachments"}
	errChatAttGone      = &chatHTTPError{http.StatusBadRequest, "An attachment is no longer available", "attachment_gone"}
	errChatRateLimited  = &chatHTTPError{http.StatusTooManyRequests, "Too many uploads, wait a moment", "rate_limited"}
	errChatStorage      = &chatHTTPError{http.StatusServiceUnavailable, "File storage is unavailable", "storage_unavailable"}
)

// ChatAttachmentResponse is an attachment as the client sees it: no storage path.
type ChatAttachmentResponse struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Mime       string    `json:"mime"`
	Size       int64     `json:"size"`
	Width      *int      `json:"width,omitempty"`
	Height     *int      `json:"height,omitempty"`
	DurationMs *int      `json:"duration_ms,omitempty"`
}

func chatAttachmentResponse(a models.ChatAttachment) ChatAttachmentResponse {
	return ChatAttachmentResponse{
		ID: a.ID, Kind: a.Kind, Name: a.Name, Mime: a.Mime, Size: a.SizeBytes,
		Width: a.Width, Height: a.Height, DurationMs: a.DurationMs,
	}
}

// chatUploadAllowed is a fixed one-minute window per person. It fails open when
// Redis is not available, like the login limiter.
func (a *App) chatUploadAllowed(userID uuid.UUID) bool {
	if a.Redis == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := "ratelimit:chat_upload:" + userID.String()
	n, err := a.Redis.Incr(ctx, key).Result()
	if err != nil {
		return true
	}
	if n == 1 {
		a.Redis.Expire(ctx, key, time.Minute)
	}
	return n <= int64(chatUploadsPerMinute)
}

// UploadChatAttachment stores one file as a pending attachment of the channel.
func (a *App) UploadChatAttachment(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	if ch.ArchivedAt != nil {
		return a.sendChatError(r, errChatArchived, "upload the file")
	}
	if !a.chatUploadAllowed(userID) {
		return a.sendChatError(r, errChatRateLimited, "upload the file")
	}

	form, ferr := r.RequestCtx.MultipartForm()
	if ferr != nil || len(form.File["file"]) == 0 {
		return a.sendChatError(r, errChatFileInvalid, "upload the file")
	}
	fh := form.File["file"][0]
	if fh.Size > chatMaxFileBytes {
		return a.sendChatError(r, errChatFileTooLarge, "upload the file")
	}
	f, oerr := fh.Open()
	if oerr != nil {
		return a.sendChatError(r, errChatFileInvalid, "upload the file")
	}
	defer func() { _ = f.Close() }()
	data, rerr := io.ReadAll(io.LimitReader(f, chatMaxFileBytes+1))
	if rerr != nil {
		return a.sendChatError(r, errChatFileInvalid, "upload the file")
	}
	if len(data) > chatMaxFileBytes {
		return a.sendChatError(r, errChatFileTooLarge, "upload the file")
	}

	wantAudio := len(form.Value["kind"]) > 0 && form.Value["kind"][0] == "audio"
	info, ierr := inspectChatFile(data, fh.Filename, wantAudio)
	switch ierr {
	case nil:
	case errFileType:
		return a.sendChatError(r, errChatFileType, "upload the file")
	default:
		return a.sendChatError(r, errChatFileInvalid, "upload the file")
	}
	if wantAudio && info.Kind != "audio" {
		return a.sendChatError(r, errChatFileInvalid, "upload the file")
	}

	sum := sha256.Sum256(data)
	att := models.ChatAttachment{
		ID: uuid.New(), OrganizationID: orgID, ChannelID: ch.ID, UploaderID: userID,
		Kind: info.Kind, Name: sanitizeChatFileName(fh.Filename, info.Ext), Mime: info.Mime,
		SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
	}
	att.StoragePath = filepath.Join(chatAttachmentsRoot, orgID.String(), ch.ID.String(), att.ID.String()+info.Ext)
	if info.Width > 0 {
		att.Width, att.Height = &info.Width, &info.Height
	}
	if info.Kind == "audio" && len(form.Value["duration_ms"]) > 0 {
		if ms, e := strconv.Atoi(form.Value["duration_ms"][0]); e == nil && ms >= 0 {
			if ms > chatMaxAudioMillis {
				ms = chatMaxAudioMillis
			}
			att.DurationMs = &ms
		}
	}

	// File first (temp + rename), then the row. Whatever fails afterwards removes
	// the file again, so neither a file without a row nor a row without a file stays.
	if werr := a.writeChatFile(att.StoragePath, data); werr != nil {
		a.Log.Error("Failed to store chat attachment", "error", werr)
		return a.sendChatError(r, errChatStorage, "upload the file")
	}
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessChatChannelDB(tx, userID, orgID, locked) {
			return errChatNotFound
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		var pending int64
		tx.Model(&models.ChatAttachment{}).
			Where("uploader_id = ? AND message_id IS NULL AND purged_at IS NULL", userID).Count(&pending)
		if pending >= chatMaxPending {
			return errChatTooManyAtt
		}
		return tx.Create(&att).Error
	})
	if txErr != nil {
		_ = a.removeChatFile(att.StoragePath)
		return a.sendChatError(r, txErr, "upload the file")
	}
	return r.SendEnvelope(chatAttachmentResponse(att))
}

func (a *App) writeChatFile(rel string, data []byte) error {
	full := filepath.Join(a.getMediaStoragePath(), rel)
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// removeChatFile deletes a stored file; a file that is already gone is success.
func (a *App) removeChatFile(rel string) error {
	if rel == "" {
		return nil
	}
	full, err := a.resolveSafeStoragePath(rel)
	if err != nil {
		if err == errStorageFileNotFound {
			return nil
		}
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteChatAttachment discards the caller's own pending attachment (the user
// removed it from the queue before sending).
func (a *App) DeleteChatAttachment(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	id, perr := parsePathUUID(r, "aid", "attachment")
	if perr != nil {
		return nil
	}
	var att models.ChatAttachment
	if err := a.DB.Where("id = ? AND organization_id = ? AND channel_id = ? AND uploader_id = ? AND message_id IS NULL",
		id, orgID, ch.ID, userID).First(&att).Error; err != nil {
		return a.sendChatError(r, errChatNotFound, "discard the attachment")
	}
	// Atomic against a send that attaches it at the same moment: one of them wins.
	res := a.DB.Where("id = ? AND message_id IS NULL", att.ID).Delete(&models.ChatAttachment{})
	if res.Error != nil || res.RowsAffected == 0 {
		return a.sendChatError(r, errChatNotFound, "discard the attachment")
	}
	_ = a.removeChatFile(att.StoragePath)
	return r.SendEnvelope(map[string]any{"deleted": true})
}

// attachPendingFiles fixes the caller's pending attachments to the message, inside
// the sender's transaction. The number of rows touched must equal the number asked
// for: anything unknown, foreign, already used or purged aborts the whole send.
func attachPendingFiles(tx *gorm.DB, orgID, userID, channelID, messageID uuid.UUID, ids []uuid.UUID) error {
	res := tx.Model(&models.ChatAttachment{}).
		Where("id IN ? AND organization_id = ? AND channel_id = ? AND uploader_id = ? AND message_id IS NULL AND purged_at IS NULL",
			ids, orgID, channelID, userID).
		Update("message_id", messageID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != int64(len(ids)) {
		return errChatAttGone
	}
	return nil
}

// DownloadChatAttachment streams an attachment. Access is decided on every request:
// the viewer must have access to the channel and the message must still exist (a
// pending attachment is the uploader's alone). Every refusal is a 404.
func (a *App) DownloadChatAttachment(r *fastglue.Request) error {
	notFound := func() error {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Attachment not found", nil, "")
	}
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	id, perr := parsePathUUID(r, "id", "attachment")
	if perr != nil {
		return nil
	}
	var att models.ChatAttachment
	if err := a.DB.Where("id = ? AND organization_id = ? AND purged_at IS NULL", id, orgID).First(&att).Error; err != nil {
		return notFound()
	}
	ch, ok := a.loadChatChannel(orgID, att.ChannelID)
	if !ok || !a.canAccessChatChannel(userID, orgID, ch) {
		return notFound()
	}
	if att.MessageID == nil {
		if att.UploaderID != userID {
			return notFound()
		}
	} else {
		var n int64 // the default scope hides soft-deleted messages
		a.DB.Model(&models.ChatMessage{}).Where("id = ? AND channel_id = ?", *att.MessageID, att.ChannelID).Count(&n)
		if n == 0 {
			return notFound()
		}
	}
	full, serr := a.resolveSafeStoragePath(att.StoragePath)
	if serr != nil {
		return notFound()
	}
	f, oerr := os.Open(full)
	if oerr != nil {
		return notFound()
	}
	st, serr2 := f.Stat()
	if serr2 != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return notFound()
	}

	size := st.Size()
	start, length, status := int64(0), size, fasthttp.StatusOK
	if h := string(r.RequestCtx.Request.Header.Peek("Range")); h != "" {
		s, e, valid, partial := parseByteRange(h, size)
		if !valid {
			_ = f.Close()
			r.RequestCtx.Response.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			return r.SendErrorEnvelope(fasthttp.StatusRequestedRangeNotSatisfiable, "Range not satisfiable", nil, "")
		}
		if partial {
			start, length, status = s, e-s+1, fasthttp.StatusPartialContent
			r.RequestCtx.Response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, size))
		}
	}

	hd := &r.RequestCtx.Response.Header
	hd.Set("Content-Type", att.Mime)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("Cache-Control", "private, no-cache")
	hd.Set("Accept-Ranges", "bytes")
	hd.Set("Content-Disposition", chatContentDisposition(att))
	r.RequestCtx.SetStatusCode(status)
	r.RequestCtx.SetBodyStream(&sectionCloser{io.NewSectionReader(f, start, length), f}, int(length))
	return nil
}

type sectionCloser struct {
	*io.SectionReader
	c io.Closer
}

func (s *sectionCloser) Close() error { return s.c.Close() }

// parseByteRange reads a single "bytes=a-b" range. valid=false means 416;
// partial=false with valid=true means "ignore the header, send everything".
func parseByteRange(h string, size int64) (start, end int64, valid, partial bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes=") || strings.Contains(h, ",") {
		return 0, 0, true, false
	}
	spec := strings.TrimPrefix(h, "bytes=")
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return 0, 0, false, false
	}
	a, b := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	switch {
	case a == "" && b != "": // suffix: the last n bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 || size == 0 {
			return 0, 0, false, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, true
	case a != "":
		s, err := strconv.ParseInt(a, 10, 64)
		if err != nil || s < 0 || s >= size {
			return 0, 0, false, false
		}
		e := size - 1
		if b != "" {
			v, err := strconv.ParseInt(b, 10, 64)
			if err != nil || v < s {
				return 0, 0, false, false
			}
			if v < e {
				e = v
			}
		}
		return s, e, true, true
	}
	return 0, 0, false, false
}

// chatContentDisposition: inline only for images and audio, download for the rest.
// The name is the sanitized label, as an ASCII fallback plus RFC 5987 filename*,
// so nothing the sender typed can break out of the header.
func chatContentDisposition(att models.ChatAttachment) string {
	mode := "attachment"
	if att.Kind == "image" || att.Kind == "audio" {
		mode = "inline"
	}
	var ascii, enc strings.Builder
	for _, b := range []byte(att.Name) {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '.', b == '-', b == '_':
			ascii.WriteByte(b)
			enc.WriteByte(b)
		default:
			if b < 0x80 && b != '"' && b != '\\' && b >= 0x20 {
				ascii.WriteByte('_')
			} else if b >= 0x80 {
				ascii.WriteByte('_')
			}
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, mode, ascii.String(), enc.String())
}

// chatAttachmentsFor loads the attachments of the given messages with one query.
func (a *App) chatAttachmentsFor(messageIDs []uuid.UUID) map[uuid.UUID][]models.ChatAttachment {
	out := map[uuid.UUID][]models.ChatAttachment{}
	if len(messageIDs) == 0 {
		return out
	}
	var rows []models.ChatAttachment
	a.DB.Where("message_id IN ? AND purged_at IS NULL", messageIDs).Order("created_at ASC, id ASC").Find(&rows)
	for _, r := range rows {
		out[*r.MessageID] = append(out[*r.MessageID], r)
	}
	return out
}

// ChatCleanupResult counts what one cleanup pass did (used by tests and logs).
type ChatCleanupResult struct{ Pending, Deleted, Orphans int }

// CleanupChatAttachments is the hourly housekeeping pass:
//  1. pending attachments older than an hour are discarded (row first, atomically
//     against a send that attaches them at the same moment, then the file);
//  2. files of messages deleted more than 7 days ago are removed and marked purged;
//  3. files with no row, older than a day (a crash between writing the file and the
//     row), are removed.
//
// A file that cannot be removed is kept and retried on the next pass.
func (a *App) CleanupChatAttachments(now time.Time) ChatCleanupResult {
	var res ChatCleanupResult

	var pending []models.ChatAttachment
	a.DB.Where("message_id IS NULL AND purged_at IS NULL AND created_at < ?", now.Add(-chatPendingGrace)).Find(&pending)
	for _, p := range pending {
		del := a.DB.Where("id = ? AND message_id IS NULL", p.ID).Delete(&models.ChatAttachment{})
		if del.Error == nil && del.RowsAffected == 1 {
			if err := a.removeChatFile(p.StoragePath); err != nil {
				a.Log.Error("Failed to remove pending chat file", "error", err)
			}
			res.Pending++
		}
	}

	var gone []models.ChatAttachment
	a.DB.Raw(`SELECT a.* FROM chat_attachments a JOIN chat_messages m ON m.id = a.message_id
		WHERE a.purged_at IS NULL AND m.deleted_at IS NOT NULL AND m.deleted_at < ?`, now.Add(-chatDeletedGrace)).Scan(&gone)
	for _, g := range gone {
		if err := a.removeChatFile(g.StoragePath); err != nil {
			a.Log.Error("Failed to remove chat file of a deleted message", "error", err)
			continue
		}
		a.DB.Model(&models.ChatAttachment{}).Where("id = ?", g.ID).Update("purged_at", now)
		res.Deleted++
	}

	root := filepath.Join(a.getMediaStoragePath(), chatAttachmentsRoot)
	cutoff := now.Add(-chatOrphanGrace)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		base := d.Name()
		id, perr := uuid.Parse(strings.TrimSuffix(base, filepath.Ext(base)))
		if perr == nil {
			var n int64
			a.DB.Model(&models.ChatAttachment{}).Where("id = ?", id).Count(&n)
			if n > 0 {
				return nil
			}
		}
		if os.Remove(p) == nil {
			res.Orphans++
		}
		return nil
	})
	return res
}

// RunChatAttachmentCleanup runs the housekeeping pass on an interval until ctx ends.
func (a *App) RunChatAttachmentCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r := a.CleanupChatAttachments(time.Now()); r != (ChatCleanupResult{}) {
				a.Log.Info("Chat attachment cleanup", "pending", r.Pending, "deleted", r.Deleted, "orphans", r.Orphans)
			}
		}
	}
}
