package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// apiKnowledgeSources are the types the API accepts on create. manual_html only
// comes from the import CLI.
var apiKnowledgeSources = map[string]bool{
	models.KnowledgeSourceFAQ: true, models.KnowledgeSourceArticle: true,
	models.KnowledgeSourceProcess: true, models.KnowledgeSourceText: true,
	models.KnowledgeSourceMarkdown: true,
}

const maxKnowledgeBody = 200_000 // characters

// ScopeID is a unit/department id that remembers whether its key was present in
// the JSON ("unit_id": null is explicit; an absent key is not), so PUT can demand
// the scope be stated instead of turning a partial body into "global".
type ScopeID struct {
	Set bool
	ID  *uuid.UUID
}

func (s *ScopeID) UnmarshalJSON(b []byte) error {
	s.Set = true
	if string(b) == "null" {
		s.ID = nil
		return nil
	}
	var id uuid.UUID
	if err := json.Unmarshal(b, &id); err != nil {
		return err
	}
	s.ID = &id
	return nil
}

// KnowledgeDocumentRequest is the create/update body.
//
// PUT is the COMPLETE representation of title, body and scope (there is no PATCH
// in 8A/8B): unit_id and department_id MUST be present, each a UUID or an explicit
// null (= no restriction). Omitting either is a 400, never an implicit "global".
// POST treats an omitted scope key as null.
//
// ExpectedUpdatedAt (optional, PUT): the updated_at the client last read; if the
// document changed since, the answer is 409. Absent = last writer wins.
//
// For a manual_html document (content owned by the importer) title, body and
// source_type are optional on PUT and, when sent, must equal what is stored (409
// otherwise); only scope and status change.
type KnowledgeDocumentRequest struct {
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	SourceType        string     `json:"source_type"`
	UnitID            ScopeID    `json:"unit_id"`
	DepartmentID      ScopeID    `json:"department_id"`
	Status            string     `json:"status"`
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
}

// knowledgeAccess is who is asking and with which unit/department context.
//
// Three capabilities, deliberately separate (review M3):
//   - read   (knowledge:read): ACTIVE documents eligible for the user's OWN
//     unit/department; never chooses a context;
//   - choose (conversations:view_all, with knowledge:read): may state unit_id /
//     department_id; still only ACTIVE documents, evaluated against that context;
//   - admin  (knowledge:write): everything, archived and every scope included.
type knowledgeAccess struct {
	Unit, Dept *uuid.UUID
	Admin      bool
	Chooses    bool
}

// knowledgeCapabilities is the single place that maps permissions to capabilities.
func (a *App) knowledgeCapabilities(userID, orgID uuid.UUID) (admin, chooses bool) {
	admin = a.HasPermission(userID, models.ResourceKnowledge, models.ActionWrite, orgID)
	chooses = admin || a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID)
	return admin, chooses
}

// restrict limits a documents query to what this access may read.
func (k knowledgeAccess) restrict(q *gorm.DB) *gorm.DB {
	if k.Admin {
		return q
	}
	return scopeEligible(q, k.Unit, k.Dept).Where("status = ?", models.KnowledgeStatusActive)
}

// resolveKnowledgeAccess resolves the context from the optional unit_id /
// department_id query parameters:
//   - who may choose: the parameters are the context (no parameter = no
//     unit/department, global content only); they must belong to the organization (400);
//   - everyone else: their own unit/department; asking for another is a 403, never
//     silently ignored.
//
// ok=false after the error response was sent.
func (a *App) resolveKnowledgeAccess(r *fastglue.Request, orgID, userID uuid.UUID) (acc knowledgeAccess, ok bool) {
	acc.Admin, acc.Chooses = a.knowledgeCapabilities(userID, orgID)

	reqUnit, err1 := optionalUUIDQuery(r, "unit_id")
	reqDept, err2 := optionalUUIDQuery(r, "department_id")
	if err1 != nil || err2 != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id or department_id", nil, "")
		return acc, false
	}

	if acc.Chooses {
		if !a.scopeTargetsExist(orgID, reqUnit, reqDept) {
			_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown unit or department", nil, "")
			return acc, false
		}
		acc.Unit, acc.Dept = reqUnit, reqDept
		return acc, true
	}

	var u models.User
	if err := a.DB.Select("id", "unit_id", "department_id").First(&u, "id = ?", userID).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load user", nil, "")
		return acc, false
	}
	if (reqUnit != nil && !sameUUID(reqUnit, u.UnitID)) || (reqDept != nil && !sameUUID(reqDept, u.DepartmentID)) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions for this unit or department", nil, "")
		return acc, false
	}
	acc.Unit, acc.Dept = u.UnitID, u.DepartmentID
	return acc, true
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
	acc, ok := a.resolveKnowledgeAccess(r, orgID, userID)
	if !ok {
		return nil
	}

	q := string(r.RequestCtx.QueryArgs().Peek("q"))
	limit := r.RequestCtx.QueryArgs().GetUintOrZero("limit")
	svc := knowledge.Service{Retriever: knowledge.LexicalRetriever{DB: a.DB}}
	hits, err := svc.Search(context.Background(), knowledge.Query{
		OrgID: orgID, Text: q, UnitID: acc.Unit, DepartmentID: acc.Dept, Limit: limit,
	})
	if errors.Is(err, knowledge.ErrEmptyQuery) {
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
	acc, ok := a.resolveKnowledgeAccess(r, orgID, userID)
	if !ok {
		return nil
	}
	pg := parsePagination(r)
	args := r.RequestCtx.QueryArgs()

	q := a.DB.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", orgID)
	if acc.Admin {
		// Administrators list every document; unit/department narrow the list only when given.
		if acc.Unit != nil {
			q = q.Where("unit_id = ?", acc.Unit)
		}
		if acc.Dept != nil {
			q = q.Where("department_id = ?", acc.Dept)
		}
		if st := string(args.Peek("status")); st != "" {
			q = q.Where("status = ?", st)
		}
	} else {
		q = acc.restrict(q)
	}
	if st := string(args.Peek("source_type")); st != "" {
		q = q.Where("source_type = ?", st)
	}
	if s := strings.TrimSpace(string(args.Peek("search"))); s != "" {
		q = q.Where("title ILIKE ?", "%"+s+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load documents", nil, "")
	}
	var docs []models.KnowledgeDocument
	if err := pg.Apply(q.Omit("body").Order("title ASC")).Find(&docs).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load documents", nil, "")
	}
	return r.SendEnvelope(listEnvelope("documents", docs, total, pg))
}

// findReadableKnowledgeDocument loads a document the caller may read, or sends a
// 404 (outside the reach is indistinguishable from missing).
func (a *App) findReadableKnowledgeDocument(r *fastglue.Request, resource string) (*models.KnowledgeDocument, knowledgeAccess, bool) {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionRead)
	if err != nil {
		return nil, knowledgeAccess{}, false
	}
	acc, ok := a.resolveKnowledgeAccess(r, orgID, userID)
	if !ok {
		return nil, acc, false
	}
	id, err := parsePathUUID(r, "id", resource)
	if err != nil {
		return nil, acc, false
	}
	var doc models.KnowledgeDocument
	if err := acc.restrict(a.DB.Where("id = ? AND organization_id = ?", id, orgID)).First(&doc).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Document not found", nil, "")
		return nil, acc, false
	}
	return &doc, acc, true
}

// GetKnowledgeDocument GET /api/knowledge/documents/{id}
func (a *App) GetKnowledgeDocument(r *fastglue.Request) error {
	doc, _, ok := a.findReadableKnowledgeDocument(r, "document")
	if !ok {
		return nil
	}
	return r.SendEnvelope(doc)
}

// KnowledgeChunkView is a chunk as the management API shows it.
type KnowledgeChunkView struct {
	ID           uuid.UUID `json:"id"`
	ChunkIndex   int       `json:"chunk_index"`
	Heading      string    `json:"heading"`
	Content      string    `json:"content"`
	CharCount    int       `json:"char_count"`
	IndexVersion int       `json:"index_version"`
}

// ListKnowledgeChunks GET /api/knowledge/documents/{id}/chunks: the chunks the
// document is searched by, under the same reach rules as GET /documents/{id}.
func (a *App) ListKnowledgeChunks(r *fastglue.Request) error {
	doc, _, ok := a.findReadableKnowledgeDocument(r, "document")
	if !ok {
		return nil
	}
	var chunks []models.KnowledgeChunk
	if err := a.DB.Where("document_id = ?", doc.ID).Order("chunk_index").Find(&chunks).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load chunks", nil, "")
	}
	out := make([]KnowledgeChunkView, len(chunks))
	for i, c := range chunks {
		out[i] = KnowledgeChunkView{c.ID, c.ChunkIndex, c.Heading, c.Content, utf8.RuneCountInString(c.Content), c.IndexVersion}
	}
	return r.SendEnvelope(map[string]any{"chunks": out, "total": len(out)})
}

// apiError carries an HTTP status out of a transaction.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(msg string) error { return &apiError{fasthttp.StatusBadRequest, msg} }
func conflict(msg string) error   { return &apiError{fasthttp.StatusConflict, msg} }

// sendKnowledgeError turns an error from a document transaction into a response.
func (a *App) sendKnowledgeError(r *fastglue.Request, err error, what string) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return r.SendErrorEnvelope(ae.status, ae.msg, nil, "")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Document not found", nil, "")
	case isUniqueViolation(err):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The document was changed concurrently, retry", nil, "")
	}
	a.Log.Error("Knowledge document operation failed", "op", what, "error", err)
	return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to "+what, nil, "")
}

// validateKnowledgeRequest checks the body of a create or of a PUT on a document
// the API owns, and returns a bad-request message, or "".
func (a *App) validateKnowledgeRequest(orgID uuid.UUID, req *KnowledgeDocumentRequest, isCreate bool) string {
	req.Title = strings.TrimSpace(req.Title)
	switch {
	case !isCreate && (!req.UnitID.Set || !req.DepartmentID.Set):
		return "unit_id and department_id must be present on update (a UUID or null); PUT replaces the whole scope"
	case req.Title == "":
		return "title is required"
	case len([]rune(req.Title)) > 500:
		return "title is too long"
	case strings.TrimSpace(req.Body) == "":
		return "body is required"
	case len([]rune(req.Body)) > maxKnowledgeBody:
		return "body is too long"
	case (isCreate || req.SourceType != "") && !apiKnowledgeSources[req.SourceType]:
		return "invalid source_type"
	case req.Status != "" && req.Status != models.KnowledgeStatusActive && req.Status != models.KnowledgeStatusArchived:
		return "invalid status"
	case !a.scopeTargetsExist(orgID, req.UnitID.ID, req.DepartmentID.ID):
		return "unknown unit or department"
	}
	return ""
}

// validateManualUpdate is the PUT contract of a manual_html document: scope and
// status only. Title/body/source_type are optional but must equal what is stored.
func (a *App) validateManualUpdate(orgID uuid.UUID, req *KnowledgeDocumentRequest, doc *models.KnowledgeDocument) error {
	switch {
	case !req.UnitID.Set || !req.DepartmentID.Set:
		return badRequest("unit_id and department_id must be present on update (a UUID or null); PUT replaces the whole scope")
	case req.Status != "" && req.Status != models.KnowledgeStatusActive && req.Status != models.KnowledgeStatusArchived:
		return badRequest("invalid status")
	case !a.scopeTargetsExist(orgID, req.UnitID.ID, req.DepartmentID.ID):
		return badRequest("unknown unit or department")
	}
	if (req.Title != "" && strings.TrimSpace(req.Title) != doc.Title) ||
		(req.Body != "" && knowledge.NormalizeText(req.Body) != doc.Body) ||
		(req.SourceType != "" && req.SourceType != doc.SourceType) {
		return conflict("manual_html content is managed by the importer: change the HTML and import again; only scope and status can be edited here")
	}
	return nil
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
	ArchivedBy   string `json:"archived_by"`
	ContentHash  string `json:"content_hash"`
}

func auditViewOf(d *models.KnowledgeDocument) knowledgeAuditView {
	v := knowledgeAuditView{Title: d.Title, SourceType: d.SourceType, Visibility: d.Visibility,
		Status: d.Status, ArchivedBy: d.ArchivedBy, ContentHash: d.ContentHash}
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
		OrganizationID: orgID, UnitID: req.UnitID.ID, DepartmentID: req.DepartmentID.ID,
		SourceType: req.SourceType, Title: req.Title, Body: req.Body,
		CreatedByID: &userID, UpdatedByID: &userID,
	}
	knowledge.SetArchivedState(&doc, req.Status, models.KnowledgeArchivedByUser)
	if err := knowledge.Save(a.DB, &doc, knowledge.SectionsFor(doc.SourceType, doc.Title, req.Body)); err != nil {
		a.Log.Error("Failed to create knowledge document", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create document", nil, "")
	}
	after := auditViewOf(&doc)
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionCreated, nil, &after)
	return r.SendEnvelope(doc)
}

// UpdateKnowledgeDocument PUT /api/knowledge/documents/{id}. Everything happens
// in one transaction on the row locked FOR UPDATE, so concurrent edits are
// serialized (no duplicated or missing chunks). See KnowledgeDocumentRequest for the contract.
func (a *App) UpdateKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	var req KnowledgeDocumentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	var doc *models.KnowledgeDocument
	var before knowledgeAuditView
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if doc, err = knowledge.Lock(tx, orgID, id); err != nil {
			return err
		}
		if req.ExpectedUpdatedAt != nil &&
			!req.ExpectedUpdatedAt.Truncate(time.Microsecond).Equal(doc.UpdatedAt.Truncate(time.Microsecond)) {
			return conflict("The document was changed by someone else; reload it before saving")
		}
		manual := doc.SourceType == models.KnowledgeSourceManualHTML
		if manual {
			if err := a.validateManualUpdate(orgID, &req, doc); err != nil {
				return err
			}
		} else if msg := a.validateKnowledgeRequest(orgID, &req, false); msg != "" {
			return badRequest(msg)
		}

		before = auditViewOf(doc)
		doc.UnitID, doc.DepartmentID = req.UnitID.ID, req.DepartmentID.ID
		doc.UpdatedByID = &userID
		knowledge.SetArchivedState(doc, req.Status, models.KnowledgeArchivedByUser)
		if manual {
			return knowledge.ApplyScope(tx, doc) // content stays the importer's
		}
		doc.Title, doc.Body = req.Title, req.Body
		if req.SourceType != "" {
			doc.SourceType = req.SourceType
		}
		return knowledge.Save(tx, doc, knowledge.SectionsFor(doc.SourceType, doc.Title, req.Body))
	})
	if err != nil {
		return a.sendKnowledgeError(r, err, "update document")
	}
	after := auditViewOf(doc)
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionUpdated, before, &after)
	return r.SendEnvelope(doc)
}

// DeleteKnowledgeDocument DELETE /api/knowledge/documents/{id}: soft delete, the
// chunks are dropped (the text is gone from search immediately). It never touches
// the source HTML of an imported manual: the next import recreates the document
// if its section still exists (archive it to retire it for good).
func (a *App) DeleteKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	var doc *models.KnowledgeDocument
	var before knowledgeAuditView
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if doc, err = knowledge.Lock(tx, orgID, id); err != nil {
			return err
		}
		before = auditViewOf(doc)
		return knowledge.Remove(tx, doc)
	})
	if err != nil {
		return a.sendKnowledgeError(r, err, "delete document")
	}
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionDeleted, before, nil)
	return r.SendEnvelope(map[string]any{"message": "Document deleted"})
}

// ReindexKnowledgeDocument POST /api/knowledge/documents/{id}/reindex: rebuilds
// the chunks of one document from its stored text at the current index version.
func (a *App) ReindexKnowledgeDocument(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "document")
	if err != nil {
		return nil
	}
	var chunks, oldVersion int
	var doc *models.KnowledgeDocument
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		if doc, err = knowledge.Lock(tx, orgID, id); err != nil {
			return err
		}
		if err := tx.Model(&models.KnowledgeChunk{}).Where("document_id = ?", id).
			Select("COALESCE(MIN(index_version), 0)").Scan(&oldVersion).Error; err != nil {
			return err
		}
		chunks, err = knowledge.Reindex(tx, doc)
		return err
	})
	if err != nil {
		return a.sendKnowledgeError(r, err, "reindex document")
	}
	view := auditViewOf(doc)
	a.logAudit(orgID, userID, "knowledge_document", doc.ID, models.AuditActionUpdated, view, &view,
		map[string]any{"field": "reindexed", "old_value": oldVersion, "new_value": knowledge.IndexVersion})
	return r.SendEnvelope(map[string]any{"chunks": chunks, "index_version": knowledge.IndexVersion})
}

// ReindexKnowledge POST /api/knowledge/reindex?only_stale=true|false (default true):
// reindexes the organization one document per transaction.
func (a *App) ReindexKnowledge(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	onlyStale := true
	switch string(r.RequestCtx.QueryArgs().Peek("only_stale")) {
	case "", "true":
	case "false":
		onlyStale = false
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "only_stale must be true or false", nil, "")
	}
	res, err := knowledge.ReindexOrg(a.DB, orgID, onlyStale)
	if err != nil {
		a.Log.Error("Knowledge reindex failed", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to reindex", nil, "")
	}
	a.logAudit(orgID, userID, "knowledge_index", orgID, models.AuditActionUpdated, nil, nil,
		map[string]any{"field": "reindexed", "old_value": res.Documents + res.Skipped, "new_value": res.Documents})
	return r.SendEnvelope(res)
}

// KnowledgeIndexStatus GET /api/knowledge/status: how much exists and how much
// was built with an older indexing strategy (needs a reindex).
func (a *App) KnowledgeIndexStatus(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionWrite)
	if err != nil {
		return nil
	}
	st, err := knowledge.Status(a.DB, orgID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read status", nil, "")
	}
	return r.SendEnvelope(st)
}
