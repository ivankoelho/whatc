package knowledge_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---- M1: accented stop words, syntax preserved -----------------------------

func TestSearchForm_StopwordsAreRemovedAndSyntaxIsKept(t *testing.T) {
	cases := map[string]string{
		"Não emitir":              "emitir",
		"nao emitir":              "emitir", // typed without the accent: same
		"Orçamento já pago":       "orcamento pago",
		`"não emitir"`:            `"emitir"`,
		`"não"`:                   "",
		`-"não"`:                  "",
		`-"não" foo`:              "foo",
		`foo OR não`:              "foo",
		`não OR foo`:              "foo",
		`foo OR não OR bar`:       "foo or bar",
		`-não foo`:                "foo",
		`foo -bar`:                "foo -bar",
		`"emitir nota" -garantia`: `"emitir nota" -garantia`,
		"você também":             "",
	}
	for in, want := range cases {
		assert.Equal(t, want, knowledge.SearchForm(in), in)
	}
}

// Every listed word is a stop word for PostgreSQL's 'portuguese' (so the list
// cannot drift from the database), and removing it leaves a query PostgreSQL
// reads with the same structure.
func TestStopwords_AgreeWithPostgres(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ts := func(sql string, args ...any) string {
		var out string
		require.NoError(t, db.Raw(sql, args...).Scan(&out).Error)
		return out
	}
	for _, w := range knowledge.AccentedStopwords() {
		assert.Equal(t, "", ts(`SELECT to_tsvector('portuguese', ?)::text`, w), "PostgreSQL does not treat %q as a stop word", w)
		assert.Equal(t, "", knowledge.SearchForm(w), w)
	}
	// real words with accents are not touched
	assert.Equal(t, "orcamento ocorrencia", knowledge.SearchForm("orçamento ocorrência"))

	query := map[string]string{
		`"não emitir"`:   "'emit'",
		`-"não" emitir`:  "'emit'",
		`emitir OR não`:  "'emit'",
		`emitir OR nota`: "'emit' | 'not'",
		`"emitir nota"`:  "'emit' <-> 'not'",
		`foo -bar`:       "'foo' & !'bar'",
		`orçamento`:      "'orcament'",
	}
	for q, want := range query {
		got := ts(`SELECT websearch_to_tsquery('portuguese', ?)::text`, knowledge.SearchForm(q))
		assert.Equal(t, want, got, q)
	}
}

func TestIndexedCopyDropsStopwordsButTheTextStaysOriginal(t *testing.T) {
	db, org, r := setup(t)
	d := add(t, db, org, nil, nil, "Pedido", "O cliente não aceitou, já pagou e você também viu o orçamento.")
	var c models.KnowledgeChunk
	require.NoError(t, db.First(&c, "document_id = ?", d.ID).Error)
	assert.Contains(t, c.Content, "não aceitou", "the displayed text keeps its accents and words")
	assert.NotContains(t, c.SearchText, "nao")
	assert.NotContains(t, c.SearchText, "voce")
	assert.Contains(t, c.SearchText, "orcamento")
	assert.Equal(t, knowledge.IndexVersion, c.IndexVersion)

	assert.Empty(t, search(t, r, org, nil, nil, "não", 5), "only a stop word: nothing")
	assert.Len(t, search(t, r, org, nil, nil, "aceitou orçamento", 5), 1)
	assert.Len(t, search(t, r, org, nil, nil, `"não aceitou"`, 5), 1, "a phrase with a stop word still matches")
}

// ---- reindex and index version ---------------------------------------------

func TestIndexVersion_EditKeepsCurrentAndReindexFixesStaleChunks(t *testing.T) {
	db, org, r := setup(t)
	d := add(t, db, org, nil, nil, "Doc", "texto sobre pedidos que não foram aceitos")

	// simulate a chunk built by the 8A strategy: version 1 and the stop word indexed
	require.NoError(t, db.Model(&models.KnowledgeChunk{}).Where("document_id = ?", d.ID).
		Updates(map[string]any{"index_version": 1, "search_text": "texto sobre pedidos que nao foram aceitos"}).Error)
	st, err := knowledge.Status(db, org)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.StaleChunks)
	assert.Equal(t, knowledge.IndexVersion, st.IndexVersion)

	var before models.KnowledgeDocument
	require.NoError(t, db.First(&before, "id = ?", d.ID).Error)
	res, err := knowledge.ReindexOrg(db, org, true)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Documents)
	assert.EqualValues(t, 0, res.StaleRemaining)
	var c models.KnowledgeChunk
	require.NoError(t, db.First(&c, "document_id = ?", d.ID).Error)
	assert.Equal(t, knowledge.IndexVersion, c.IndexVersion)
	assert.NotContains(t, c.SearchText, "nao")
	var after models.KnowledgeDocument
	require.NoError(t, db.First(&after, "id = ?", d.ID).Error)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "reindex does not touch the document row")
	assert.Len(t, search(t, r, org, nil, nil, "pedidos aceitos", 5), 1)

	// only_stale is repeatable: nothing left to do
	res, err = knowledge.ReindexOrg(db, org, true)
	require.NoError(t, err)
	assert.Equal(t, knowledge.ReindexResult{Documents: 0, Skipped: 0, Chunks: 0, StaleRemaining: 0}, res)
	// all = true rebuilds anyway
	res, err = knowledge.ReindexOrg(db, org, false)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Documents)

	// editing a document rebuilds at the current version (a content edit is not a strategy change)
	d.Body = "outro texto qualquer sobre containers"
	require.NoError(t, knowledge.Save(db, d, knowledge.SectionsFor(d.SourceType, d.Title, d.Body)))
	var c3 models.KnowledgeChunk
	require.NoError(t, db.First(&c3, "document_id = ?", d.ID).Error)
	assert.Equal(t, knowledge.IndexVersion, c3.IndexVersion)
}

func TestReindexOrg_StaysInsideTheOrganizationAndRebuildsArchivedToo(t *testing.T) {
	db, org, _ := setup(t)
	other := testutil.CreateTestOrganization(t, db).ID
	mine := add(t, db, org, nil, nil, "Meu", "conteudo arquivado do cliente")
	theirs := add(t, db, other, nil, nil, "Dele", "conteudo de outra organizacao")
	setStatus(t, db, mine, models.KnowledgeStatusArchived)
	require.NoError(t, db.Model(&models.KnowledgeChunk{}).Where("document_id IN ?", []uuid.UUID{mine.ID, theirs.ID}).
		Update("index_version", 1).Error)

	res, err := knowledge.ReindexOrg(db, org, true)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Documents)
	var c models.KnowledgeChunk
	require.NoError(t, db.First(&c, "document_id = ?", mine.ID).Error)
	assert.Equal(t, knowledge.IndexVersion, c.IndexVersion)
	assert.Equal(t, models.KnowledgeStatusArchived, c.Status, "reindex keeps the status")
	var c2 models.KnowledgeChunk
	require.NoError(t, db.First(&c2, "document_id = ?", theirs.ID).Error)
	assert.Equal(t, 1, c2.IndexVersion, "another organization is untouched")
}

// ---- archived_by: the state machine and the constraint ---------------------

func TestArchivedBy_StateMachine(t *testing.T) {
	d := &models.KnowledgeDocument{Status: models.KnowledgeStatusActive}
	knowledge.SetArchivedState(d, models.KnowledgeStatusArchived, models.KnowledgeArchivedByUser)
	assert.Equal(t, "archived/user", d.Status+"/"+d.ArchivedBy)
	knowledge.SetArchivedState(d, models.KnowledgeStatusActive, models.KnowledgeArchivedByUser)
	assert.Equal(t, "active/", d.Status+"/"+d.ArchivedBy)
	knowledge.SetArchivedState(d, models.KnowledgeStatusArchived, models.KnowledgeArchivedByImport)
	assert.Equal(t, "archived/import", d.Status+"/"+d.ArchivedBy)
	// same status: the "who" does not change (a person editing an import-archived document)
	knowledge.SetArchivedState(d, models.KnowledgeStatusArchived, models.KnowledgeArchivedByUser)
	assert.Equal(t, "archived/import", d.Status+"/"+d.ArchivedBy)
	// empty status: nothing changes
	knowledge.SetArchivedState(d, "", models.KnowledgeArchivedByUser)
	assert.Equal(t, "archived/import", d.Status+"/"+d.ArchivedBy)
}

func TestArchivedBy_IsAClosedListInTheDatabase(t *testing.T) {
	db, org, _ := setup(t)
	d := add(t, db, org, nil, nil, "Doc", "texto qualquer do documento")
	assert.Error(t, db.Model(d).Update("archived_by", "robot").Error)
	assert.NoError(t, db.Model(d).Update("archived_by", "user").Error)
}

// ---- M4: import target validation ------------------------------------------

func TestValidateImportTarget(t *testing.T) {
	db, org, _ := setup(t)
	other := testutil.CreateTestOrganization(t, db).ID
	unit := models.Unit{OrganizationID: org, Name: "U" + uuid.NewString()[:6], Active: true}
	foreign := models.Unit{OrganizationID: other, Name: "F" + uuid.NewString()[:6], Active: true}
	dept := models.Department{OrganizationID: org, Name: "D" + uuid.NewString()[:6], Active: true}
	require.NoError(t, db.Create(&unit).Error)
	require.NoError(t, db.Create(&foreign).Error)
	require.NoError(t, db.Create(&dept).Error)
	missing := uuid.New()

	assert.NoError(t, knowledge.ValidateImportTarget(db, org, nil, nil))
	assert.NoError(t, knowledge.ValidateImportTarget(db, org, &unit.ID, &dept.ID))
	assert.Error(t, knowledge.ValidateImportTarget(db, missing, nil, nil), "organization does not exist")
	assert.Error(t, knowledge.ValidateImportTarget(db, org, &foreign.ID, nil), "unit of another organization")
	assert.Error(t, knowledge.ValidateImportTarget(db, org, &missing, nil), "unit does not exist")
	assert.Error(t, knowledge.ValidateImportTarget(db, org, nil, &missing), "department does not exist")
}

// ---- M5: anchors and synchronization ---------------------------------------

func anchors(t *testing.T, html string) []string {
	t.Helper()
	docs, err := knowledge.ParseManualHTML(strings.NewReader(html))
	require.NoError(t, err)
	var out []string
	for _, d := range docs {
		out = append(out, d.Anchor)
	}
	return out
}

func sec(title, text string) string { return "<h2>" + title + "</h2><p>" + text + "</p>" }

const filler = " texto longo o bastante para virar um documento do conhecimento."

func TestAnchors_AreStableWhenASectionIsInsertedOrRemoved(t *testing.T) {
	a := sec("Como atender", "atender"+filler) + sec("Fila de espera", "fila"+filler)
	b := sec("Novidade", "nova"+filler) + a // inserted BEFORE the others
	assert.Equal(t, []string{"como-atender", "fila-de-espera"}, anchors(t, a))
	assert.Equal(t, []string{"novidade", "como-atender", "fila-de-espera"}, anchors(t, b))
}

func TestAnchors_PreferIdsAndSuffixOnlyBetweenDuplicates(t *testing.T) {
	html := `<section id="s"><h2>Um</h2><p>um` + filler + `</p><h3>Dois</h3><p>dois` + filler + `</p></section>` +
		`<h2 id="meu-id">Tres</h2><p>tres` + filler + `</p>` +
		`<h2>Repetido</h2><p>a` + filler + `</p><h2>Repetido</h2><p>b` + filler + `</p>`
	// the section id goes to the first heading only; the second falls back to its slug
	assert.Equal(t, []string{"s", "dois", "meu-id", "repetido", "repetido-2"}, anchors(t, html))
	// text before the first heading
	assert.Equal(t, []string{"introducao", "um"}, anchors(t, "<p>conteudo antes do primeiro titulo"+filler+"</p>"+sec("Um", "um"+filler)))
}

type manualDir struct {
	t    *testing.T
	dir  string
	db   *gorm.DB
	org  uuid.UUID
	r    knowledge.LexicalRetriever
	file string
}

func newManualDir(t *testing.T) *manualDir {
	db, org, r := setup(t)
	return &manualDir{t: t, dir: t.TempDir(), db: db, org: org, r: r, file: "guia.html"}
}

func (m *manualDir) write(sections ...string) {
	m.t.Helper()
	html := "<html><body>" + strings.Join(sections, "") + "</body></html>"
	require.NoError(m.t, os.WriteFile(filepath.Join(m.dir, m.file), []byte(html), 0o600))
}

func (m *manualDir) run(dry bool) knowledge.ImportResult {
	m.t.Helper()
	res, err := knowledge.ImportManuals(m.db, m.org, nil, nil, m.dir, dry)
	require.NoError(m.t, err)
	return res
}

func (m *manualDir) doc(anchor string) models.KnowledgeDocument {
	m.t.Helper()
	var d models.KnowledgeDocument
	require.NoError(m.t, m.db.First(&d, "organization_id = ? AND origin = ?", m.org, "manual/"+m.file+"#"+anchor).Error)
	return d
}

func TestImport_ArchivesVanishedSectionsAndReactivatesOnlyOwnArchives(t *testing.T) {
	m := newManualDir(t)
	a, b, c := sec("Alfa", "alfa"+filler), sec("Beta", "beta"+filler), sec("Gama", "gama"+filler)
	m.write(a, b, c)
	assert.Equal(t, 3, m.run(false).Created)

	// Beta leaves the HTML: archived by the import, no longer searchable
	m.write(a, c)
	res := m.run(false)
	assert.Equal(t, 1, res.Archived)
	assert.Equal(t, []string{"manual/guia.html#beta"}, res.ArchivedOrigins)
	d := m.doc("beta")
	assert.Equal(t, models.KnowledgeStatusArchived, d.Status)
	assert.Equal(t, models.KnowledgeArchivedByImport, d.ArchivedBy)
	assert.Empty(t, search(t, m.r, m.org, nil, nil, "beta", 5))
	assert.Equal(t, 0, m.run(false).Archived, "idempotent")

	// Beta comes back: reactivated, searchable again
	m.write(a, b, c)
	res = m.run(false)
	assert.Equal(t, 1, res.Reactivated)
	d = m.doc("beta")
	assert.Equal(t, models.KnowledgeStatusActive, d.Status)
	assert.Equal(t, models.KnowledgeArchivedByNone, d.ArchivedBy)
	assert.NotEmpty(t, search(t, m.r, m.org, nil, nil, "beta", 5))

	// a person archives Gama; its section stays in the HTML: it stays archived
	g := m.doc("gama")
	setStatus(t, m.db, &g, models.KnowledgeStatusArchived)
	res = m.run(false)
	assert.Equal(t, 0, res.Reactivated)
	g = m.doc("gama")
	assert.Equal(t, models.KnowledgeStatusArchived, g.Status)
	assert.Equal(t, models.KnowledgeArchivedByUser, g.ArchivedBy)
	// ...and it also stays archived if the section leaves and returns
	m.write(a, b)
	m.run(false)
	m.write(a, b, c)
	m.run(false)
	assert.Equal(t, models.KnowledgeStatusArchived, m.doc("gama").Status)
	assert.Equal(t, models.KnowledgeArchivedByUser, m.doc("gama").ArchivedBy)
}

func TestImport_AFileWithNoSectionsArchivesNothing(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler), sec("Beta", "beta"+filler))
	m.run(false)

	require.NoError(t, os.WriteFile(filepath.Join(m.dir, m.file), []byte("<html><body></body></html>"), 0o600))
	res, err := knowledge.ImportManuals(m.db, m.org, nil, nil, m.dir, false)
	require.Error(t, err)
	assert.Equal(t, []string{"guia.html"}, res.Failed)
	assert.Equal(t, 0, res.Archived)
	assert.Equal(t, models.KnowledgeStatusActive, m.doc("alfa").Status)
	assert.Equal(t, models.KnowledgeStatusActive, m.doc("beta").Status)
}

func TestImport_DryRunWritesNothing(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler), sec("Beta", "beta"+filler))
	res := m.run(true)
	assert.Equal(t, 2, res.Created, "reports what it would do")
	var n int64
	m.db.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", m.org).Count(&n)
	assert.Zero(t, n)

	m.run(false)
	m.write(sec("Alfa", "alfa"+filler))
	res = m.run(true)
	assert.Equal(t, 1, res.Archived)
	assert.Equal(t, models.KnowledgeStatusActive, m.doc("beta").Status, "dry run did not archive")
}

func TestImport_NeverChangesTheScopeOrStatusOfAnExistingDocument(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler))
	m.run(false)

	// an administrator scopes Alfa to a unit through the API
	unit := uuid.New()
	d := m.doc("alfa")
	d.UnitID = &unit
	require.NoError(t, knowledge.ApplyScope(m.db, &d))

	// the HTML changes; the import runs with NO scope
	m.write(sec("Alfa", "alfa mudou completamente"+filler))
	res := m.run(false)
	assert.Equal(t, 1, res.Updated)
	d = m.doc("alfa")
	require.NotNil(t, d.UnitID, "the scope chosen through the API is kept")
	assert.Equal(t, unit, *d.UnitID)
	assert.Contains(t, d.Body, "mudou completamente")
	assert.NotEmpty(t, search(t, m.r, m.org, &unit, nil, "mudou", 5))
	assert.Empty(t, search(t, m.r, m.org, nil, nil, "mudou", 5))
}

func TestImport_UnitAppliesOnlyToNewDocuments(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler))
	m.run(false)
	m.write(sec("Alfa", "alfa"+filler), sec("Beta", "beta"+filler))
	unit := uuid.New()
	_, err := knowledge.ImportManuals(m.db, m.org, &unit, nil, m.dir, false)
	require.NoError(t, err)
	assert.Nil(t, m.doc("alfa").UnitID, "existing document keeps its scope")
	require.NotNil(t, m.doc("beta").UnitID, "the new one is scoped")
}

func TestImport_LegacyPositionalAnchorsAreArchived(t *testing.T) {
	m := newManualDir(t)
	legacy := models.KnowledgeDocument{OrganizationID: m.org, SourceType: models.KnowledgeSourceManualHTML,
		Title: "Antigo", Origin: "manual/guia.html#secao-3", Body: "conteudo antigo duplicado"}
	require.NoError(t, knowledge.Save(m.db, &legacy, knowledge.SectionsFor(legacy.SourceType, legacy.Title, legacy.Body)))
	m.write(sec("Alfa", "alfa"+filler))
	res := m.run(false)
	assert.Equal(t, 1, res.Created)
	assert.Equal(t, 1, res.Archived)
	assert.Equal(t, []string{"manual/guia.html#secao-3"}, res.ArchivedOrigins)
	assert.Empty(t, search(t, m.r, m.org, nil, nil, "duplicado", 5))
}

func TestImport_OnlyTouchesItsOwnFileAndOrganization(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler))
	m.run(false)
	// a document of another file and one of another organization are not orphans of this file
	other := models.KnowledgeDocument{OrganizationID: m.org, SourceType: models.KnowledgeSourceManualHTML,
		Title: "De outro arquivo", Origin: "manual/outro.html#x", Body: "texto de outro arquivo qualquer"}
	require.NoError(t, knowledge.Save(m.db, &other, knowledge.SectionsFor(other.SourceType, other.Title, other.Body)))
	otherOrg := testutil.CreateTestOrganization(t, m.db).ID
	foreign := models.KnowledgeDocument{OrganizationID: otherOrg, SourceType: models.KnowledgeSourceManualHTML,
		Title: "Outra org", Origin: "manual/guia.html#fantasma", Body: "texto de outra organizacao"}
	require.NoError(t, knowledge.Save(m.db, &foreign, knowledge.SectionsFor(foreign.SourceType, foreign.Title, foreign.Body)))

	res := m.run(false)
	assert.Equal(t, 0, res.Archived)
	var o, f models.KnowledgeDocument
	require.NoError(t, m.db.First(&o, "id = ?", other.ID).Error)
	require.NoError(t, m.db.First(&f, "id = ?", foreign.ID).Error)
	assert.Equal(t, models.KnowledgeStatusActive, o.Status)
	assert.Equal(t, models.KnowledgeStatusActive, f.Status)
}

// ---- M6: concurrency --------------------------------------------------------

func TestImport_ConcurrentImportsOfTheSameManualDoNotFail(t *testing.T) {
	m := newManualDir(t)
	m.write(sec("Alfa", "alfa"+filler), sec("Beta", "beta"+filler), sec("Gama", "gama"+filler))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := knowledge.ImportManuals(m.db, m.org, nil, nil, m.dir, false)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	var n int64
	m.db.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", m.org).Count(&n)
	assert.EqualValues(t, 3, n, "one document per section, no duplicates")
	var chunks int64
	m.db.Model(&models.KnowledgeChunk{}).Where("organization_id = ?", m.org).Count(&chunks)
	assert.EqualValues(t, 3, chunks)
}

func TestSave_ConcurrentRebuildsSerializeOnTheLockedRow(t *testing.T) {
	db, org, _ := setup(t)
	d := add(t, db, org, nil, nil, "Doc", strings.Repeat("palavra comum do texto. ", 120))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Transaction(func(tx *gorm.DB) error {
				doc, err := knowledge.Lock(tx, org, d.ID)
				if err != nil {
					return err
				}
				doc.Body = strings.Repeat("palavra comum do texto. ", 100+i*7)
				time.Sleep(10 * time.Millisecond) // widen the window a race would need
				return knowledge.Save(tx, doc, knowledge.SectionsFor(doc.SourceType, doc.Title, doc.Body))
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	var chunks []models.KnowledgeChunk
	require.NoError(t, db.Where("document_id = ?", d.ID).Order("chunk_index").Find(&chunks).Error)
	var final models.KnowledgeDocument
	require.NoError(t, db.First(&final, "id = ?", d.ID).Error)
	want := knowledge.Chunk(knowledge.SectionsFor(final.SourceType, final.Title, final.Body))
	require.Len(t, chunks, len(want), "the chunks belong to ONE version of the body")
	for i, c := range chunks {
		assert.Equal(t, i, c.ChunkIndex)
		assert.Equal(t, want[i].Content, c.Content)
	}
}

// ---- M1 regressions found by the final audit -------------------------------

// The string forms: compounds are never turned into operators, quoted phrases stay
// separate, and the legitimate operators are untouched.
func TestSearchForm_CompoundsPhrasesAndOperatorsKeepTheirMeaning(t *testing.T) {
	cases := map[string]string{
		// a hyphen between letters is part of the word, stop-word parts included
		"não-conformidade":  "nao-conformidade",
		"nao-conformidade":  "nao-conformidade",
		"Não-Conformidade":  "nao-conformidade",
		"só-leitura":        "so-leitura",
		"já-existente":      "ja-existente",
		"foo-não":           "foo-nao",
		`"não-conformidade"`: `"nao-conformidade"`,
		// separate phrases stay separate
		`"emitir nota" "nota fiscal"`:      `"emitir nota" "nota fiscal"`,
		`"emitir nota"`:                    `"emitir nota"`,
		`"emitir nota" -"nota fiscal"`:     `"emitir nota" -"nota fiscal"`,
		`"emitir nota" -garantia`:          `"emitir nota" -garantia`,
		`"emitir não" -"garantia não"`:     `"emitir" -"garantia"`,
		`"não" "emitir nota"`:              `"emitir nota"`,
		`"emitir nota" "não"`:              `"emitir nota"`,
		// exclusions
		"foo -bar":    "foo -bar",
		`-"garantia"`: `-"garantia"`,
		"-não foo":    "foo",
		"foo -não":    "foo",
		// OR
		"foo OR não":        "foo",
		"não OR foo":        "foo",
		"foo or or bar":     "foo or bar",
		"foo OR não OR bar": "foo or bar",
		// leftovers
		"-": "", "- foo": "foo", "(não) foo": "foo", "não, foo.": "foo.", `"""`: "", "não não não": "",
		`unterminado "emitir nota`: "unterminado emitir nota",
	}
	for in, want := range cases {
		assert.Equal(t, want, knowledge.SearchForm(in), in)
	}
}

// What PostgreSQL does with the prepared text: the semantics, not the spelling.
func TestSearchForm_TsqueryHasTheRightSemanticsInPostgres(t *testing.T) {
	db := testutil.SetupTestDB(t)
	tsq := func(q string) string {
		var out string
		require.NoError(t, db.Raw(`SELECT websearch_to_tsquery('portuguese', ?)::text`, knowledge.SearchForm(q)).Scan(&out).Error)
		return out
	}
	// the text as the indexer prepares it, matched against the query as the search prepares it
	matches := func(doc, q string) bool {
		var ok bool
		require.NoError(t, db.Raw(`SELECT to_tsvector('portuguese', ?) @@ websearch_to_tsquery('portuguese', ?)`,
			knowledge.SearchForm(doc), knowledge.SearchForm(q)).Scan(&ok).Error)
		return ok
	}

	// MAJOR 1: a compound is never an exclusion
	for q, want := range map[string]string{
		"não-conformidade": "'nao-conform' <-> 'nao' <-> 'conform'",
		"nao-conformidade": "'nao-conform' <-> 'nao' <-> 'conform'",
		"só-leitura":       "'so-leitur' <-> 'so' <-> 'leitur'",
		"já-existente":     "'ja-existent' <-> 'ja' <-> 'existent'",
	} {
		got := tsq(q)
		assert.Equal(t, want, got, q)
		assert.NotContains(t, got, "!", "%q must not become an exclusion", q)
	}
	assert.NotEqual(t, "!'conform'", tsq("não-conformidade"))
	assert.True(t, matches("O relatório registrou a não-conformidade do lote.", "não-conformidade"))
	assert.False(t, matches("A política de troca segue a regra geral da loja.", "não-conformidade"),
		"the old behavior matched every text WITHOUT the word")
	assert.False(t, matches("A conformidade foi restabelecida.", "não-conformidade"))

	// MAJOR 2: two phrases stay two independent phrases
	two := tsq(`"emitir nota" "nota fiscal"`)
	assert.Equal(t, "'emit' <-> 'not' & 'not' <-> 'fiscal'", two)
	assert.NotEqual(t, "'emit' <-> 'not' <-> 'not' <-> 'fiscal'", two, "not one fused phrase")
	doc := "Para emitir nota basta clicar no botão. Depois a nota fiscal é enviada ao cliente."
	assert.True(t, matches(doc, `"emitir nota" "nota fiscal"`))
	assert.False(t, matches("Para emitir nota basta clicar no botão.", `"emitir nota" "nota fiscal"`), "both phrases are required")
	assert.False(t, matches("A nota fiscal é enviada ao cliente.", `"emitir nota" "nota fiscal"`))

	// phrase + excluded phrase
	assert.Equal(t, "'emit' <-> 'not' & !( 'not' <-> 'fiscal' )", tsq(`"emitir nota" -"nota fiscal"`))
	assert.False(t, matches(doc, `"emitir nota" -"nota fiscal"`), "the excluded phrase removes it")
	assert.True(t, matches("Para emitir nota basta clicar no botão.", `"emitir nota" -"nota fiscal"`))

	// stop words inside phrases and exclusions
	assert.Equal(t, "'emit' & !'garant'", tsq(`"emitir não" -"garantia não"`))
	assert.True(t, matches("Você pode emitir a nota hoje", `"emitir não" -"garantia não"`))
	assert.False(t, matches("Emitir sem garantia é possível", `"emitir não" -"garantia não"`))

	// exclusions and OR are untouched
	assert.Equal(t, "'foo' & !'bar'", tsq("foo -bar"))
	assert.Equal(t, "!'garant'", tsq(`-"garantia"`))
	assert.Equal(t, "'foo'", tsq("foo OR não"))
	assert.Equal(t, "'foo'", tsq("não OR foo"))
	assert.Equal(t, "'foo' | 'bar'", tsq("foo or or bar"))
	assert.Equal(t, "'foo'", tsq("-não foo"))
}

// End to end through the retriever: the same two cases, against real chunks.
func TestSearch_CompoundsAndPhrasesThroughTheRetriever(t *testing.T) {
	db, org, r := setup(t)
	add(t, db, org, nil, nil, "Relatório de qualidade", "A não-conformidade encontrada no lote foi registrada.")
	add(t, db, org, nil, nil, "Política de troca", "Trocas seguem a política geral da loja, sem relação com auditorias.")
	add(t, db, org, nil, nil, "Emissão", "Para emitir nota basta clicar no botão. Depois a nota fiscal é enviada ao cliente.")
	add(t, db, org, nil, nil, "Só emissão", "Para emitir nota basta clicar no botão e conferir os dados.")

	assert.Equal(t, []string{"Relatório de qualidade"}, titles(search(t, r, org, nil, nil, "não-conformidade", 10)),
		"only the text that has the compound, never the ones that lack it")
	assert.Equal(t, []string{"Relatório de qualidade"}, titles(search(t, r, org, nil, nil, "nao-conformidade", 10)))
	assert.Equal(t, []string{"Emissão"}, titles(search(t, r, org, nil, nil, `"emitir nota" "nota fiscal"`, 10)))
	assert.Equal(t, []string{"Só emissão"}, titles(search(t, r, org, nil, nil, `"emitir nota" -"nota fiscal"`, 10)))

	// one rule for both sides: what is indexed is exactly SearchForm of the stored text
	var chunks []models.KnowledgeChunk
	require.NoError(t, db.Where("organization_id = ?", org).Find(&chunks).Error)
	require.NotEmpty(t, chunks)
	for _, c := range chunks {
		assert.Equal(t, knowledge.SearchForm(c.Content), c.SearchText)
		assert.Equal(t, knowledge.SearchForm(c.Heading), c.SearchHeading)
	}
}
