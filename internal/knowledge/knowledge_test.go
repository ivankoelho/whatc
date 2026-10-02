package knowledge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---- pure: normalization, chunking, markdown, html -------------------------

func TestFoldForSearch_AccentsCaseAndSyntax(t *testing.T) {
	assert.Equal(t, "orcamento da ocorrencia", knowledge.FoldForSearch("  ORÇAMENTO   da Ocorrência "))
	// websearch syntax survives
	assert.Equal(t, `"troca de produto" -garantia`, knowledge.FoldForSearch(`"Troca de Produto" -Garantia`))
}

func TestNormalizeText(t *testing.T) {
	got := knowledge.NormalizeText("a\r\nb\u0000  c\t\td\n\n\n\n e   f\n")
	assert.Equal(t, "a\nb c d\n\ne f", got)
}

func TestChunk_ShortTextIsOneChunk(t *testing.T) {
	p := knowledge.Chunk([]knowledge.Section{{Heading: "H", Text: "Texto curto."}})
	require.Len(t, p, 1)
	assert.Equal(t, 0, p[0].Index)
	assert.Equal(t, "H", p[0].Heading)
}

func TestChunk_LongTextRespectsLimitOverlapAndWords(t *testing.T) {
	var paras []string
	for i := 0; i < 60; i++ {
		paras = append(paras, "palavra numero "+strings.Repeat("abc ", 25)+"fim.")
	}
	text := strings.Join(paras, "\n")
	a := knowledge.Chunk([]knowledge.Section{{Heading: "H", Text: text}})
	b := knowledge.Chunk([]knowledge.Section{{Heading: "H", Text: text}})
	assert.Equal(t, a, b, "chunking is deterministic")
	require.Greater(t, len(a), 3)

	words := map[string]bool{}
	for _, w := range strings.Fields(text) {
		words[w] = true
	}
	for i, c := range a {
		assert.Equal(t, i, c.Index)
		assert.LessOrEqual(t, utf8.RuneCountInString(c.Content), 1000)
		for _, w := range strings.Fields(c.Content) {
			assert.True(t, words[w], "chunk %d cut a word: %q", i, w)
		}
		if i > 0 { // overlap: the start repeats the tail of the previous chunk
			first := strings.Fields(c.Content)[0]
			assert.Contains(t, a[i-1].Content, first)
		}
	}
}

func TestChunk_OneHugeWordIsKeptWhole(t *testing.T) {
	w := strings.Repeat("x", 3000)
	p := knowledge.Chunk([]knowledge.Section{{Text: w}})
	require.NotEmpty(t, p)
	assert.Equal(t, w, p[0].Content)
}

func TestParseMarkdown_HeadingTrailAndMarkupStripped(t *testing.T) {
	md := "Intro **solta**\n\n# Processo\nTexto do [processo](http://x).\n## Etapa 2\n- passo `um`\n```\n# nao e titulo\n```\n"
	s := knowledge.ParseMarkdown(md, "Meu Doc")
	require.Len(t, s, 3)
	assert.Equal(t, "Meu Doc", s[0].Heading)
	assert.Equal(t, "Intro solta", s[0].Text)
	assert.Equal(t, "Processo", s[1].Heading)
	assert.Equal(t, "Texto do processo.", s[1].Text)
	assert.Equal(t, "Processo > Etapa 2", s[2].Heading)
	assert.Contains(t, s[2].Text, "passo um")
	assert.NotContains(t, s[2].Text, "```")
}

const manualFixture = `<html><head><style>.x{}</style><script>var a=1</script></head><body>
<nav>Menu lateral</nav><h1>Titulo</h1>
<section id="tela"><h2>A tela de conversas</h2><p>Texto da tela de conversas com informações suficientes para virar documento.</p>
<img src="data:image/png;base64,AAAAAAAA"><script>alert(1)</script></section>
<h2>Fila</h2>
<h3 id="fila-a">1.1 Assumir da fila</h3><p>Para assumir da fila o agente abre a aba e clica em assumir conversa agora.</p><ul><li>Primeiro item da lista</li></ul>
</body></html>`

func TestParseManualHTML(t *testing.T) {
	docs, err := knowledge.ParseManualHTML(strings.NewReader(manualFixture))
	require.NoError(t, err)
	require.Len(t, docs, 2, "tiny sections are dropped")
	assert.Equal(t, "A tela de conversas", docs[0].Title)
	assert.Equal(t, "tela", docs[0].Anchor, "anchor comes from the enclosing section")
	assert.Equal(t, "1.1 Assumir da fila", docs[1].Title)
	assert.Equal(t, "fila-a", docs[1].Anchor)
	all := docs[0].Text + docs[1].Text
	for _, bad := range []string{"base64", "alert", "var a", "Menu lateral", "data:image"} {
		assert.NotContains(t, all, bad)
	}
	assert.Contains(t, docs[1].Text, "Primeiro item da lista")
}

// ---- database: FTS, scope, storage, import ---------------------------------

func setup(t *testing.T) (*gorm.DB, uuid.UUID, knowledge.LexicalRetriever) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	return db, org.ID, knowledge.LexicalRetriever{DB: db}
}

func add(t *testing.T, db *gorm.DB, org uuid.UUID, unit, dept *uuid.UUID, title, body string) *models.KnowledgeDocument {
	t.Helper()
	d := &models.KnowledgeDocument{OrganizationID: org, UnitID: unit, DepartmentID: dept,
		SourceType: models.KnowledgeSourceArticle, Title: title, Body: body}
	require.NoError(t, knowledge.Save(db, d, knowledge.SectionsFor(d.SourceType, title, body)))
	return d
}

func search(t *testing.T, r knowledge.Retriever, org uuid.UUID, unit, dept *uuid.UUID, q string, limit int) []knowledge.Hit {
	t.Helper()
	hits, err := knowledge.Service{Retriever: r}.Search(context.Background(),
		knowledge.Query{OrgID: org, Text: q, UnitID: unit, DepartmentID: dept, Limit: limit})
	require.NoError(t, err)
	return hits
}

func titles(h []knowledge.Hit) []string {
	var out []string
	for _, x := range h {
		out = append(out, x.Title)
	}
	return out
}

func TestFTS_AccentsFlexionAndSyntax(t *testing.T) {
	db, org, r := setup(t)
	add(t, db, org, nil, nil, "Como emitir orçamento", "O vendedor emite o orçamento no sistema e envia ao cliente. As ocorrências ficam registradas.")
	add(t, db, org, nil, nil, "Garantia", "Produto em garantia segue outro processo de troca.")

	for _, q := range []string{"orcamento", "ORÇAMENTO", "orçamentos", "ocorrencia"} {
		assert.Equal(t, []string{"Como emitir orçamento"}, titles(search(t, r, org, nil, nil, q, 5)), q)
	}
	// websearch exclusion: "troca" matches Garantia, excluding "garantia" removes it
	assert.Len(t, search(t, r, org, nil, nil, "troca", 5), 1)
	assert.Empty(t, search(t, r, org, nil, nil, "troca -garantia", 5))
	assert.Len(t, search(t, r, org, nil, nil, `"processo de troca"`, 5), 1)

	h := search(t, r, org, nil, nil, "orcamento", 5)
	assert.Equal(t, "Como emitir orçamento", h[0].Title, "display text keeps its accents")
	assert.Contains(t, h[0].Content, "orçamento")
	assert.Equal(t, models.KnowledgeSourceArticle, h[0].Citation.SourceType)
	assert.Greater(t, h[0].Score, 0.0)
}

func TestFTS_EmptyAndStopWordQueries(t *testing.T) {
	db, org, r := setup(t)
	add(t, db, org, nil, nil, "Doc", "alguma coisa qualquer sobre o assunto")
	_, err := knowledge.Service{Retriever: r}.Search(context.Background(), knowledge.Query{OrgID: org, Text: "   "})
	assert.ErrorIs(t, err, knowledge.ErrEmptyQuery)
	assert.Empty(t, search(t, r, org, nil, nil, "de a o", 5), "only stop-words: nothing, no error")
}

func TestFTS_TitleWeighsMoreThanBody(t *testing.T) {
	db, org, r := setup(t)
	add(t, db, org, nil, nil, "Assunto diverso", "Quando houver troca de produto, registre a troca no sistema.")
	add(t, db, org, nil, nil, "Política de troca", "Texto sobre regras gerais do atendimento, com a troca citada uma vez.")
	h := search(t, r, org, nil, nil, "troca", 5)
	require.Len(t, h, 2)
	assert.Equal(t, "Política de troca", h[0].Title)
	assert.GreaterOrEqual(t, h[0].Score, h[1].Score)
}

func TestScope_UnitDepartmentGlobalAndOrganization(t *testing.T) {
	db, org, r := setup(t)
	u1, u2, d1, d2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	add(t, db, org, nil, nil, "Global", "regra de reembolso valida para todos")
	add(t, db, org, &u1, nil, "Unidade 1", "regra de reembolso da unidade um")
	add(t, db, org, &u2, nil, "Unidade 2", "regra de reembolso da unidade dois")
	add(t, db, org, nil, &d1, "Depto 1", "regra de reembolso do departamento um")
	add(t, db, org, &u1, &d1, "Unidade 1 e Depto 1", "regra de reembolso da unidade um no departamento um")

	set := func(h []knowledge.Hit) map[string]bool {
		m := map[string]bool{}
		for _, x := range h {
			m[x.Title] = true
		}
		return m
	}
	// no context: global only
	assert.Equal(t, map[string]bool{"Global": true}, set(search(t, r, org, nil, nil, "reembolso", 20)))
	// unit 1 without department: global + unit 1, never the department-scoped ones
	assert.Equal(t, map[string]bool{"Global": true, "Unidade 1": true}, set(search(t, r, org, &u1, nil, "reembolso", 20)))
	// department 1 without unit: global + dept 1, not the (unit 1 + dept 1) one
	assert.Equal(t, map[string]bool{"Global": true, "Depto 1": true}, set(search(t, r, org, nil, &d1, "reembolso", 20)))
	// unit 1 + dept 1
	assert.Equal(t, map[string]bool{"Global": true, "Unidade 1": true, "Depto 1": true, "Unidade 1 e Depto 1": true},
		set(search(t, r, org, &u1, &d1, "reembolso", 20)))
	// unit 2 + other department
	assert.Equal(t, map[string]bool{"Global": true, "Unidade 2": true}, set(search(t, r, org, &u2, &d2, "reembolso", 20)))
}

func TestScope_FilterRunsBeforeLimit(t *testing.T) {
	db, org, r := setup(t)
	other := uuid.New()
	// Two documents the asker may NOT see, far better ranked than the global one.
	add(t, db, org, &other, nil, "reembolso reembolso", "reembolso reembolso reembolso reembolso")
	add(t, db, org, &other, nil, "reembolso urgente", "reembolso reembolso reembolso")
	add(t, db, org, nil, nil, "Regras gerais", "uma menção ao reembolso no meio do texto")
	h := search(t, r, org, nil, nil, "reembolso", 1)
	require.Len(t, h, 1, "an invisible better chunk must not consume the LIMIT")
	assert.Equal(t, "Regras gerais", h[0].Title)
}

func TestScope_OtherOrganizationNeverAppears(t *testing.T) {
	db, org, r := setup(t)
	otherOrg := testutil.CreateTestOrganization(t, db).ID
	add(t, db, otherOrg, nil, nil, "Segredo", "procedimento confidencial de cofre")
	assert.Empty(t, search(t, r, org, nil, nil, "cofre", 5))
	assert.Len(t, search(t, r, otherOrg, nil, nil, "cofre", 5), 1)
}

func TestStore_ScopeStatusAndDeleteFollowTheDocument(t *testing.T) {
	db, org, r := setup(t)
	d := add(t, db, org, nil, nil, "Doc mutavel", "texto sobre fornecedores homologados")
	assert.Len(t, search(t, r, org, nil, nil, "fornecedores", 5), 1)

	// scope change: chunks are re-copied, the doc now needs its unit
	unit := uuid.New()
	d.UnitID = &unit
	require.NoError(t, knowledge.Save(db, d, knowledge.SectionsFor(d.SourceType, d.Title, d.Body)))
	assert.Equal(t, models.KnowledgeVisibilityUnit, d.Visibility)
	assert.Empty(t, search(t, r, org, nil, nil, "fornecedores", 5))
	assert.Len(t, search(t, r, org, &unit, nil, "fornecedores", 5), 1)
	var c models.KnowledgeChunk
	require.NoError(t, db.First(&c, "document_id = ?", d.ID).Error)
	assert.Equal(t, models.KnowledgeVisibilityUnit, c.Visibility)
	require.NotNil(t, c.UnitID)

	// archive: out of search, kept; reactivate: back
	require.NoError(t, knowledge.SetStatus(db, d, models.KnowledgeStatusArchived))
	assert.Empty(t, search(t, r, org, &unit, nil, "fornecedores", 5))
	require.NoError(t, knowledge.SetStatus(db, d, models.KnowledgeStatusActive))
	assert.Len(t, search(t, r, org, &unit, nil, "fornecedores", 5), 1)

	// delete: gone from search and no chunks left
	require.NoError(t, knowledge.Remove(db, d))
	assert.Empty(t, search(t, r, org, &unit, nil, "fornecedores", 5))
	var n int64
	db.Model(&models.KnowledgeChunk{}).Where("document_id = ?", d.ID).Count(&n)
	assert.Zero(t, n)
}

func TestStore_EditReplacesChunksAtomically(t *testing.T) {
	db, org, r := setup(t)
	d := add(t, db, org, nil, nil, "Doc", "conteudo antigo sobre paletes")
	d.Body = "conteudo novo sobre containers"
	require.NoError(t, knowledge.Save(db, d, knowledge.SectionsFor(d.SourceType, d.Title, d.Body)))
	assert.Empty(t, search(t, r, org, nil, nil, "paletes", 5))
	assert.Len(t, search(t, r, org, nil, nil, "containers", 5), 1)
	var n int64
	db.Model(&models.KnowledgeChunk{}).Where("document_id = ?", d.ID).Count(&n)
	assert.EqualValues(t, 1, n)
}

type fakeRetriever struct{ got knowledge.Query }

func (f *fakeRetriever) Retrieve(_ context.Context, q knowledge.Query) ([]knowledge.Hit, error) {
	f.got = q
	return []knowledge.Hit{{Title: "fake"}}, nil
}

func TestService_DependsOnlyOnTheRetrieverContract(t *testing.T) {
	f := &fakeRetriever{}
	h, err := knowledge.Service{Retriever: f}.Search(context.Background(), knowledge.Query{Text: " x ", Limit: 999})
	require.NoError(t, err)
	assert.Equal(t, "fake", h[0].Title)
	assert.Equal(t, "x", f.got.Text)
	assert.Equal(t, knowledge.MaxLimit, f.got.Limit)
	_, _ = knowledge.Service{Retriever: f}.Search(context.Background(), knowledge.Query{Text: "x"})
	assert.Equal(t, knowledge.DefaultLimit, f.got.Limit)
}

func TestImportManuals_IdempotentAndUpdates(t *testing.T) {
	db, org, r := setup(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "guia.html")
	require.NoError(t, os.WriteFile(file, []byte(manualFixture), 0o600))

	res, err := knowledge.ImportManuals(db, org, nil, nil, dir)
	require.NoError(t, err)
	assert.Equal(t, knowledge.ImportResult{Created: 2}, res)

	h := search(t, r, org, nil, nil, "assumir fila", 5)
	require.NotEmpty(t, h)
	assert.Equal(t, "manual/guia.html#fila-a", h[0].Citation.Origin)
	assert.Equal(t, "fila-a", h[0].Citation.Anchor)

	res, err = knowledge.ImportManuals(db, org, nil, nil, dir)
	require.NoError(t, err)
	assert.Equal(t, knowledge.ImportResult{Unchanged: 2}, res, "same content: nothing rewritten")

	changed := strings.Replace(manualFixture, "clica em assumir conversa agora", "usa o botão pegar atendimento", 1)
	require.NoError(t, os.WriteFile(file, []byte(changed), 0o600))
	res, err = knowledge.ImportManuals(db, org, nil, nil, dir)
	require.NoError(t, err)
	assert.Equal(t, knowledge.ImportResult{Updated: 1, Unchanged: 1}, res)
	assert.NotEmpty(t, search(t, r, org, nil, nil, "pegar atendimento", 5))
	var n int64
	db.Model(&models.KnowledgeDocument{}).Where("organization_id = ?", org).Count(&n)
	assert.EqualValues(t, 2, n, "no duplicates")
}

func TestImportManuals_ScopedToAUnit(t *testing.T) {
	db, org, r := setup(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "g.html"), []byte(manualFixture), 0o600))
	unit := uuid.New()
	_, err := knowledge.ImportManuals(db, org, &unit, nil, dir)
	require.NoError(t, err)
	assert.Empty(t, search(t, r, org, nil, nil, "assumir fila", 5))
	assert.NotEmpty(t, search(t, r, org, &unit, nil, "assumir fila", 5))
}

// The two real manuals: sections are extracted, no embedded image or script leaks in.
func TestImportManuals_RealManuals(t *testing.T) {
	dir := filepath.Join("..", "..", "manuais")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("manuais/ not present")
	}
	db, org, r := setup(t)
	res, err := knowledge.ImportManuals(db, org, nil, nil, dir)
	require.NoError(t, err)
	t.Logf("imported %d sections", res.Created)
	assert.Greater(t, res.Created, 10)

	var docs []models.KnowledgeDocument
	require.NoError(t, db.Where("organization_id = ?", org).Find(&docs).Error)
	for _, d := range docs {
		assert.NotContains(t, d.Body, "data:image", d.Origin)
		assert.NotContains(t, d.Body, "base64,", d.Origin)
		assert.True(t, strings.HasPrefix(d.Origin, "manual/"))
		assert.LessOrEqual(t, len(d.Body), 400_000)
	}
	assert.NotEmpty(t, search(t, r, org, nil, nil, "visibilidade de conversas", 5))
}
