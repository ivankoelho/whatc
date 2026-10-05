package handlers_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func (e knowEnv) archive(t *testing.T, id string, unit, dept *uuid.UUID) {
	t.Helper()
	b := map[string]any{"title": "Arquivado", "body": "texto do documento arquivado", "status": "archived", "unit_id": nil, "department_id": nil}
	if unit != nil {
		b["unit_id"] = unit.String()
	}
	if dept != nil {
		b["department_id"] = dept.String()
	}
	s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: b})
	require.Equal(t, fasthttp.StatusOK, s)
}

func (e knowEnv) docRow(t *testing.T, id string) models.KnowledgeDocument {
	t.Helper()
	var d models.KnowledgeDocument
	require.NoError(t, e.app.DB.First(&d, "id = ?", id).Error)
	return d
}

func ids(list any) []string {
	var out []string
	if arr, ok := list.([]any); ok {
		for _, x := range arr {
			out = append(out, x.(map[string]any)["id"].(string))
		}
	}
	return out
}

// ---- M3: reading, choosing a context and administering are separate --------

func TestKnowledge8B_ChoosingAContextIsNotAdministration(t *testing.T) {
	e := newKnowEnv(t)
	global := e.create(t, "Global", "politica de devolucao para todos", nil, nil)
	unit2 := e.create(t, "Unidade 2", "politica de devolucao da unidade dois", &e.u2.ID, nil)
	arch := e.create(t, "Arquivado", "politica de devolucao antiga", nil, nil)
	e.archive(t, arch, nil, nil)

	// view_all + read: sees only ACTIVE documents, evaluated against the chosen context
	s, _ := e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.viewAll.ID, id: global})
	assert.Equal(t, fasthttp.StatusOK, s)
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.viewAll.ID, id: arch})
	assert.Equal(t, fasthttp.StatusNotFound, s, "archived is for administrators only")
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.viewAll.ID, id: unit2})
	assert.Equal(t, fasthttp.StatusNotFound, s, "no context chosen: a unit document is out of reach")
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.viewAll.ID, id: unit2, query: map[string]string{"unit_id": e.u2.ID.String()}})
	assert.Equal(t, fasthttp.StatusOK, s, "the chosen context makes it eligible")
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.viewAll.ID, id: unit2, query: map[string]string{"unit_id": e.u1.ID.String()}})
	assert.Equal(t, fasthttp.StatusNotFound, s)

	_, list := e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.viewAll.ID})
	assert.ElementsMatch(t, []string{global}, ids(list["documents"]))
	_, list = e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.viewAll.ID, query: map[string]string{"unit_id": e.u2.ID.String()}})
	assert.ElementsMatch(t, []string{global, unit2}, ids(list["documents"]))
	_, list = e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.viewAll.ID, query: map[string]string{"status": "archived"}})
	assert.NotContains(t, ids(list["documents"]), arch, "the status filter is an administrator tool")

	// chunks follow the same rules
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.viewAll.ID, id: arch})
	assert.Equal(t, fasthttp.StatusNotFound, s)
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.viewAll.ID, id: unit2})
	assert.Equal(t, fasthttp.StatusNotFound, s)
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.viewAll.ID, id: unit2, query: map[string]string{"unit_id": e.u2.ID.String()}})
	assert.Equal(t, fasthttp.StatusOK, s)

	// the administrator sees everything, archived included
	s, _ = e.do(t, e.app.GetKnowledgeDocument, kreq{user: e.admin.ID, id: arch})
	assert.Equal(t, fasthttp.StatusOK, s)
	_, list = e.do(t, e.app.ListKnowledgeDocuments, kreq{user: e.admin.ID})
	assert.ElementsMatch(t, []string{global, unit2, arch}, ids(list["documents"]))

	// view_all never gains write or index operations
	s, _ = e.do(t, e.app.ReindexKnowledgeDocument, kreq{user: e.viewAll.ID, id: global})
	assert.Equal(t, fasthttp.StatusForbidden, s)
	s, _ = e.do(t, e.app.ReindexKnowledge, kreq{user: e.viewAll.ID})
	assert.Equal(t, fasthttp.StatusForbidden, s)
	s, _ = e.do(t, e.app.KnowledgeIndexStatus, kreq{user: e.viewAll.ID})
	assert.Equal(t, fasthttp.StatusForbidden, s)
}

func TestKnowledge8B_ChunksRespectTheReadersReach(t *testing.T) {
	e := newKnowEnv(t)
	own := e.create(t, "Da unidade 1", strings.Repeat("regra da unidade um com bastante texto. ", 60), &e.u1.ID, nil)
	foreign := e.create(t, "Da unidade 2", "regra da unidade dois", &e.u2.ID, nil)

	s, data := e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.reader.ID, id: own})
	require.Equal(t, fasthttp.StatusOK, s)
	chunks := data["chunks"].([]any)
	assert.EqualValues(t, len(chunks), data["total"])
	require.GreaterOrEqual(t, len(chunks), 2, "a long document has several chunks")
	for i, c := range chunks {
		m := c.(map[string]any)
		assert.EqualValues(t, i, m["chunk_index"], "ordered")
		assert.EqualValues(t, knowledge.IndexVersion, m["index_version"])
		assert.Greater(t, m["char_count"].(float64), 0.0)
		assert.NotEmpty(t, m["content"])
	}
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.reader.ID, id: foreign})
	assert.Equal(t, fasthttp.StatusNotFound, s)
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.agent.ID, id: own})
	assert.Equal(t, fasthttp.StatusForbidden, s)

	other := newKnowEnv(t)
	otherDoc := other.create(t, "Outra org", "texto da outra organizacao", nil, nil)
	s, _ = e.do(t, e.app.ListKnowledgeChunks, kreq{user: e.admin.ID, id: otherDoc})
	assert.Equal(t, fasthttp.StatusNotFound, s)
}

// ---- archived_by through the API --------------------------------------------

func TestKnowledge8B_ArchivingThroughTheAPIRecordsWhoAndReactivationClears(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Doc", "texto do documento para arquivar", nil, nil)
	assert.Equal(t, models.KnowledgeArchivedByNone, e.docRow(t, id).ArchivedBy)

	put := func(status string) int {
		s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: map[string]any{
			"title": "Doc", "body": "texto do documento para arquivar", "unit_id": nil, "department_id": nil, "status": status}})
		return s
	}
	require.Equal(t, fasthttp.StatusOK, put("archived"))
	assert.Equal(t, "archived/user", e.docRow(t, id).Status+"/"+e.docRow(t, id).ArchivedBy)
	require.Equal(t, fasthttp.StatusOK, put("active"))
	assert.Equal(t, "active/", e.docRow(t, id).Status+"/"+e.docRow(t, id).ArchivedBy)

	// created archived straight away
	s, data := e.do(t, e.app.CreateKnowledgeDocument, kreq{user: e.admin.ID, body: map[string]any{
		"title": "Nasce arquivado", "body": "texto qualquer do documento", "source_type": "text", "status": "archived"}})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.Equal(t, "user", data["archived_by"])
}

// ---- M7: the lifecycle of manual_html ---------------------------------------

func (e knowEnv) importedManual(t *testing.T) models.KnowledgeDocument {
	t.Helper()
	dir := t.TempDir()
	html := `<html><body><section id="fila"><h2>Fila de espera</h2><p>Como funciona a fila de espera do atendimento, com texto suficiente para virar documento.</p></section></body></html>`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guia.html"), []byte(html), 0o600))
	_, err := knowledge.ImportManuals(e.app.DB, e.org.ID, nil, nil, dir, false)
	require.NoError(t, err)
	var d models.KnowledgeDocument
	require.NoError(t, e.app.DB.First(&d, "organization_id = ? AND origin = ?", e.org.ID, "manual/guia.html#fila").Error)
	return d
}

func TestKnowledge8B_ManualHTMLContentBelongsToTheImporter(t *testing.T) {
	e := newKnowEnv(t)
	d := e.importedManual(t)
	id := d.ID.String()
	put := func(body map[string]any) (int, map[string]any) {
		return e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: body})
	}
	scope := func(b map[string]any) map[string]any {
		if _, ok := b["unit_id"]; !ok {
			b["unit_id"] = nil
		}
		if _, ok := b["department_id"]; !ok {
			b["department_id"] = nil
		}
		return b
	}

	// content differs: 409, nothing changes
	s, _ := put(scope(map[string]any{"title": "Outro titulo"}))
	assert.Equal(t, fasthttp.StatusConflict, s)
	s, _ = put(scope(map[string]any{"body": "um corpo qualquer editado a mao"}))
	assert.Equal(t, fasthttp.StatusConflict, s)
	s, _ = put(scope(map[string]any{"source_type": "text"}))
	assert.Equal(t, fasthttp.StatusConflict, s)
	after := e.docRow(t, id)
	assert.Equal(t, d.Title, after.Title)
	assert.Equal(t, d.Body, after.Body)
	assert.Equal(t, d.ContentHash, after.ContentHash)

	// scope is still mandatory
	s, _ = put(map[string]any{"status": "active"})
	assert.Equal(t, fasthttp.StatusBadRequest, s)

	// scope and status change through the API; title/body may be omitted or identical
	s, _ = put(map[string]any{"unit_id": e.u1.ID.String(), "department_id": nil})
	assert.Equal(t, fasthttp.StatusOK, s)
	s, _ = put(map[string]any{"title": d.Title, "body": d.Body, "unit_id": e.u1.ID.String(), "department_id": nil, "source_type": "manual_html"})
	assert.Equal(t, fasthttp.StatusOK, s, "an identical title/body round trip is accepted")
	row := e.docRow(t, id)
	require.NotNil(t, row.UnitID)
	assert.Equal(t, models.KnowledgeVisibilityUnit, row.Visibility)
	var c models.KnowledgeChunk
	require.NoError(t, e.app.DB.First(&c, "document_id = ?", d.ID).Error)
	require.NotNil(t, c.UnitID, "the chunks followed the new scope without being rebuilt")
	assert.Equal(t, d.Body, row.Body)

	s, _ = put(scope(map[string]any{"unit_id": e.u1.ID.String(), "status": "archived"}))
	assert.Equal(t, fasthttp.StatusOK, s)
	assert.Equal(t, "archived/user", e.docRow(t, id).Status+"/"+e.docRow(t, id).ArchivedBy)
	_, _, _ = e.search(t, e.admin.ID, "fila", map[string]string{"unit_id": e.u1.ID.String()})
	_, titles, _ := e.search(t, e.admin.ID, "fila", map[string]string{"unit_id": e.u1.ID.String()})
	assert.Empty(t, titles, "archived: out of search")
}

func TestKnowledge8B_ManualHTMLCannotBeCreatedAndDeleteIsRecreatedByTheImport(t *testing.T) {
	e := newKnowEnv(t)
	s, _ := e.do(t, e.app.CreateKnowledgeDocument, kreq{user: e.admin.ID, body: map[string]any{
		"title": "t", "body": "texto qualquer", "source_type": "manual_html"}})
	assert.Equal(t, fasthttp.StatusBadRequest, s)

	d := e.importedManual(t)
	s, _ = e.do(t, e.app.DeleteKnowledgeDocument, kreq{user: e.admin.ID, id: d.ID.String()})
	require.Equal(t, fasthttp.StatusOK, s)
	var n int64
	e.app.DB.Model(&models.KnowledgeDocument{}).Where("organization_id = ? AND origin = ?", e.org.ID, "manual/guia.html#fila").Count(&n)
	assert.Zero(t, n)

	// the source HTML is untouched, so the next import recreates the document
	dir := t.TempDir()
	html := `<html><body><section id="fila"><h2>Fila de espera</h2><p>Como funciona a fila de espera do atendimento, com texto suficiente para virar documento.</p></section></body></html>`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guia.html"), []byte(html), 0o600))
	res, err := knowledge.ImportManuals(e.app.DB, e.org.ID, nil, nil, dir, false)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Created)
}

func TestKnowledge8B_AHandMadeDocumentStaysFreelyEditable(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "FAQ", "resposta antiga da pergunta", nil, nil)
	s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: map[string]any{
		"title": "FAQ nova", "body": "resposta nova da pergunta", "unit_id": nil, "department_id": nil}})
	assert.Equal(t, fasthttp.StatusOK, s)
	assert.Equal(t, "FAQ nova", e.docRow(t, id).Title)
}

// ---- M6: concurrency and the optimistic check -------------------------------

func TestKnowledge8B_ConcurrentEditsOfOneDocumentAllSucceedAndStayConsistent(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Doc", strings.Repeat("texto inicial do documento. ", 80), nil, nil)

	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: map[string]any{
				"title": "Doc", "body": strings.Repeat("texto editado numero. ", 60+i*9), "unit_id": nil, "department_id": nil}})
			statuses <- s
		}()
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		assert.Equal(t, fasthttp.StatusOK, s)
	}

	final := e.docRow(t, id)
	var chunks []models.KnowledgeChunk
	require.NoError(t, e.app.DB.Where("document_id = ?", id).Order("chunk_index").Find(&chunks).Error)
	want := knowledge.Chunk(knowledge.SectionsFor(final.SourceType, final.Title, final.Body))
	require.Len(t, chunks, len(want))
	for i, c := range chunks {
		assert.Equal(t, i, c.ChunkIndex)
		assert.Equal(t, want[i].Content, c.Content)
	}
}

func TestKnowledge8B_ExpectedUpdatedAtDetectsAConcurrentChange(t *testing.T) {
	e := newKnowEnv(t)
	id := e.create(t, "Doc", "texto original do documento", nil, nil)
	read := e.docRow(t, id)
	stamp := read.UpdatedAt.Format(time.RFC3339Nano)
	body := func(text string, expected any) map[string]any {
		b := map[string]any{"title": "Doc", "body": text, "unit_id": nil, "department_id": nil}
		if expected != nil {
			b["expected_updated_at"] = expected
		}
		return b
	}

	s, _ := e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: body("primeira edicao", stamp)})
	assert.Equal(t, fasthttp.StatusOK, s, "the version the client read is still current")
	// a second client still holds the old version
	s, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: body("edicao atrasada", stamp)})
	assert.Equal(t, fasthttp.StatusConflict, s)
	assert.Equal(t, "primeira edicao", e.docRow(t, id).Body, "the stale edit did not overwrite")
	// without the field: last writer wins (documented)
	s, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: body("sem checagem", nil)})
	assert.Equal(t, fasthttp.StatusOK, s)
	// a malformed timestamp is a bad request
	s, _ = e.do(t, e.app.UpdateKnowledgeDocument, kreq{user: e.admin.ID, id: id, body: body("x", "ontem")})
	assert.Equal(t, fasthttp.StatusBadRequest, s)
}

// ---- reindex and status endpoints -------------------------------------------

func TestKnowledge8B_ReindexAndStatusEndpoints(t *testing.T) {
	e := newKnowEnv(t)
	a := e.create(t, "A", "texto do primeiro documento que nao foi aceito", nil, nil)
	b := e.create(t, "B", "texto do segundo documento", nil, nil)
	// both built by the old strategy
	require.NoError(t, e.app.DB.Model(&models.KnowledgeChunk{}).Where("organization_id = ?", e.org.ID).Update("index_version", 1).Error)

	s, st := e.do(t, e.app.KnowledgeIndexStatus, kreq{user: e.admin.ID})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.EqualValues(t, 2, st["documents"])
	assert.EqualValues(t, 2, st["chunks"])
	assert.EqualValues(t, 2, st["stale_chunks"])
	assert.EqualValues(t, knowledge.IndexVersion, st["index_version"])

	// one document
	s, data := e.do(t, e.app.ReindexKnowledgeDocument, kreq{user: e.admin.ID, id: a})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.EqualValues(t, 1, data["chunks"])
	assert.EqualValues(t, knowledge.IndexVersion, data["index_version"])
	_, st = e.do(t, e.app.KnowledgeIndexStatus, kreq{user: e.admin.ID})
	assert.EqualValues(t, 1, st["stale_chunks"])

	// reindexing is audited with the version change
	var entry models.AuditLog
	// The audit rows are written asynchronously: wait for the one that carries the reindex
	// (waiting for "any row of the document" could return the creation row, which comes first).
	require.Eventually(t, func() bool {
		return e.app.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ? AND changes::text LIKE ?",
			e.org.ID, "knowledge_document", uuid.MustParse(a), "%reindexed%").
			Order("created_at DESC").First(&entry).Error == nil
	}, 5*time.Second, 50*time.Millisecond)
	assert.Contains(t, string(mustJSON(t, entry.Changes)), "reindexed")

	// the rest of the organization, only stale
	s, res := e.do(t, e.app.ReindexKnowledge, kreq{user: e.admin.ID})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.EqualValues(t, 1, res["documents"])
	assert.EqualValues(t, 0, res["stale_remaining"])
	s, res = e.do(t, e.app.ReindexKnowledge, kreq{user: e.admin.ID, query: map[string]string{"only_stale": "true"}})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.EqualValues(t, 0, res["documents"], "repeatable: nothing stale is left")
	s, res = e.do(t, e.app.ReindexKnowledge, kreq{user: e.admin.ID, query: map[string]string{"only_stale": "false"}})
	require.Equal(t, fasthttp.StatusOK, s)
	assert.EqualValues(t, 2, res["documents"])
	s, _ = e.do(t, e.app.ReindexKnowledge, kreq{user: e.admin.ID, query: map[string]string{"only_stale": "maybe"}})
	assert.Equal(t, fasthttp.StatusBadRequest, s)

	// permissions and isolation
	for name, call := range map[string]kreq{
		"reader doc": {user: e.reader.ID, id: b}, "agent doc": {user: e.agent.ID, id: b},
	} {
		s, _ := e.do(t, e.app.ReindexKnowledgeDocument, call)
		assert.Equal(t, fasthttp.StatusForbidden, s, name)
	}
	s, _ = e.do(t, e.app.ReindexKnowledge, kreq{user: e.reader.ID})
	assert.Equal(t, fasthttp.StatusForbidden, s)
	other := newKnowEnv(t)
	otherDoc := other.create(t, "Outra", "texto da outra organizacao", nil, nil)
	s, _ = e.do(t, e.app.ReindexKnowledgeDocument, kreq{user: e.admin.ID, id: otherDoc})
	assert.Equal(t, fasthttp.StatusNotFound, s)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// A query that is only stop words answers an EMPTY LIST, not null.
func TestKnowledge8B_StopWordOnlySearchReturnsAnEmptyList(t *testing.T) {
	e := newKnowEnv(t)
	e.create(t, "Doc", "o cliente não aceitou", nil, nil)
	for _, q := range []string{"não", `-"não"`, "você também", "de a o"} {
		s, data := e.do(t, e.app.SearchKnowledge, kreq{user: e.admin.ID, query: map[string]string{"q": q}})
		require.Equal(t, fasthttp.StatusOK, s, q)
		results, ok := data["results"].([]any)
		assert.True(t, ok, "results must be a JSON list, not null (%q)", q)
		assert.Empty(t, results, q)
	}
}
