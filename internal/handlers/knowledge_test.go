package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type knowEnv struct {
	app          *handlers.App
	org          *models.Organization
	admin        *models.User
	u1, u2       models.Unit
	d1, d2       models.Department
	reader       *models.User // knowledge:read only, works in u1 / d1
	readerNoUnit *models.User // knowledge:read only, no unit / department
	viewAll      *models.User // knowledge:read + conversations:view_all, no unit
	agent        *models.User // no knowledge permission
}

func newKnowEnv(t *testing.T) knowEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	e := knowEnv{app: app, org: org}
	e.admin = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	mk := func(n string) models.Unit {
		u := models.Unit{OrganizationID: org.ID, Name: n + uuid.NewString()[:6], Active: true}
		require.NoError(t, app.DB.Create(&u).Error)
		return u
	}
	mkd := func(n string) models.Department {
		d := models.Department{OrganizationID: org.ID, Name: n + uuid.NewString()[:6], Active: true}
		require.NoError(t, app.DB.Create(&d).Error)
		return d
	}
	e.u1, e.u2, e.d1, e.d2 = mk("U1"), mk("U2"), mkd("D1"), mkd("D2")

	readRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "kread", []string{"knowledge:read"})
	e.reader = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&readRole.ID))
	require.NoError(t, app.DB.Model(&models.User{}).Where("id = ?", e.reader.ID).
		Updates(map[string]any{"unit_id": e.u1.ID, "department_id": e.d1.ID}).Error)
	e.readerNoUnit = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&readRole.ID))
	allRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "kall", []string{"knowledge:read", "conversations:view_all"})
	e.viewAll = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&allRole.ID))
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	e.agent = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	return e
}

type kreq struct {
	user  uuid.UUID
	org   uuid.UUID
	body  any
	query map[string]string
	id    string
}

func (e knowEnv) do(t *testing.T, h func(*fastglue.Request) error, r kreq) (int, map[string]any) {
	t.Helper()
	var req *fastglue.Request
	if r.body != nil {
		req = testutil.NewJSONRequest(t, r.body)
	} else {
		req = testutil.NewGETRequest(t)
	}
	org := r.org
	if org == uuid.Nil {
		org = e.org.ID
	}
	testutil.SetAuthContext(req, org, r.user)
	for k, v := range r.query {
		testutil.SetQueryParam(req, k, v)
	}
	if r.id != "" {
		testutil.SetPathParam(req, "id", r.id)
	}
	require.NoError(t, h(req))
	var resp struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func (e knowEnv) create(t *testing.T, title, body string, unit, dept *uuid.UUID) string {
	t.Helper()
	b := map[string]any{"title": title, "body": body, "source_type": "article"}
	if unit != nil {
		b["unit_id"] = unit.String()
	}
	if dept != nil {
		b["department_id"] = dept.String()
	}
	status, data := e.do(t, e.app.CreateKnowledgeDocument, kreq{user: e.admin.ID, body: b})
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	return data["id"].(string)
}

func (e knowEnv) search(t *testing.T, user uuid.UUID, q string, extra map[string]string) (int, []string, map[string]any) {
	t.Helper()
	query := map[string]string{"q": q}
	for k, v := range extra {
		query[k] = v
	}
	status, data := e.do(t, e.app.SearchKnowledge, kreq{user: user, query: query})
	var titles []string
	if res, ok := data["results"].([]any); ok {
		for _, x := range res {
			titles = append(titles, x.(map[string]any)["title"].(string))
		}
	}
	return status, titles, data
}

func TestKnowledge_PermissionsGateEveryEndpoint(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Doc", "texto sobre reembolso de cliente", nil, nil)

	// no knowledge permission at all
	for name, h := range map[string]func(*fastglue.Request) error{
		"search": e.app.SearchKnowledge, "list": e.app.ListKnowledgeDocuments,
		"get": e.app.GetKnowledgeDocument, "create": e.app.CreateKnowledgeDocument,
		"update": e.app.UpdateKnowledgeDocument, "delete": e.app.DeleteKnowledgeDocument,
	} {
		status, _ := e.do(t, h, kreq{user: e.agent.ID, id: id, query: map[string]string{"q": "x"},
			body: map[string]any{"title": "x", "body": "y", "source_type": "text"}})
		assert.Equal(t, fasthttp.StatusForbidden, status, name)
	}

	// read-only user: reads, never writes
	s, _, _ := e.search(t, e.readerNoUnit.ID, "reembolso", nil)
	assert.Equal(t, fasthttp.StatusOK, s)
	body := map[string]any{"title": "x", "body": "y", "source_type": "text"}
	for name, h := range map[string]func(*fastglue.Request) error{
		"create": e.app.CreateKnowledgeDocument, "update": e.app.UpdateKnowledgeDocument, "delete": e.app.DeleteKnowledgeDocument,
	} {
		status, _ := e.do(t, h, kreq{user: e.readerNoUnit.ID, id: id, body: body})
		assert.Equal(t, fasthttp.StatusForbidden, status, name)
	}
}

func TestKnowledge_CrudAndSearchWithCitation(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Como emitir orçamento", "O vendedor emite o orçamento no sistema.", nil, nil)

	status, titles, data := e.search(t, e.admin.ID, "orcamento", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"Como emitir orçamento"}, titles)
	hit := data["results"].([]any)[0].(map[string]any)
	assert.Equal(t, id, hit["document_id"])
	assert.NotEmpty(t, hit["chunk_id"])
	assert.Greater(t, hit["score"].(float64), 0.0)
	cit := hit["citation"].(map[string]any)
	assert.Equal(t, "Como emitir orçamento", cit["title"])
	assert.Equal(t, "article", cit["source_type"])

	// update replaces the content; the old text is no longer found
	status, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id,
		body: map[string]any{"title": "Como emitir pedido", "body": "O vendedor emite o pedido no sistema."}})
	require.Equal(t, fasthttp.StatusOK, status)
	_, titles, _ = e.search(t, e.admin.ID, "orcamento", nil)
	assert.Empty(t, titles)
	_, titles, _ = e.search(t, e.admin.ID, "pedido", nil)
	assert.Equal(t, []string{"Como emitir pedido"}, titles)

	// archive: out of search; admin still lists it; delete: gone
	status, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id,
		body: map[string]any{"title": "Como emitir pedido", "body": "O vendedor emite o pedido no sistema.", "status": "archived"}})
	require.Equal(t, fasthttp.StatusOK, status)
	_, titles, _ = e.search(t, e.admin.ID, "pedido", nil)
	assert.Empty(t, titles)
	_, list := e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.admin.ID, query: map[string]string{"status": "archived"}})
	assert.Len(t, list["documents"], 1)

	status, _ = e.do(t, e.app.DeleteKnowledgeDocument, kreq{user: e.admin.ID, id: id})
	assert.Equal(t, fasthttp.StatusOK, status)
	status, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.admin.ID, id: id})
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestKnowledge_Validation(t *testing.T) {
	e := newKnowEnv(t)
	otherOrg := testutil.CreateTestOrganization(t, e.app.DB)
	foreignUnit := models.Unit{OrganizationID: otherOrg.ID, Name: "X" + uuid.NewString()[:6], Active: true}
	require.NoError(t, e.app.DB.Create(&foreignUnit).Error)

	bad := []map[string]any{
		{"title": "", "body": "x", "source_type": "text"},
		{"title": "t", "body": "  ", "source_type": "text"},
		{"title": "t", "body": "x", "source_type": ""},
		{"title": "t", "body": "x", "source_type": "manual_html"}, // CLI-only
		{"title": "t", "body": "x", "source_type": "pdf"},
		{"title": "t", "body": "x", "source_type": "text", "status": "weird"},
		{"title": "t", "body": "x", "source_type": "text", "unit_id": foreignUnit.ID.String()},
		{"title": "t", "body": "x", "source_type": "text", "department_id": uuid.NewString()},
	}
	for i, b := range bad {
		status, _ := e.do(t, e.app.CreateKnowledgeDocument, kreq{user: e.admin.ID, body: b})
		assert.Equal(t, fasthttp.StatusBadRequest, status, "case %d: %v", i, b)
	}
	status, _, _ := e.search(t, e.admin.ID, "   ", nil)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "blank q")
}

func TestKnowledge_OtherOrganizationIsInvisible(t *testing.T) {
	e := newKnowEnv(t)
	other := newKnowEnv(t)
	id := other.create(t, "Segredo", "procedimento confidencial de cofre", nil, nil)

	_, titles, _ := e.search(t, e.admin.ID, "cofre", nil)
	assert.Empty(t, titles)
	for name, h := range map[string]func(*fastglue.Request) error{
		"get": e.app.GetKnowledgeDocument, "update": e.app.UpdateKnowledgeDocument, "delete": e.app.DeleteKnowledgeDocument,
	} {
		status, _ := e.do(t, h, kreq{user: e.admin.ID, id: id, body: map[string]any{"title": "x", "body": "y"}})
		assert.Equal(t, fasthttp.StatusNotFound, status, name)
	}
	_, list := e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.admin.ID})
	assert.Empty(t, list["documents"])
}

func TestKnowledge_ReachFollowsMembershipAndForbidsOtherContexts(t *testing.T) {
	e := newKnowEnv(t)
	e.create(t, "Global", "politica de reembolso para todos", nil, nil)
	e.create(t, "Da unidade 1", "politica de reembolso da unidade um", &e.u1.ID, nil)
	e.create(t, "Da unidade 2", "politica de reembolso da unidade dois", &e.u2.ID, nil)
	e.create(t, "Do depto 1", "politica de reembolso do departamento um", nil, &e.d1.ID)
	e.create(t, "Do depto 2", "politica de reembolso do departamento dois", nil, &e.d2.ID)

	// reader (unit 1, dept 1): own context without parameters
	_, got, _ := e.search(t, e.reader.ID, "reembolso", nil)
	assert.ElementsMatch(t, []string{"Global", "Da unidade 1", "Do depto 1"}, got)
	// asking explicitly for its own context is fine
	s, got, _ := e.search(t, e.reader.ID, "reembolso", map[string]string{"unit_id": e.u1.ID.String(), "department_id": e.d1.ID.String()})
	assert.Equal(t, fasthttp.StatusOK, s)
	assert.ElementsMatch(t, []string{"Global", "Da unidade 1", "Do depto 1"}, got)
	// another unit or department: 403, never silently ignored
	for _, q := range []map[string]string{{"unit_id": e.u2.ID.String()}, {"department_id": e.d2.ID.String()}, {"unit_id": uuid.NewString()}} {
		s, got, _ = e.search(t, e.reader.ID, "reembolso", q)
		assert.Equal(t, fasthttp.StatusForbidden, s, "%v", q)
		assert.Empty(t, got)
	}
	// a user with no unit/department sees only global content, and cannot ask for more
	_, got, _ = e.search(t, e.readerNoUnit.ID, "reembolso", nil)
	assert.Equal(t, []string{"Global"}, got)
	s, _, _ = e.search(t, e.readerNoUnit.ID, "reembolso", map[string]string{"unit_id": e.u1.ID.String()})
	assert.Equal(t, fasthttp.StatusForbidden, s)

	// global reach (knowledge:write or conversations:view_all) picks the context
	for _, uid := range []uuid.UUID{e.admin.ID, e.viewAll.ID} {
		_, got, _ = e.search(t, uid, "reembolso", nil)
		assert.Equal(t, []string{"Global"}, got, "no parameter: global content only")
		_, got, _ = e.search(t, uid, "reembolso", map[string]string{"unit_id": e.u2.ID.String()})
		assert.ElementsMatch(t, []string{"Global", "Da unidade 2"}, got)
		_, got, _ = e.search(t, uid, "reembolso", map[string]string{"department_id": e.d2.ID.String()})
		assert.ElementsMatch(t, []string{"Global", "Do depto 2"}, got)
	}
	// ... but only for units/departments of its own organization
	s, _, _ = e.search(t, e.admin.ID, "reembolso", map[string]string{"unit_id": uuid.NewString()})
	assert.Equal(t, fasthttp.StatusBadRequest, s)
	s, _, _ = e.search(t, e.admin.ID, "reembolso", map[string]string{"unit_id": "not-a-uuid"})
	assert.Equal(t, fasthttp.StatusBadRequest, s)
}

func TestKnowledge_DocumentReadsRespectTheSameReach(t *testing.T) {
	e := newKnowEnv(t)
	own := e.create(t, "Da unidade 1", "texto da unidade um", &e.u1.ID, nil)
	foreign := e.create(t, "Da unidade 2", "texto da unidade dois", &e.u2.ID, nil)

	s, _ := e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.reader.ID, id: own})
	assert.Equal(t, fasthttp.StatusOK, s)
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.reader.ID, id: foreign})
	assert.Equal(t, fasthttp.StatusNotFound, s, "outside the reach: indistinguishable from missing")

	_, list := e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.reader.ID})
	require.Len(t, list["documents"], 1)
	_, list = e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.admin.ID})
	assert.Len(t, list["documents"], 2, "administrators list everything")
	_, list = e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.admin.ID, query: map[string]string{"unit_id": e.u2.ID.String()}})
	assert.Len(t, list["documents"], 1)

	// archived documents are invisible to readers, visible to administrators
	s, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: own,
		body: map[string]any{"title": "Da unidade 1", "body": "texto da unidade um", "unit_id": e.u1.ID.String(), "status": "archived"}})
	require.Equal(t, fasthttp.StatusOK, s)
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.reader.ID, id: own})
	assert.Equal(t, fasthttp.StatusNotFound, s)
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.admin.ID, id: own})
	assert.Equal(t, fasthttp.StatusOK, s)
}

func TestKnowledge_ChangesAreAudited(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Doc auditado", "texto inicial", &e.u1.ID, nil)
	docID := uuid.MustParse(id)

	entries := func(action models.AuditAction) []models.AuditLog {
		var out []models.AuditLog
		require.Eventually(t, func() bool {
			e.app.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ? AND action = ?",
				e.org.ID, "knowledge_document", docID, action).Find(&out)
			return len(out) > 0
		}, 3*time.Second, 50*time.Millisecond, string(action))
		return out
	}
	entries(models.AuditActionCreated)

	// moving to global (clearing the unit) must be recorded as a change
	s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id,
		body: map[string]any{"title": "Doc auditado", "body": "texto novo"}})
	require.Equal(t, fasthttp.StatusOK, s)
	upd := entries(models.AuditActionUpdated)
	changes, _ := json.Marshal(upd[0].Changes)
	assert.Contains(t, string(changes), "unit_id")
	assert.Contains(t, string(changes), "visibility")
	assert.Contains(t, string(changes), "content_hash")
	assert.NotContains(t, string(changes), "texto novo", "the body itself is not copied into the audit log")

	s, _ = e.do(t, e.app.DeleteKnowledgeDocument, kreq{user: e.admin.ID, id: id})
	require.Equal(t, fasthttp.StatusOK, s)
	entries(models.AuditActionDeleted)
}
