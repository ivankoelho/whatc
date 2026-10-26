package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// General documents of a case (Word, Excel, PDF, images, NF-e XML...). They are NOT the
// invoice/coupon records (OccurrenceDocument): any number of files, from any collaborator
// who works the case, each with its own author, time and visibility.
//
// Who may do what:
//   - list      anyone who can open the case (names, sizes, authors; never the bytes)
//   - download  those who work the case (assignee, team, supervisors), the person who sent it,
//               and, for files not marked restricted, everyone who can open the case
//   - upload    those who work the case, or a participant allowed to comment
//   - delete    the sender, or a supervisor of the case
// Every download and removal is audited; sends and removals are also case history events.

const (
	occurrenceFilesRoot     = "occurrence-files"
	occurrenceFileMaxName   = 255
	occurrenceFileDedupeFor = 30 * time.Second
	occurrenceFileDefaultMB = 15
)

var (
	errOccFileType     = &chatHTTPError{http.StatusUnsupportedMediaType, "This type of file is not allowed", "file_type_not_allowed"}
	errOccFileInvalid  = &chatHTTPError{http.StatusBadRequest, "The file is invalid", "invalid_file"}
	errOccFileTooLarge = &chatHTTPError{http.StatusRequestEntityTooLarge, "File too large", "file_too_large"}
	errOccFileForbid   = &chatHTTPError{http.StatusForbidden, "You cannot do this with this file", "forbidden"}
	errOccFileGone     = &chatHTTPError{http.StatusNotFound, "File not found", "not_found"}
	errOccFileStorage  = &chatHTTPError{http.StatusServiceUnavailable, "File storage is unavailable", "storage_unavailable"}
)

func (a *App) occurrenceFileMaxBytes() int {
	mb := a.Config.App.OccurrenceFileMaxMB
	if mb <= 0 {
		mb = occurrenceFileDefaultMB
	}
	return mb << 20
}

// occurrenceFileInfo is what inspection decided about the bytes.
type occurrenceFileInfo struct {
	Kind string // image | file
	Mime string
	Ext  string
}

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// inspectOccurrenceFile decides what a case document is from its BYTES: the closed list of the chat
// (PDF, images, docx/xlsx/pptx, txt/csv) plus legacy Word/Excel (OLE) and NF-e XML. Executables,
// scripts, html/svg, macro-enabled Office files and archives that look like bombs are refused.
func inspectOccurrenceFile(data []byte, rawName string) (occurrenceFileInfo, error) {
	if len(data) == 0 {
		return occurrenceFileInfo{}, errOccFileInvalid
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(rawName), "."))
	// Anything dangerous anywhere in the name, except the .xml we handle ourselves below.
	if ext != "xml" && hasDangerousExt(rawName) {
		return occurrenceFileInfo{}, errOccFileType
	}
	if ext == "xml" {
		if hasDangerousExt(strings.TrimSuffix(rawName, path.Ext(rawName))) {
			return occurrenceFileInfo{}, errOccFileType
		}
		if err := checkPlainXML(data); err != nil {
			return occurrenceFileInfo{}, err
		}
		return occurrenceFileInfo{Kind: "file", Mime: "application/xml", Ext: ".xml"}, nil
	}
	if bytes.HasPrefix(data, oleMagic) {
		switch ext {
		case "doc":
			return occurrenceFileInfo{Kind: "file", Mime: "application/msword", Ext: ".doc"}, nil
		case "xls":
			return occurrenceFileInfo{Kind: "file", Mime: "application/vnd.ms-excel", Ext: ".xls"}, nil
		}
		return occurrenceFileInfo{}, errOccFileType
	}
	info, err := inspectChatFile(data, rawName, false)
	if err != nil {
		if errors.Is(err, errFileType) {
			return occurrenceFileInfo{}, errOccFileType
		}
		return occurrenceFileInfo{}, errOccFileInvalid
	}
	if info.Kind == "audio" {
		return occurrenceFileInfo{}, errOccFileType
	}
	return occurrenceFileInfo{Kind: info.Kind, Mime: info.Mime, Ext: info.Ext}, nil
}

// checkPlainXML accepts well-formed XML with no DTD or entity declarations (no external entities
// to resolve, no entity-expansion tricks). It does not judge whether the content is a real NF-e.
func checkPlainXML(data []byte) error {
	head := bytes.ToUpper(data)
	if bytes.Contains(head, []byte("<!DOCTYPE")) || bytes.Contains(head, []byte("<!ENTITY")) {
		return errOccFileInvalid
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil
	tokens := 0
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errOccFileInvalid
		}
		if tokens++; tokens > 2_000_000 {
			return errOccFileInvalid
		}
	}
	if tokens == 0 {
		return errOccFileInvalid
	}
	return nil
}

// OccurrenceFileResponse is a file as listed: metadata only.
type OccurrenceFileResponse struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Mime         string    `json:"mime"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"size_bytes"`
	Restricted   bool      `json:"restricted"`
	UploadedBy   uuid.UUID `json:"uploaded_by_id"`
	UploaderName string    `json:"uploaded_by_name"`
	CreatedAt    time.Time `json:"created_at"`
	CanDownload  bool      `json:"can_download"`
	CanDelete    bool      `json:"can_delete"`
	CanPreview   bool      `json:"can_preview"`
	// ScanStatus is the antimalware verdict: only "clean" can be opened.
	ScanStatus string `json:"scan_status"`
}

func (a *App) canDownloadOccurrenceFile(userID uuid.UUID, acc occurrenceAccess, f *models.OccurrenceFile) bool {
	return workTheCase(acc) || f.UploadedByID == userID || !f.Restricted
}

func (a *App) canDeleteOccurrenceFile(userID uuid.UUID, acc occurrenceAccess, f *models.OccurrenceFile) bool {
	return f.UploadedByID == userID || acc.supervisor
}

func previewable(mime string) bool {
	switch mime {
	case "application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp", "text/plain":
		return true
	}
	return false
}

func (a *App) occurrenceFileResponses(userID uuid.UUID, acc occurrenceAccess, files []models.OccurrenceFile, canWrite bool) []OccurrenceFileResponse {
	ids := make([]uuid.UUID, len(files))
	for i, f := range files {
		ids[i] = f.UploadedByID
	}
	names := a.chatUserNames(ids)
	fileIDs := make([]uuid.UUID, len(files))
	for i, f := range files {
		fileIDs[i] = f.ID
	}
	var scanOrg uuid.UUID
	if len(files) > 0 {
		scanOrg = files[0].OrganizationID
	}
	scans := a.scanStatuses(scanOrg, models.ScanSourceOccurrenceFile, fileIDs)
	out := make([]OccurrenceFileResponse, len(files))
	for i := range files {
		f := &files[i]
		st := scanStatusOf(scans, f.ID)
		dl := a.canDownloadOccurrenceFile(userID, acc, f) && st == models.ScanClean
		out[i] = OccurrenceFileResponse{
			ID: f.ID, Name: f.Name, Mime: f.Mime, Kind: f.Kind, SizeBytes: f.SizeBytes, Restricted: f.Restricted,
			UploadedBy: f.UploadedByID, UploaderName: names[f.UploadedByID], CreatedAt: f.CreatedAt,
			CanDownload: dl, CanPreview: dl && previewable(f.Mime),
			CanDelete: canWrite && a.canDeleteOccurrenceFile(userID, acc, f), ScanStatus: st,
		}
	}
	return out
}

// ListOccurrenceFiles lists the documents of a case the caller can open.
func (a *App) ListOccurrenceFiles(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var files []models.OccurrenceFile
	a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).Order("created_at DESC, id DESC").Find(&files)
	canWrite := a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID)
	return r.SendEnvelope(map[string]any{
		"files":      a.occurrenceFileResponses(userID, acc, files, canWrite),
		"can_upload": canWrite && (workTheCase(acc) || acc.can(models.OccurrenceCapComment)),
		"max_bytes":  a.occurrenceFileMaxBytes(),
	})
}

// storeOccurrenceFile validates and stores bytes as a document of the case and records the history.
// It answers 200 with the file; a repeat of the same file by the same person within a few seconds
// returns the one already stored (a double click or a retried request does not duplicate it).
func (a *App) storeOccurrenceFile(r *fastglue.Request, orgID, userID uuid.UUID, occ *models.Occurrence, data []byte, rawName string, restricted bool, origin string) error {
	if len(data) > a.occurrenceFileMaxBytes() {
		return a.sendChatError(r, errOccFileTooLarge, "attach the document")
	}
	info, ierr := inspectOccurrenceFile(data, rawName)
	if ierr != nil {
		return a.sendChatError(r, ierr, "attach the document")
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	var dup models.OccurrenceFile
	if a.DB.Where("occurrence_id = ? AND uploaded_by_id = ? AND sha256 = ? AND created_at > ?",
		occ.ID, userID, hexSum, time.Now().Add(-occurrenceFileDedupeFor)).First(&dup).Error == nil {
		return r.SendEnvelope(map[string]any{"file": a.occurrenceFileResponses(userID, occurrenceAccess{}, []models.OccurrenceFile{dup}, true)[0], "duplicate": true})
	}

	f := models.OccurrenceFile{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, OccurrenceID: occ.ID, UploadedByID: userID,
		Name: sanitizeChatFileName(rawName, info.Ext), Mime: info.Mime, Kind: info.Kind, SizeBytes: int64(len(data)),
		SHA256: hexSum, Restricted: restricted,
	}
	// An unpredictable internal name: nothing of the user's name or the case is in the path.
	f.StoragePath = filepath.Join(occurrenceFilesRoot, orgID.String(), occ.ID.String(), f.ID.String()+info.Ext)
	if werr := a.writeChatFile(f.StoragePath, data); werr != nil {
		a.Log.Error("Failed to store occurrence file", "error", werr)
		return a.sendChatError(r, errOccFileStorage, "attach the document")
	}
	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&f).Error; err != nil {
			return err
		}
		// The file is stored but closed: it is queued for the antimalware scan in the same transaction.
		if err := registerScanTx(tx, orgID, models.ScanSourceOccurrenceFile, f.ID, f.StoragePath, f.SHA256, f.SizeBytes); err != nil {
			return err
		}
		return tx.Create(&models.OccurrenceEvent{
			OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventFileAdded, Content: f.Name,
			Metadata: models.JSONB{"file_id": f.ID.String(), "size_bytes": f.SizeBytes, "mime": f.Mime, "origin": origin}, CreatedByID: &userID,
		}).Error
	})
	if txErr != nil {
		_ = a.removeChatFile(f.StoragePath)
		a.Log.Error("Failed to record occurrence file", "error", txErr)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to attach the document", nil, "")
	}
	a.logAudit(orgID, userID, "occurrence_file", f.ID, models.AuditActionCreated, nil, map[string]any{"name": f.Name, "occurrence_id": occ.ID.String(), "origin": origin})
	a.ScanNow(models.ScanSourceOccurrenceFile, f.ID) // small files are cleared (or blocked) before the answer
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: occurrenceToResponse(*occ)})
	acc := a.authorizeOccurrence(userID, orgID, occ)
	return r.SendEnvelope(map[string]any{"file": a.occurrenceFileResponses(userID, acc, []models.OccurrenceFile{f}, true)[0]})
}

// canAttachToOccurrence: the person works the case, or follows it with the right to comment.
func (a *App) canAttachToOccurrence(orgID, userID uuid.UUID, acc occurrenceAccess) bool {
	return a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID) &&
		(workTheCase(acc) || acc.can(models.OccurrenceCapComment))
}

// UploadOccurrenceFile receives a file from the computer.
func (a *App) UploadOccurrenceFile(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	if !a.canAttachToOccurrence(orgID, userID, acc) {
		return a.sendChatError(r, errOccFileForbid, "attach the document")
	}
	if !a.chatUploadAllowed(userID) {
		return a.sendChatError(r, errChatRateLimited, "attach the document")
	}
	form, ferr := r.RequestCtx.MultipartForm()
	if ferr != nil || len(form.File["file"]) == 0 {
		return a.sendChatError(r, errOccFileInvalid, "attach the document")
	}
	fh := form.File["file"][0]
	if fh.Size > int64(a.occurrenceFileMaxBytes()) {
		return a.sendChatError(r, errOccFileTooLarge, "attach the document")
	}
	f, oerr := fh.Open()
	if oerr != nil {
		return a.sendChatError(r, errOccFileInvalid, "attach the document")
	}
	defer func() { _ = f.Close() }()
	data, rerr := io.ReadAll(io.LimitReader(f, int64(a.occurrenceFileMaxBytes())+1))
	if rerr != nil {
		return a.sendChatError(r, errOccFileInvalid, "attach the document")
	}
	restricted := !(len(form.Value["restricted"]) > 0 && form.Value["restricted"][0] == "false")
	return a.storeOccurrenceFile(r, orgID, userID, occ, data, fh.Filename, restricted, "upload")
}

// AttachNextcloudToOccurrence brings a file of the caller's own Nextcloud into the case. The
// server fetches it with the caller's stored credentials, so no one can pull another person's files.
func (a *App) AttachNextcloudToOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	if !a.canAttachToOccurrence(orgID, userID, acc) {
		return a.sendChatError(r, errOccFileForbid, "attach the document")
	}
	if !a.chatUploadAllowed(userID) {
		return a.sendChatError(r, errChatRateLimited, "attach the document")
	}
	var req struct {
		Path       string `json:"path"`
		Restricted *bool  `json:"restricted"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	p, valid := cleanNextcloudPath(req.Path)
	if !valid || p == "/" {
		return a.sendChatError(r, errNextcloudBadPath, "attach the document")
	}
	data, ferr := a.fetchNextcloudFile(context.Background(), orgID, userID, p, a.occurrenceFileMaxBytes())
	if ferr != nil {
		return a.sendChatError(r, ferr, "attach the document")
	}
	restricted := req.Restricted == nil || *req.Restricted
	return a.storeOccurrenceFile(r, orgID, userID, occ, data, path.Base(p), restricted, "nextcloud")
}

// loadOccurrenceFile resolves :id (case) and :fid (file) and applies the case scope. A file of
// another case, deleted, or in another organization answers like one that does not exist.
func (a *App) loadOccurrenceFile(r *fastglue.Request) (*models.Occurrence, occurrenceAccess, *models.OccurrenceFile, uuid.UUID, uuid.UUID, error) {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil, occurrenceAccess{}, nil, uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil, occurrenceAccess{}, nil, uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	fid, perr := uuid.Parse(r.RequestCtx.UserValue("fid").(string))
	if perr != nil {
		_ = a.sendChatError(r, errOccFileGone, "find the file")
		return nil, occurrenceAccess{}, nil, uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	var f models.OccurrenceFile
	if a.DB.Where("id = ? AND occurrence_id = ? AND organization_id = ?", fid, occ.ID, orgID).First(&f).Error != nil {
		_ = a.sendChatError(r, errOccFileGone, "find the file")
		return nil, occurrenceAccess{}, nil, uuid.Nil, uuid.Nil, errEnvelopeSent
	}
	return occ, acc, &f, orgID, userID, nil
}

// DownloadOccurrenceFile streams a document. ?inline=1 shows PDFs and images in the browser;
// everything else is always a download, with nosniff.
func (a *App) DownloadOccurrenceFile(r *fastglue.Request) error {
	occ, acc, f, orgID, userID, err := a.loadOccurrenceFile(r)
	if err != nil {
		return nil
	}
	if !a.canDownloadOccurrenceFile(userID, acc, f) {
		return a.sendChatError(r, errOccFileForbid, "download the file")
	}
	// Permission first (so nobody learns the scan state of a file they may not see), then the verdict.
	if !a.requireCleanFile(r, orgID, models.ScanSourceOccurrenceFile, f.ID, f.StoragePath) {
		return nil
	}
	full, perr := a.resolveSafeStoragePath(f.StoragePath)
	if perr != nil {
		return a.sendChatError(r, errOccFileGone, "download the file")
	}
	data, rerr := os.ReadFile(full)
	if rerr != nil {
		return a.sendChatError(r, errOccFileGone, "download the file")
	}
	inline := string(r.RequestCtx.QueryArgs().Peek("inline")) == "1" && previewable(f.Mime)
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	h := &r.RequestCtx.Response.Header
	h.Set("Content-Type", f.Mime)
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(f.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	r.RequestCtx.SetBody(data)
	a.logAudit(orgID, userID, "occurrence_file", f.ID, models.AuditActionAccessed, nil, map[string]any{"name": f.Name, "occurrence_id": occ.ID.String(), "inline": inline})
	return nil
}

// DeleteOccurrenceFile removes a document: the sender or a supervisor of the case.
func (a *App) DeleteOccurrenceFile(r *fastglue.Request) error {
	occ, acc, f, orgID, userID, err := a.loadOccurrenceFile(r)
	if err != nil {
		return nil
	}
	if !a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID) || !a.canDeleteOccurrenceFile(userID, acc, f) {
		return a.sendChatError(r, errOccFileForbid, "remove the file")
	}
	if a.occurrenceHeld(orgID, occ) {
		return r.SendErrorEnvelope(fasthttp.StatusLocked, "This protocol is under a legal hold: its files cannot be removed", nil, "")
	}
	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", f.ID).Delete(&models.OccurrenceFile{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errOccFileGone // removed a moment ago by someone else
		}
		return tx.Create(&models.OccurrenceEvent{
			OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventFileRemoved, Content: f.Name,
			Metadata: models.JSONB{"file_id": f.ID.String()}, CreatedByID: &userID,
		}).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "remove the file")
	}
	_ = a.removeChatFile(f.StoragePath)
	a.logAudit(orgID, userID, "occurrence_file", f.ID, models.AuditActionDeleted, map[string]any{"name": f.Name, "occurrence_id": occ.ID.String()}, nil)
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: occurrenceToResponse(*occ)})
	return r.SendEnvelope(map[string]any{"deleted": true})
}
