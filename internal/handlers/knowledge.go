package handlers

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// manualSourceTypes are the types the API accepts. manual_html only comes from
// the import CLI.
var apiKnowledgeSources = map[string]bool{
	models.KnowledgeSourceFAQ: true, models.KnowledgeSourceArticle: true,
	models.KnowledgeSourceProcess: true, models.KnowledgeSourceText: true,
	models.KnowledgeSourceMarkdown: true,
}

const maxKnowledgeBody = 200_000 // characters

// KnowledgeDocumentRequest is the create/update body. PUT replaces the scope:
// leaving unit_id/department_id out makes the document global to the organization.
type KnowledgeDocumentRequest struct {
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	SourceType   string     `json:"source_type"`
	UnitID       *uuid.UUID `json:"unit_id"`
	DepartmentID *uuid.UUID `json:"department_id"`
	Status       string     `json:"status"`
}

// knowledgeReach resolves WHO is asking: the unit/department context used to
// filter content, and whether the caller may choose it freely.
//   - Global reach (knowledge:write or conversations:view_all) may pass
//     unit_id/department_id; no parameter means no unit/department (global content only).
//   - Everyone else uses their own unit/department; asking for another is a 403,
//     never silently ignored.
//
// Returns ok=false after sending the error response.
func (a *App) knowledgeReach(r *fastglue.Request, orgID, userID uuid.UUID) (unit, dept *uuid.UUID, global, ok bool) {
	global = a.HasPermission(userID, models.ResourceKnowledge, models.ActionWrite, orgID) ||
		a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID)

	reqUnit, err1 := optionalUUIDQuery(r, "unit_id")
	reqDept, err2 := optionalUUIDQuery(r, "department_id")
	if err1 != nil || err2 != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id or department_id", nil, "")
		return nil, nil, false, false
	}

	if global {
		if !a.scopeTargetsExist(orgID, reqUnit, reqDept) {
			_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown unit or department", nil, "")
			return nil, nil, false, false
		}
		return reqUnit, reqDept, true, true
	}

	var u models.User
	if err := a.DB.Select("id", "unit_id", "department_id").First(&u, "id = ?", userID).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load user", nil, "")
		return nil, nil, false, false
	}
	if (reqUnit != nil && !sameUUID(reqUnit, u.UnitID)) || (reqDept != nil && !sameUUID(reqDept, u.DepartmentID)) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions for this unit or department", nil, "")
		return nil, nil, false, false
	}
	return u.UnitID, u.DepartmentID, false, true
}

func sameUUID(a, b *uuid.UUID) bool { return a != nil && b != nil && *a == *b }

func optionalUUIDQuery(r *fastglue.Request, key string) (*uuid.UUID, error) {
	raw := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek(key)))
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// scopeTargetsExist checks a unit/department belongs to the organization.
func (a *App) scopeTargetsExist(orgID uuid.UUID, unit, dept *uuid.UUID) bool {
	var n int64
	if unit != nil {
		if a.DB.Model(&models.Unit{}).Where("id = ? AND organization_id = ?", *unit, orgID).Count(&n); n == 0 {
			return false
		}
	}
	if dept != nil {
		if a.DB.Model(&models.Department{}).Where("id = ? AND organization_id = ?", *dept, orgID).Count(&n); n == 0 {
			return false
		}
	}
	return true
}

// scopeEligible restricts a documents query to what a context may see (the same
// predicate the retriever applies to chunks).
func scopeEligible(q *gorm.DB, unit, dept *uuid.UUID) *gorm.DB {
	return q.Where("(unit_id IS NULL OR unit_id = ?) AND (department_id IS NULL OR department_id = ?)", unit, dept)
}

// SearchKnowledge GET /api/knowledge/search?q=&unit_id=&department_id=&limit=
func (a *App) SearchKnowledge(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionRead)
	if err != nil {
		return nil
	}
	unit, dept, _, ok := a.knowledgeReach(r, orgID, userID)
	if !ok {
		return nil
	}

	q := string(r.RequestCtx.QueryArgs().Peek("q"))
	limit := r.RequestCtx.QueryArgs().GetUintOrZero("limit")
	svc := knowledge.Service{Retriever: knowledge.LexicalRetriever{DB: a.DB}}
	hits, err := svc.Search(context.Background(), knowledge.Query{
		OrgID: orgID, Text: q, UnitID: unit, DepartmentID: dept, Limit: limit,
	})
	if err == knowledge.ErrEmptyQuery {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "q is required", nil, "")
	}
	if err != nil {
		a.Log.Error("Knowledge search failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Search failed", nil, "")
	}
	return r.SendEnvelope(map[string]any{"results": hits})
}

// ListKnowledgeDocuments GET /api/knowledge/documents
func (a *App) ListKnowledgeDocuments(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionRead)
	if err != nil {
		return nil
	}
	unit, dept, global, ok := a.knowledgeReach(r, orgID, userID)
	if !ok {
		return nil
	}
	pg := parsePagination(r)
	args := r.RequestCtx.QueryArgs()

	q := a.DB.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", orgID)
	if !global {
		q = scopeEligible(q, unit, dept).Where("status = ?", models.KnowledgeStatusActive)
	} else {
		// Administrators see every document; unit/department narrow the list only when given.
		if unit != nil {
			q = q.Where("unit_id = ?", unit)
		}
		if dept != nil {
			q = q.Where("department_id = ?", dept)
		}
		if st := string(args.Peek("status")); st != "" {
			q = q.Where("status = ?", st)
		}
	}
	if st := string(args.Peek("source_type")); st != "" {
		q = q.Where("source_type = ?", st)
	}
	if s := strings.TrimSpace(string(args.Peek("search"))); s != "" {
		q = q.Where("title ILIKE ?", "%"+s+"%")
	}

	var total int64
	q.Count(&total)
	var docs []models.KnowledgeDocument
	if err := pg.Apply(q.Omit("body").Order("title ASC")).Find(&docs).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load documents", nil, "")
	}
	return r.SendEnvelope(listEnvelope("documents", docs, total, pg))
}

// GetKnowledgeDocument GET /api/knowledge/documents/{id}. A document outside the
// caller's reach is a 404, like one from another organization.
func (a *App) GetKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionRead)
	if err != nil {
		return nil
	}
	unit, dept, global, ok := a.knowledgeReach(r, orgID, userID)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	q := a.DB.Where("id = ? AND organization_id = ?", id, orgID)
	if !global {
		q = scopeEligible(q, unit, dept).Where("status = ?", models.KnowledgeStatusActive)
	}
	var doc models.KnowledgeDocument
	if err := q.First(&doc).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Document not found", nil, "")
	}
	return r.SendEnvelope(doc)
}

// validateKnowledgeRequest checks the body and returns a bad-request message, or "".
func (a *App) validateKnowledgeRequest(orgID uuid.UUID, req *KnowledgeDocumentRequest, requireSource bool) string {
	req.Title = strings.TrimSpace(req.Title)
	switch {
	case req.Title == "":
		return "title is required"
	case len([]rune(req.Title)) > 500:
		return "title is too long"
	case strings.TrimSpace(req.Body) == "":
		return "body is required"
	case len([]rune(req.Body)) > maxKnowledgeBody:
		return "body is too long"
	case (requireSource || req.SourceType != "") && !apiKnowledgeSources[req.SourceType]:
		return "invalid source_type"
	case req.Status != "" && req.Status != models.KnowledgeStatusActive && req.Status != models.KnowledgeStatusArchived:
		return "invalid status"
	case !a.scopeTargetsExist(orgID, req.UnitID, req.DepartmentID):
		return "unknown unit or department"
	}
	return ""
}

// knowledgeAuditView is what the audit log compares. Plain strings, never
// omitted: clearing a unit scope must show up as a change (audit.LogAudit only
// compares the keys of the new state). The body is represented by its hash.
type knowledgeAuditView struct {
	Title        string `json:"title"`
	SourceType   string `json:"source_type"`
	UnitID       string `json:"unit_id"`
	DepartmentID string `json:"department_id"`
	Visibility   string `json:"visibility"`
	Status       string `json:"status"`
	ContentHash  string `json:"content_hash"`
}

func auditViewOf(d *models.KnowledgeDocument) knowledgeAuditView {
	v := knowledgeAuditView{Title: d.Title, SourceType: d.SourceType, Visibility: d.Visibility, Status: d.Status, ContentHash: d.ContentHash}
	if d.UnitID != nil {
		v.UnitID = d.UnitID.String()
	}
	if d.DepartmentID != nil {
		v.DepartmentID = d.DepartmentID.String()
	}
	return v
}

// CreateKnowledgeDocument POST /api/knowledge/documents
func (a *App) CreateKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req KnowledgeDocumentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if msg := a.validateKnowledgeRequest(orgID, &req, true); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	doc := models.KnowledgeDocument{
		OrganizationID: orgID, UnitID: req.UnitID, DepartmentID: req.DepartmentID,
		SourceType: req.SourceType, Title: req.Title, Body: req.Body,
		Status: req.Status, CreatedByID: &userID, UpdatedByID: &userID,
	}
	if err := knowledge.Save(a.DB, &doc, knowledge.SectionsFor(doc.SourceType, doc.Title, req.Body)); err != nil {
		a.Log.Error("Failed to create knowledge document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create document", nil, "")
	}
	after := auditViewOf(&doc)
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionCreated, nil, &after)
	return r.SendEnvelope(doc)
}

// UpdateKnowledgeDocument PUT /api/knowledge/documents/{id}. Replaces title,
// body and scope; source_type and status are kept when omitted. Archiving is
// status=archived.
func (a *App) UpdateKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	doc, err := findByIDAndOrg[models.KnowledgeDocument](a.DB, r, id, orgID, "Document")
	if err != nil {
		return nil
	}
	var req KnowledgeDocumentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if msg := a.validateKnowledgeRequest(orgID, &req, false); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	before := auditViewOf(doc)
	doc.Title, doc.Body = req.Title, req.Body
	doc.UnitID, doc.DepartmentID = req.UnitID, req.DepartmentID
	doc.UpdatedByID = &userID
	if req.SourceType != "" {
		doc.SourceType = req.SourceType
	}
	if req.Status != "" {
		doc.Status = req.Status
	}
	if err := knowledge.Save(a.DB, doc, knowledge.SectionsFor(doc.SourceType, doc.Title, req.Body)); err != nil {
		a.Log.Error("Failed to update knowledge document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update document", nil, "")
	}
	after := auditViewOf(doc)
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionUpdated, before, &after)
	return r.SendEnvelope(doc)
}

// DeleteKnowledgeDocument DELETE /api/knowledge/documents/{id}: soft delete,
// the chunks are dropped (the text is gone from search immediately).
func (a *App) DeleteKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	doc, err := findByIDAndOrg[models.KnowledgeDocument](a.DB, r, id, orgID, "Document")
	if err != nil {
		return nil
	}
	before := auditViewOf(doc)
	if err := knowledge.Remove(a.DB, doc); err != nil {
		a.Log.Error("Failed to delete knowledge document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete document", nil, "")
	}
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionDeleted, before, nil)
	return r.SendEnvelope(map[string]any{"message": "Document deleted"})
}
