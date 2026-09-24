package handlers

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// OccurrenceDocumentItem is one product line of a purchase document.
type OccurrenceDocumentItem struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"` // free text: "12 m²", "3 cx"
}

// OccurrenceDocumentRequest is one NF or cupom/pedido sent by the agent.
type OccurrenceDocumentRequest struct {
	Type         string                   `json:"type"`
	Number       string                   `json:"number"`
	PurchaseDate string                   `json:"purchase_date"` // "2006-01-02", optional
	Items        []OccurrenceDocumentItem `json:"items"`
}

const (
	maxOccurrenceDocuments      = 20
	maxOccurrenceDocumentItems  = 100
	maxOccurrenceAttachmentSize = 10 << 20 // 10MB
)

// occurrenceAttachmentMIME lists what a customer typically sends for an NF or
// cupom: the PDF, or a photo of it.
var occurrenceAttachmentMIME = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
}

// buildOccurrenceDocuments validates the request documents and turns them into
// rows (without OccurrenceID). Product lines without a description are dropped.
func buildOccurrenceDocuments(orgID, userID uuid.UUID, loc *time.Location, reqs []OccurrenceDocumentRequest) ([]models.OccurrenceDocument, error) {
	if len(reqs) > maxOccurrenceDocuments {
		return nil, errors.New("too many documents")
	}
	docs := make([]models.OccurrenceDocument, 0, len(reqs))
	for _, req := range reqs {
		doc, err := buildOccurrenceDocument(orgID, userID, loc, req)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func buildOccurrenceDocument(orgID, userID uuid.UUID, loc *time.Location, req OccurrenceDocumentRequest) (models.OccurrenceDocument, error) {
	if req.Type != models.OccurrenceDocumentNF && req.Type != models.OccurrenceDocumentCupom {
		return models.OccurrenceDocument{}, errors.New("document type must be nf or cupom")
	}
	// The number is optional, as the old single NF field was: sometimes all the
	// customer has is the product or a photo of the receipt.
	number := strings.TrimSpace(req.Number)
	if len(number) > 60 {
		return models.OccurrenceDocument{}, errors.New("document number is too long")
	}
	var purchaseDate *time.Time
	if req.PurchaseDate != "" {
		d, err := time.ParseInLocation("2006-01-02", req.PurchaseDate, loc)
		if err != nil {
			return models.OccurrenceDocument{}, errors.New("invalid purchase_date")
		}
		purchaseDate = &d
	}
	if len(req.Items) > maxOccurrenceDocumentItems {
		return models.OccurrenceDocument{}, errors.New("too many products")
	}
	items := models.JSONBArray{}
	for _, it := range req.Items {
		desc := strings.TrimSpace(it.Description)
		if desc == "" {
			continue
		}
		items = append(items, map[string]any{
			"code":        strings.TrimSpace(it.Code),
			"description": desc,
			"quantity":    strings.TrimSpace(it.Quantity),
		})
	}
	return models.OccurrenceDocument{
		OrganizationID: orgID,
		Type:           req.Type,
		Number:         number,
		PurchaseDate:   purchaseDate,
		Items:          items,
		CreatedByID:    &userID,
	}, nil
}

// summarizeOccurrenceDocuments condenses documents into the occurrence's legacy
// single-value fields, which message templates ([NF]), process required-field
// rules and reports still read.
func summarizeOccurrenceDocuments(docs []models.OccurrenceDocument) (invoice string, purchaseDate *time.Time, product string) {
	var numbers, products []string
	for _, d := range docs {
		if d.Number != "" {
			numbers = append(numbers, d.Number)
		}
		if d.PurchaseDate != nil && (purchaseDate == nil || d.PurchaseDate.Before(*purchaseDate)) {
			purchaseDate = d.PurchaseDate
		}
		products = append(products, documentProducts(d)...)
	}
	return truncateRunes(strings.Join(numbers, summaryNumberSep), 255), purchaseDate, truncateRunes(strings.Join(products, summaryProductSep), 255)
}

const (
	summaryNumberSep  = ", "
	summaryProductSep = "; "
)

func documentProducts(d models.OccurrenceDocument) []string {
	var out []string
	for _, it := range d.Items {
		if m, ok := it.(map[string]any); ok {
			if desc, _ := m["description"].(string); desc != "" {
				out = append(out, desc)
			}
		}
	}
	return out
}

// updateSummaryForDocument keeps the protocol's single-value fields in step
// when a document is added or removed after the protocol was opened. It edits
// the lists rather than rebuilding them from the documents, so values typed in
// those fields before documents existed are not thrown away.
func updateSummaryForDocument(occ *models.Occurrence, doc models.OccurrenceDocument, added bool, remaining []models.OccurrenceDocument) map[string]any {
	numbers := splitSummary(occ.InvoiceNumber, summaryNumberSep)
	products := splitSummary(occ.ProductDescription, summaryProductSep)
	purchaseDate := occ.PurchaseDate
	if added {
		if doc.Number != "" && !containsString(numbers, doc.Number) {
			numbers = append(numbers, doc.Number)
		}
		products = append(products, documentProducts(doc)...)
		if doc.PurchaseDate != nil && (purchaseDate == nil || doc.PurchaseDate.Before(*purchaseDate)) {
			purchaseDate = doc.PurchaseDate
		}
	} else {
		numbers = removeOnce(numbers, doc.Number)
		for _, p := range documentProducts(doc) {
			products = removeOnce(products, p)
		}
		if doc.PurchaseDate != nil && purchaseDate != nil && purchaseDate.Equal(*doc.PurchaseDate) {
			_, purchaseDate, _ = summarizeOccurrenceDocuments(remaining)
		}
	}
	return map[string]any{
		"invoice_number":      truncateRunes(strings.Join(numbers, summaryNumberSep), 255),
		"product_description": truncateRunes(strings.Join(products, summaryProductSep), 255),
		"purchase_date":       purchaseDate,
	}
}

func splitSummary(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, sep)
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func removeOnce(list []string, v string) []string {
	for i, x := range list {
		if x == v {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ListOccurrenceDocuments returns an occurrence's purchase documents.
func (a *App) ListOccurrenceDocuments(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var docs []models.OccurrenceDocument
	if err := a.DB.Where("occurrence_id = ? AND organization_id = ?", occ.ID, orgID).
		Order("created_at ASC").Find(&docs).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load documents", nil, "")
	}
	return r.SendEnvelope(map[string]any{"documents": docs})
}

// CreateOccurrenceDocument adds a purchase document to an existing occurrence.
func (a *App) CreateOccurrenceDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	var req OccurrenceDocumentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	doc, err := buildOccurrenceDocument(orgID, userID, a.orgLocation(orgID), req)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	var count int64
	a.DB.Model(&models.OccurrenceDocument{}).Where("occurrence_id = ?", occ.ID).Count(&count)
	if count >= maxOccurrenceDocuments {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "too many documents", nil, "")
	}
	doc.OccurrenceID = occ.ID
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&doc).Error; err != nil {
			return err
		}
		return tx.Model(&models.Occurrence{}).Where("id = ?", occ.ID).
			Updates(updateSummaryForDocument(occ, doc, true, nil)).Error
	})
	if err != nil {
		a.Log.Error("Failed to create occurrence document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save document", nil, "")
	}
	return r.SendEnvelope(doc)
}

// loadOccurrenceDocument resolves {docId} inside an occurrence the user may access.
func (a *App) loadOccurrenceDocument(r *fastglue.Request, orgID, userID uuid.UUID, needInteract bool) (*models.OccurrenceDocument, error) {
	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, needInteract)
	if err != nil {
		return nil, err
	}
	docID, err := parsePathUUID(r, "docId", "document")
	if err != nil {
		return nil, errEnvelopeSent
	}
	var doc models.OccurrenceDocument
	if err := a.DB.Where("id = ? AND occurrence_id = ? AND organization_id = ?", docID, occ.ID, orgID).
		First(&doc).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Document not found", nil, "")
		return nil, errEnvelopeSent
	}
	return &doc, nil
}

// DeleteOccurrenceDocument removes a document (and its file) typed by mistake.
func (a *App) DeleteOccurrenceDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	doc, err := a.loadOccurrenceDocument(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	var occ models.Occurrence
	if err := a.DB.First(&occ, "id = ?", doc.OccurrenceID).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Occurrence not found", nil, "")
	}
	// Hard delete: the file is removed too, so a soft-deleted row would only
	// point at nothing.
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(doc).Error; err != nil {
			return err
		}
		var remaining []models.OccurrenceDocument
		if err := tx.Where("occurrence_id = ?", doc.OccurrenceID).Find(&remaining).Error; err != nil {
			return err
		}
		return tx.Model(&models.Occurrence{}).Where("id = ?", doc.OccurrenceID).
			Updates(updateSummaryForDocument(&occ, *doc, false, remaining)).Error
	})
	if err != nil {
		a.Log.Error("Failed to delete occurrence document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete document", nil, "")
	}
	a.removeOccurrenceAttachment(doc.AttachmentPath)
	return r.SendEnvelope(map[string]any{"deleted": true})
}

// UploadOccurrenceDocumentAttachment stores the NF/cupom file (PDF or photo).
// A new upload replaces the previous file.
func (a *App) UploadOccurrenceDocumentAttachment(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	doc, err := a.loadOccurrenceDocument(r, orgID, userID, true)
	if err != nil {
		return nil
	}

	form, err := r.RequestCtx.MultipartForm()
	if err != nil || len(form.File["file"]) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No file provided", nil, "")
	}
	fileHeader := form.File["file"][0]
	file, err := fileHeader.Open()
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Failed to open file", nil, "")
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxOccurrenceAttachmentSize+1))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}
	if len(data) > maxOccurrenceAttachmentSize {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "File too large. Maximum size is 10MB", nil, "")
	}
	// Trust the bytes, not the browser's label.
	mimeType := strings.SplitN(http.DetectContentType(data), ";", 2)[0]
	if !occurrenceAttachmentMIME[mimeType] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only PDF or image files are allowed", nil, "")
	}

	subdir := "occurrence-documents"
	if err := a.ensureMediaDir(subdir); err != nil {
		a.Log.Error("Failed to create attachment directory", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}
	relPath := filepath.Join(subdir, doc.ID.String()+getExtensionFromMimeType(mimeType))
	if err := os.WriteFile(filepath.Join(a.getMediaStoragePath(), relPath), data, 0644); err != nil {
		a.Log.Error("Failed to save attachment", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}
	if doc.AttachmentPath != "" && doc.AttachmentPath != relPath {
		a.removeOccurrenceAttachment(doc.AttachmentPath)
	}

	name := filepath.Base(fileHeader.Filename)
	if err := a.DB.Model(doc).Updates(map[string]any{
		"attachment_path": relPath, "attachment_name": truncateRunes(name, 255), "attachment_mime": mimeType,
	}).Error; err != nil {
		a.removeOccurrenceAttachment(relPath)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}
	return r.SendEnvelope(doc)
}

// ServeOccurrenceDocumentAttachment streams the stored file back.
func (a *App) ServeOccurrenceDocumentAttachment(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	doc, err := a.loadOccurrenceDocument(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	if doc.AttachmentPath == "" {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No attachment", nil, "")
	}
	fullPath, err := a.resolveSafeStoragePath(doc.AttachmentPath)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Attachment not found", nil, "")
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Attachment not found", nil, "")
	}
	r.RequestCtx.Response.Header.Set("Content-Type", doc.AttachmentMime)
	r.RequestCtx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	r.RequestCtx.Response.Header.Set("Cache-Control", "private, max-age=3600")
	r.RequestCtx.SetBody(data)
	return nil
}

func (a *App) removeOccurrenceAttachment(relPath string) {
	if relPath == "" {
		return
	}
	if fullPath, err := a.resolveSafeStoragePath(relPath); err == nil {
		_ = os.Remove(fullPath)
	}
}

// deleteOccurrenceDocuments removes every document of an occurrence inside tx
// and returns their file paths, to be removed once the transaction commits.
func deleteOccurrenceDocuments(tx *gorm.DB, occurrenceID uuid.UUID) ([]string, error) {
	var paths []string
	if err := tx.Model(&models.OccurrenceDocument{}).Unscoped().
		Where("occurrence_id = ? AND attachment_path <> ''", occurrenceID).
		Pluck("attachment_path", &paths).Error; err != nil {
		return nil, err
	}
	return paths, tx.Unscoped().Where("occurrence_id = ?", occurrenceID).Delete(&models.OccurrenceDocument{}).Error
}
