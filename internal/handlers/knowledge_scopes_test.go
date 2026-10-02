package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func scopeNames(list any) []string {
	var out []string
	arr, _ := list.([]any)
	for _, x := range arr {
		out = append(out, x.(map[string]any)["name"].(string))
	}
	return out
}

func (e knowEnv) scopes(t *testing.T, user uuid.UUID) (int, map[string]any) {
	t.Helper()
	return e.do(t, e.app.KnowledgeScopes, kreq{user: user})
}

func TestKnowledgeScopes_FollowTheCapabilityMatrix(t *testing.T) {
	e := newKnowEnv(t)

	// administration and conversations:view_all may choose a context: every unit/department of the organization
	for name, uid := range map[string]uuid.UUID{"write": e.admin.ID, "view_all": e.viewAll.ID} {
		s, data := e.scopes(t, uid)
		require.Equal(t, fasthttp.StatusOK, s, name)
		assert.Equal(t, true, data["can_choose_context"], name)
		assert.ElementsMatch(t, []string{e.u1.Name, e.u2.Name}, scopeNames(data["units"]), name)
		assert.ElementsMatch(t, []string{e.d1.Name, e.d2.Name}, scopeNames(data["departments"]), name)
	}

	// a plain reader sees only their own unit and department, and cannot choose
	s, data := e.scopes(t, e.reader.ID)
	require.Equal(t, fasthttp.StatusOK, s)
	assert.Equal(t, false, data["can_choose_context"])
	assert.Equal(t, []string{e.u1.Name}, scopeNames(data["units"]))
	assert.Equal(t, []string{e.d1.Name}, scopeNames(data["departments"]))
	own := data["own"].(map[string]any)
	assert.Equal(t, e.u1.ID.String(), own["unit_id"])
	assert.Equal(t, e.d1.ID.String(), own["department_id"])

	// a reader with no unit/department gets empty LISTS (never null) and null ownership
	s, data = e.scopes(t, e.readerNoUnit.ID)
	require.Equal(t, fasthttp.StatusOK, s)
	assert.Equal(t, false, data["can_choose_context"])
	assert.Equal(t, []any{}, data["units"])
	assert.Equal(t, []any{}, data["departments"])
	own = data["own"].(map[string]any)
	assert.Nil(t, own["unit_id"])
	assert.Nil(t, own["department_id"])

	// no knowledge:read, no answer
	s, _ = e.scopes(t, e.agent.ID)
	assert.Equal(t, fasthttp.StatusForbidden, s)
}

func TestKnowledgeScopes_OnlyIdNameActive_InactiveMarkedDeletedGone(t *testing.T) {
	e := newKnowEnv(t)
	// fields of other domains must not leak: give the units a CNPJ and an X2 store
	cod := "40"
	require.NoError(t, e.app.DB.Model(&models.Unit{}).Where("id = ?", e.u1.ID).
		Updates(map[string]any{"cnpj": "58675622000361", "xprocess_cod_empresa": cod, "type": "loja"}).Error)
	require.NoError(t, e.app.DB.Model(&models.Unit{}).Where("id = ?", e.u2.ID).Update("active", false).Error)
	require.NoError(t, e.app.DB.Delete(&models.Department{}, "id = ?", e.d2.ID).Error)

	s, data := e.scopes(t, e.admin.ID)
	require.Equal(t, fasthttp.StatusOK, s)
	units := data["units"].([]any)
	require.Len(t, units, 2)
	for _, u := range units {
		m := u.(map[string]any)
		keys := []string{}
		for k := range m {
			keys = append(keys, k)
		}
		assert.ElementsMatch(t, []string{"id", "name", "active"}, keys, "nothing but id, name and active")
		if m["id"] == e.u2.ID.String() {
			assert.Equal(t, false, m["active"], "inactive units are returned and marked")
		} else {
			assert.Equal(t, true, m["active"])
		}
	}
	assert.Equal(t, []string{e.d1.Name}, scopeNames(data["departments"]), "deleted departments never appear")
}

// An administrator of organization A cannot reach anything of organization B:
// not through the scopes list, not through direct ids.
func TestKnowledge_OrganizationIsolationAcrossScopesAndDirectIds(t *testing.T) {
	a := newKnowEnv(t)
	b := newKnowEnv(t)
	bDoc := b.create(t, "Segredo da B", "procedimento confidencial da organizacao B", &b.u1.ID, nil)
	bGlobal := b.create(t, "Global da B", "regra global da organizacao B", nil, nil)

	// scopes: never an item of B
	s, data := a.scopes(t, a.admin.ID)
	require.Equal(t, fasthttp.StatusOK, s)
	for _, n := range append(scopeNames(data["units"]), scopeNames(data["departments"])...) {
		assert.NotContains(t, []string{b.u1.Name, b.u2.Name, b.d1.Name, b.d2.Name}, n)
	}

	// direct ids of B's documents, through every endpoint, as A's administrator
	for name, h := range map[string]func(*fastglue.Request) error{
		"get": a.app.GetKnowledgeDocument, "chunks": a.app.ListKnowledgeChunks,
		"update": a.app.UpdateKnowledgeDocument, "delete": a.app.DeleteKnowledgeDocument,
		"reindex": a.app.ReindexKnowledgeDocument,
	} {
		for _, id := range []string{bDoc, bGlobal} {
			status, _ := a.do(t, h, kreq{user: a.admin.ID, id: id, body: map[string]any{
				"title": "x", "body": "y", "unit_id": nil, "department_id": nil}})
			assert.Equal(t, fasthttp.StatusNotFound, status, "%s %s", name, id)
		}
	}
	// ... and the documents are intact
	assert.Equal(t, "Segredo da B", b.docRow(t, bDoc).Title)
	var n int64
	b.app.DB.Model(&models.KnowledgeChunk{}).Where("document_id = ?", bDoc).Count(&n)
	assert.EqualValues(t, 1, n)

	// B's units/departments as a context or as a scope, from A
	for _, q := range []map[string]string{{"unit_id": b.u1.ID.String()}, {"department_id": b.d1.ID.String()}} {
		for name, h := range map[string]func(*fastglue.Request) error{"search": a.app.SearchKnowledge, "list": a.app.ListKnowledgeDocuments} {
			qq := map[string]string{"q": "confidencial"}
			for k, v := range q {
				qq[k] = v
			}
			status, _ := a.do(t, h, kreq{user: a.admin.ID, query: qq})
			assert.Equal(t, fasthttp.StatusBadRequest, status, "%s %v (administrator)", name, q)
			status, _ = a.do(t, h, kreq{user: a.reader.ID, query: qq})
			assert.Equal(t, fasthttp.StatusForbidden, status, "%s %v (reader)", name, q)
		}
	}
	status, _ := a.do(t, a.app.CreateKnowledgeDocument, kreq{user: a.admin.ID, body: map[string]any{
		"title": "t", "body": "texto qualquer", "source_type": "text", "unit_id": b.u1.ID.String()}})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a scope of another organization is refused on create")

	// search and list never return B's content
	_, titles, _ := a.search(t, a.admin.ID, "confidencial", nil)
	assert.Empty(t, titles)
	_, list := a.do(t, a.app.ListKnowledgeDocuments, kreq{user: a.admin.ID})
	assert.Empty(t, list["documents"])
	// and the other way round: B's own reads still work
	_, titles, _ = b.search(t, b.admin.ID, "confidencial", map[string]string{"unit_id": b.u1.ID.String()})
	assert.Equal(t, []string{"Segredo da B"}, titles)
}
