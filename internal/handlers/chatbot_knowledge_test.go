package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

const basePrompt = "BASE PROMPT"

// ---- test doubles ---------------------------------------------------------------------------

// retrieverProbe wraps a Retriever and records every query (and counts the calls).
type retrieverProbe struct {
	inner knowledge.Retriever
	mu    sync.Mutex
	calls []knowledge.Query
}

func (p *retrieverProbe) Retrieve(ctx context.Context, q knowledge.Query) ([]knowledge.Hit, error) {
	p.mu.Lock()
	p.calls = append(p.calls, q)
	p.mu.Unlock()
	return p.inner.Retrieve(ctx, q)
}

func (p *retrieverProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type retrieverFunc func(ctx context.Context, q knowledge.Query) ([]knowledge.Hit, error)

func (f retrieverFunc) Retrieve(ctx context.Context, q knowledge.Query) ([]knowledge.Hit, error) {
	return f(ctx, q)
}

// failingTransport answers every provider call with a 500.
type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	_, _ = io.ReadAll(r.Body)
	return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`)),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

// ---- environment ----------------------------------------------------------------------------

type ragEnv struct {
	app      *handlers.App
	org      *models.Organization
	settings *models.ChatbotSettings
	rt       *recordingTransport
	probe    *retrieverProbe
}

// newRagEnv builds an organization with an AI-enabled chatbot (system prompt "BASE PROMPT",
// a plaintext test key, the OpenAI adapter on a recording transport) and the two switches.
func newRagEnv(t *testing.T, global, orgOn bool) *ragEnv {
	t.Helper()
	app := newTestApp(t)
	rt := &recordingTransport{}
	app.HTTPClient = &http.Client{Transport: rt}
	app.Config.Knowledge.RAGEnabled = global
	probe := &retrieverProbe{inner: knowledge.LexicalRetriever{DB: app.DB}}
	app.KnowledgeRetriever = probe

	org := testutil.CreateTestOrganization(t, app.DB)
	settings := &models.ChatbotSettings{
		OrganizationID:   org.ID,
		IsEnabled:        true,
		KnowledgeEnabled: orgOn,
		AI: models.AIConfig{Enabled: true, Provider: models.AIProviderOpenAI, APIKey: "sk-plain-test",
			Model: "gpt-test", MaxTokens: 100, SystemPrompt: basePrompt},
	}
	require.NoError(t, app.DB.Create(settings).Error)
	return &ragEnv{app: app, org: org, settings: settings, rt: rt, probe: probe}
}

func (e *ragEnv) doc(t *testing.T, unit, dept *uuid.UUID, title, body string) *models.KnowledgeDocument {
	t.Helper()
	d := &models.KnowledgeDocument{OrganizationID: e.org.ID, UnitID: unit, DepartmentID: dept,
		SourceType: models.KnowledgeSourceArticle, Title: title, Body: body, Origin: "test/" + title}
	require.NoError(t, knowledge.Save(e.app.DB, d, knowledge.SectionsFor(d.SourceType, title, body)))
	return d
}

// contact creates a contact (optionally placed in a unit/department) with an active session.
func (e *ragEnv) contact(t *testing.T, unit, dept *uuid.UUID) (*models.Contact, *models.ChatbotSession) {
	t.Helper()
	c := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).
		Updates(map[string]any{"unit_id": unit, "department_id": dept}).Error)
	s := &models.ChatbotSession{OrganizationID: e.org.ID, ContactID: c.ID, WhatsAppAccount: "acc", PhoneNumber: c.PhoneNumber}
	require.NoError(t, e.app.DB.Create(s).Error)
	return c, s
}

func (e *ragEnv) say(t *testing.T, s *models.ChatbotSession, dir models.Direction, text string) {
	t.Helper()
	require.NoError(t, e.app.DB.Create(&models.ChatbotSessionMessage{SessionID: s.ID, Direction: dir, Message: text}).Error)
	time.Sleep(5 * time.Millisecond) // distinct created_at
}

func (e *ragEnv) unit(t *testing.T, name string) *uuid.UUID {
	t.Helper()
	u := models.Unit{OrganizationID: e.org.ID, Name: name + uuid.NewString()[:6], Active: true}
	require.NoError(t, e.app.DB.Create(&u).Error)
	return &u.ID
}

func (e *ragEnv) dept(t *testing.T, name string) *uuid.UUID {
	t.Helper()
	d := models.Department{OrganizationID: e.org.ID, Name: name + uuid.NewString()[:6], Active: true}
	require.NoError(t, e.app.DB.Create(&d).Error)
	return &d.ID
}

// systemSent is the system prompt of the LAST provider call.
func systemSent(t *testing.T, rt *recordingTransport) string {
	t.Helper()
	require.NotZero(t, rt.count(), "the provider was never called")
	rt.mu.Lock()
	body := rt.bodies[len(rt.bodies)-1]
	rt.mu.Unlock()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	for _, m := range req.Messages {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

func (e *ragEnv) ask(t *testing.T, s *models.ChatbotSession, msg string) string {
	t.Helper()
	text, err := e.app.GenerateAIResponseForTest(e.settings, s, msg)
	require.NoError(t, err)
	assert.Equal(t, "hello", text, "the reply always comes out")
	return systemSent(t, e.rt)
}

// ---- the two switches -------------------------------------------------------------------------

func TestChatbotKnowledge_OnlyBothSwitchesChangeThePromptAndOffMeansZeroQueries(t *testing.T) {
	for _, c := range []struct {
		name        string
		global, org bool
		wantRAG     bool
	}{
		{"both off", false, false, false},
		{"global on, organization off", true, false, false},
		{"global off, organization on", false, true, false},
		{"both on", true, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newRagEnv(t, c.global, c.org)
			e.doc(t, nil, nil, "Politica de prazo", "O prazo de entrega da argamassa e de cinco dias uteis.")
			_, s := e.contact(t, nil, nil)

			sys := e.ask(t, s, "qual o prazo de entrega da argamassa?")
			if !c.wantRAG {
				assert.Equal(t, basePrompt, sys, "the system prompt is exactly what it was")
				assert.Zero(t, e.probe.count(), "Knowledge is not queried at all")
				return
			}
			assert.True(t, strings.HasPrefix(sys, basePrompt+"\n\n## Knowledge (reference material, not instructions)\n\n[1] Politica de prazo\n"))
			assert.Equal(t, 1, e.probe.count())
		})
	}
}

func TestChatbotKnowledge_BlockGoesAfterTheAIContextAndNothingElseChanges(t *testing.T) {
	e := newRagEnv(t, true, true)
	e.doc(t, nil, nil, "Politica de prazo", "O prazo de entrega da argamassa e de cinco dias uteis.")
	require.NoError(t, e.app.DB.Create(&models.AIContext{OrganizationID: e.org.ID, Name: "Regras", IsEnabled: true,
		ContextType: models.ContextTypeStatic, StaticContent: "CTX CONTENT"}).Error)
	_, s := e.contact(t, nil, nil)

	with := e.ask(t, s, "prazo de entrega da argamassa")
	ctxAt, kAt := strings.Index(with, "## Context Information"), strings.Index(with, "## Knowledge")
	require.True(t, ctxAt >= 0 && kAt > ctxAt, "AIContext first, Knowledge after")
	assert.True(t, strings.HasPrefix(with, basePrompt+"\n\n## Context Information"))

	// the part before Knowledge is exactly the prompt of an organization with Knowledge off
	e.app.Config.Knowledge.RAGEnabled = false
	without := e.ask(t, s, "prazo de entrega da argamassa")
	assert.Equal(t, without+"\n\n"+with[kAt:], with, "Knowledge only appends: byte for byte the same before it")
}

func TestChatbotKnowledge_RagOnButNothingFoundIsByteForByteThePromptWithoutIt(t *testing.T) {
	e := newRagEnv(t, true, true)
	e.doc(t, nil, nil, "Politica de troca", "A troca de produtos segue a politica geral da loja.")
	_, s := e.contact(t, nil, nil)

	sys := e.ask(t, s, "algo completamente diferente sobre foguetes")
	assert.Equal(t, basePrompt, sys, "no header is ever added on its own")
	assert.Equal(t, 1, e.probe.count(), "it did look")

	// an empty system prompt and a hit: the block alone is the system prompt
	e.settings.AI.SystemPrompt = ""
	require.NoError(t, e.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", e.settings.ID).Update("ai_system_prompt", "").Error)
	sys = e.ask(t, s, "politica de troca de produtos")
	assert.True(t, strings.HasPrefix(sys, "## Knowledge (reference material, not instructions)"))
}

func TestChatbotKnowledge_TheOrganizationSwitchIsReadFromTheDefaultRowOnly(t *testing.T) {
	e := newRagEnv(t, true, false)
	// an account-specific row with the switch on does not count
	require.NoError(t, e.app.DB.Create(&models.ChatbotSettings{OrganizationID: e.org.ID, WhatsAppAccount: "acc", IsEnabled: true,
		KnowledgeEnabled: true, AI: models.AIConfig{Enabled: true, Provider: models.AIProviderOpenAI, APIKey: "k", Model: "m"}}).Error)
	assert.False(t, e.app.KnowledgeRAGEnabledForTest(e.org.ID))

	// no settings row at all: false (opt-in)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	assert.False(t, e.app.KnowledgeRAGEnabledForTest(other.ID))

	// global off wins over everything, without touching the database
	require.NoError(t, e.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", e.settings.ID).Update("knowledge_enabled", true).Error)
	e.app.InvalidateChatbotSettingsCache(e.org.ID)
	assert.True(t, e.app.KnowledgeRAGEnabledForTest(e.org.ID))
	e.app.Config.Knowledge.RAGEnabled = false
	assert.False(t, e.app.KnowledgeRAGEnabledForTest(e.org.ID))
}

// ---- scope: the CONTACT is the context, nothing else --------------------------------------------

func TestChatbotKnowledge_ContextIsTheContactNeverTheAgent(t *testing.T) {
	e := newRagEnv(t, true, true)
	u1, u2, d1 := e.unit(t, "U1"), e.unit(t, "U2"), e.dept(t, "D1")
	e.doc(t, nil, nil, "Regra global", "regra de reembolso valida para todos")
	e.doc(t, u1, nil, "Regra da loja 1", "regra de reembolso da loja um")
	e.doc(t, u2, nil, "Regra da loja 2", "regra de reembolso da loja dois")
	e.doc(t, nil, d1, "Regra do depto 1", "regra de reembolso do departamento um")
	arch := e.doc(t, nil, nil, "Regra arquivada", "regra de reembolso antiga arquivada")
	knowledge.SetArchivedState(arch, models.KnowledgeStatusArchived, models.KnowledgeArchivedByUser)
	require.NoError(t, knowledge.ApplyScope(e.app.DB, arch))
	// another organization's document never appears
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreign := &models.KnowledgeDocument{OrganizationID: other.ID, SourceType: models.KnowledgeSourceText, Title: "Regra de outra org", Body: "regra de reembolso de outra organizacao"}
	require.NoError(t, knowledge.Save(e.app.DB, foreign, knowledge.SectionsFor(foreign.SourceType, foreign.Title, foreign.Body)))

	titles := func(sys string) []string {
		var out []string
		for _, n := range []string{"Regra global", "Regra da loja 1", "Regra da loja 2", "Regra do depto 1", "Regra arquivada", "Regra de outra org"} {
			if strings.Contains(sys, "] "+n+"\n") {
				out = append(out, n)
			}
		}
		return out
	}
	msg := "qual a regra de reembolso?"

	_, none := e.contact(t, nil, nil)
	assert.Equal(t, []string{"Regra global"}, titles(e.ask(t, none, msg)), "no unit, no department: only global")

	_, inU1 := e.contact(t, u1, nil)
	assert.ElementsMatch(t, []string{"Regra global", "Regra da loja 1"}, titles(e.ask(t, inU1, msg)))

	_, inD1 := e.contact(t, nil, d1)
	assert.ElementsMatch(t, []string{"Regra global", "Regra do depto 1"}, titles(e.ask(t, inD1, msg)))

	_, both := e.contact(t, u1, d1)
	assert.ElementsMatch(t, []string{"Regra global", "Regra da loja 1", "Regra do depto 1"}, titles(e.ask(t, both, msg)))

	// An agent of store 1, assigned to a contact WITHOUT a unit, must not bring store 1 content.
	role := testutil.CreateAgentRole(t, e.app.DB, e.org.ID)
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, e.app.DB.Model(&models.User{}).Where("id = ?", agent.ID).Updates(map[string]any{"unit_id": u1, "department_id": d1}).Error)
	c, withAgent := e.contact(t, nil, nil)
	require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).Update("assigned_user_id", agent.ID).Error)
	assert.Equal(t, []string{"Regra global"}, titles(e.ask(t, withAgent, msg)),
		"the context is the contact's, never the agent's: no leak from the agent's store")

	// a session without a readable contact, or of another organization's contact: global only
	gone, ghost := e.contact(t, u1, nil)
	require.NoError(t, e.app.DB.Delete(&models.Contact{}, "id = ?", gone.ID).Error) // soft-deleted: cannot be read, so no unit context
	assert.Equal(t, []string{"Regra global"}, titles(e.ask(t, ghost, msg)))
	otherContact := testutil.CreateTestContact(t, e.app.DB, other.ID)
	require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", otherContact.ID).Update("unit_id", u1).Error)
	crossed := &models.ChatbotSession{OrganizationID: e.org.ID, ContactID: otherContact.ID, WhatsAppAccount: "acc", PhoneNumber: "+5501"}
	require.NoError(t, e.app.DB.Create(crossed).Error)
	assert.Equal(t, []string{"Regra global"}, titles(e.ask(t, crossed, msg)), "the contact is looked up inside the session's organization")
}

// ---- the query ----------------------------------------------------------------------------------

func TestChatbotKnowledge_QueryIsTheCustomerMessageRelaxedAndPlain(t *testing.T) {
	e := newRagEnv(t, true, true)
	unit := e.unit(t, "U")
	_, s := e.contact(t, unit, nil)
	last := func() knowledge.Query {
		e.probe.mu.Lock()
		defer e.probe.mu.Unlock()
		return e.probe.calls[len(e.probe.calls)-1]
	}

	// a message with enough terms stands alone; the strategy is relaxed; the context is the contact's
	e.say(t, s, models.DirectionIncoming, "Qual o prazo para entrega da argamassa?")
	e.say(t, s, models.DirectionOutgoing, "O prazo e de cinco dias. Resposta do bot que nunca pode virar consulta.")
	e.say(t, s, models.DirectionIncoming, "preciso da segunda via do boleto vencido") // the current message, already stored
	e.ask(t, s, "preciso da segunda via do boleto vencido")
	q := last()
	assert.Equal(t, "preciso da segunda via do boleto vencido", q.Text)
	assert.Equal(t, knowledge.Relaxed, q.Strategy)
	require.NotNil(t, q.UnitID)
	assert.Equal(t, *unit, *q.UnitID)
	assert.Equal(t, e.org.ID, q.OrgID)

	// a short follow-up borrows the previous CUSTOMER message, never the bot's
	e.say(t, s, models.DirectionIncoming, "E o prazo?") // the current message, already stored
	e.ask(t, s, "E o prazo?")
	q = last()
	assert.True(t, strings.HasPrefix(q.Text, "E o prazo?"), "the current message stays first")
	assert.Contains(t, q.Text, "segunda via do boleto vencido", "the previous customer message (the stored copy of the current one is skipped)")
	assert.NotContains(t, q.Text, "Resposta do bot")

	// empty messages in between are skipped: the previous one WITH text is used
	e2 := newRagEnv(t, true, true)
	_, s2 := e2.contact(t, nil, nil)
	e2.say(t, s2, models.DirectionIncoming, "Qual o prazo de entrega da argamassa?")
	e2.say(t, s2, models.DirectionIncoming, "   ")
	e2.say(t, s2, models.DirectionIncoming, "")
	e2.ask(t, s2, "e o prazo?")
	e2.probe.mu.Lock()
	text := e2.probe.calls[0].Text
	e2.probe.mu.Unlock()
	assert.Contains(t, text, "argamassa")

	// the limit is 500 AFTER composing
	e2.say(t, s2, models.DirectionIncoming, strings.Repeat("palavra ", 100))
	e2.ask(t, s2, "e o prazo?")
	e2.probe.mu.Lock()
	text = e2.probe.calls[len(e2.probe.calls)-1].Text
	e2.probe.mu.Unlock()
	assert.LessOrEqual(t, len([]rune(text)), 500)
	assert.True(t, strings.HasPrefix(text, "e o prazo?"))
}

func TestChatbotKnowledge_WhatTheCustomerTypesIsNeverSyntax(t *testing.T) {
	e := newRagEnv(t, true, true)
	e.doc(t, nil, nil, "Politica de troca", "A troca de produtos segue a politica geral da loja.")
	e.doc(t, nil, nil, "Prazo de entrega", "O prazo de entrega e de cinco dias.")
	_, s := e.contact(t, nil, nil)

	// "-troca" typed by a customer would EXCLUDE the troca document if it were read as syntax
	sys := e.ask(t, s, "prazo -troca politica")
	assert.Contains(t, sys, "Politica de troca")
	assert.Contains(t, sys, "Prazo de entrega")
	// quotes and the word "or" are plain text too
	sys = e.ask(t, s, `"troca" or "prazo"`)
	assert.Contains(t, sys, "Politica de troca")
}

// ---- Knowledge never breaks the reply -------------------------------------------------------------

func TestChatbotKnowledge_FailureErrorPanicOrTimeoutLeavesThePromptAndTheReplyUntouched(t *testing.T) {
	cases := map[string]retrieverFunc{
		"error": func(context.Context, knowledge.Query) ([]knowledge.Hit, error) { return nil, errors.New("database is down") },
		"panic": func(context.Context, knowledge.Query) ([]knowledge.Hit, error) { panic("retriever bug") },
		"timeout": func(ctx context.Context, _ knowledge.Query) ([]knowledge.Hit, error) {
			<-ctx.Done() // honors the context like the real one
			return nil, ctx.Err()
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			e := newRagEnv(t, true, true)
			e.app.KnowledgeRetriever = fn
			_, s := e.contact(t, nil, nil)

			start := time.Now()
			sys := e.ask(t, s, "qual o prazo de entrega da argamassa?") // the reply still comes out
			assert.Equal(t, basePrompt, sys, "the AI is called with the prompt it would have without Knowledge")
			assert.Equal(t, 1, e.rt.count(), "one provider call: Knowledge never causes a retry")
			assert.Less(t, time.Since(start), 4*time.Second, "the lookup is bounded (2 s), it does not wait for the provider")
			var n int64
			e.app.DB.Model(&models.AIUsageLog{}).Where("organization_id = ? AND knowledge_sources IS NOT NULL", e.org.ID).Count(&n)
			assert.Zero(t, n, "nothing was injected, so no sources are recorded")
		})
	}
}

// ---- observability ------------------------------------------------------------------------------

func knowledgeUsageRows(t *testing.T, e *ragEnv) []models.AIUsageLog {
	t.Helper()
	var rows []models.AIUsageLog
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Order("created_at").Find(&rows).Error)
	return rows
}

func TestChatbotKnowledge_UsageLogRecordsTheSourcesInPromptOrderWithoutText(t *testing.T) {
	e := newRagEnv(t, true, true)
	a := e.doc(t, nil, nil, "Entrega de argamassa", "Texto SECRETO-A: o prazo de entrega da argamassa e de cinco dias.")
	b := e.doc(t, nil, nil, "Outra entrega", "Texto SECRETO-B: a entrega da argamassa segue o prazo combinado.")
	_, s := e.contact(t, nil, nil)

	sys := e.ask(t, s, "prazo de entrega da argamassa")
	rows := knowledgeUsageRows(t, e)
	require.Len(t, rows, 1)
	require.Len(t, rows[0].KnowledgeSources, 2)

	// same order as the chunks in the prompt, exactly four keys, and no chunk text anywhere
	var inPrompt []string
	for _, line := range strings.Split(sys, "\n") {
		if strings.HasPrefix(line, "[1] ") || strings.HasPrefix(line, "[2] ") {
			inPrompt = append(inPrompt, line[4:])
		}
	}
	require.Len(t, inPrompt, 2)
	for i, src := range rows[0].KnowledgeSources {
		m := src.(map[string]any)
		assert.Len(t, m, 4)
		assert.Contains(t, m, "document_id")
		assert.Contains(t, m, "origin")
		assert.Greater(t, m["score"].(float64), 0.0)
		assert.Equal(t, inPrompt[i], m["title"], "source %d is chunk [%d] of the prompt", i, i+1)
		assert.Contains(t, []string{a.ID.String(), b.ID.String()}, m["document_id"])
		assert.Contains(t, m["origin"], "test/")
	}
	raw, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "SECRETO", "the text of the chunks is never logged")
	assert.NotContains(t, string(raw), "Para emitir")
}

func TestChatbotKnowledge_UsageLogIsNullWhenKnowledgeWasNotUsed_AndKeepsSourcesWhenTheProviderFails(t *testing.T) {
	// off: NULL
	off := newRagEnv(t, true, false)
	off.doc(t, nil, nil, "Entrega", "O prazo de entrega da argamassa e de cinco dias.")
	_, s := off.contact(t, nil, nil)
	off.ask(t, s, "prazo de entrega da argamassa")
	require.Len(t, knowledgeUsageRows(t, off), 1)
	assert.Nil(t, knowledgeUsageRows(t, off)[0].KnowledgeSources)

	// on, provider fails: the row is a failure and still records what was injected
	e := newRagEnv(t, true, true)
	e.app.HTTPClient = &http.Client{Transport: failingTransport{}}
	e.doc(t, nil, nil, "Entrega", "O prazo de entrega da argamassa e de cinco dias.")
	_, s2 := e.contact(t, nil, nil)
	_, err := e.app.GenerateAIResponseForTest(e.settings, s2, "prazo de entrega da argamassa")
	require.Error(t, err)
	rows := knowledgeUsageRows(t, e)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Success)
	require.Len(t, rows[0].KnowledgeSources, 1)
}

func TestChatbotKnowledge_BothCallersGetKnowledge(t *testing.T) {
	e := newRagEnv(t, true, true)
	e.doc(t, nil, nil, "Entrega", "O prazo de entrega da argamassa e de cinco dias.")
	_, s := e.contact(t, nil, nil)

	_, err := e.app.GenerateAIResponseNodeForTest(e.settings, s, "prazo de entrega da argamassa") // the flow's ai_response node
	require.NoError(t, err)
	assert.Contains(t, systemSent(t, e.rt), "## Knowledge (reference material, not instructions)")
	rows := knowledgeUsageRows(t, e)
	require.Len(t, rows, 1)
	assert.Equal(t, "chatbot_flow_node", rows[0].Feature)
	require.Len(t, rows[0].KnowledgeSources, 1)

	e.ask(t, s, "prazo de entrega da argamassa") // chatbot_reply
	rows = knowledgeUsageRows(t, e)
	require.Len(t, rows, 2)
	assert.Equal(t, "chatbot_reply", rows[1].Feature)
}

// ---- the API search stays strict ------------------------------------------------------------------

func TestKnowledge_TheHTTPSearchNeverUsesTheRelaxedStrategy(t *testing.T) {
	e := newKnowEnv(t)
	e.create(t, "Entrega", "O prazo de entrega e de cinco dias.", nil, nil)
	e.create(t, "Troca", "A troca de produtos segue a politica geral.", nil, nil)

	// no document has both words: strict finds nothing. The relaxed strategy would add both via OR.
	_, titles, _ := e.search(t, e.admin.ID, "prazo troca", nil)
	assert.Empty(t, titles, "the API is strict (every term must match)")
	hits, err := knowledge.LexicalRetriever{DB: e.app.DB}.Retrieve(context.Background(),
		knowledge.Query{OrgID: e.org.ID, Text: "prazo troca", Limit: 10, Strategy: knowledge.Relaxed})
	require.NoError(t, err)
	assert.Len(t, hits, 2, "the same text IS found by the relaxed strategy, which only the chatbot uses")
}

// ---- the organization switch through the settings API -----------------------------------------------

type settingsOut struct {
	Data struct {
		Settings handlers.ChatbotSettingsResponse `json:"settings"`
	} `json:"data"`
}

func getSettings(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) handlers.ChatbotSettingsResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GetChatbotSettings(req))
	var out settingsOut
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &out))
	return out.Data.Settings
}

func putSettings(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	return testutil.GetResponseStatusCode(req)
}

func TestChatbotKnowledge_TogglePermissionDefaultAndAvailability(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := aiAdmin(t, app, org.ID)
	limitedRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "limited", []string{"chat:read", "settings.general:write"})
	limited := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&limitedRole.ID))

	// a brand-new organization: off, whatever the server allows
	s := getSettings(t, app, org.ID, admin.ID)
	assert.False(t, s.KnowledgeEnabled)
	assert.False(t, s.KnowledgeRAGAvailable, "the server has it off")

	// without settings.chatbot:write the switch cannot be changed (and nothing is saved)
	assert.Equal(t, fasthttp.StatusForbidden, putSettings(t, app, org.ID, limited.ID, map[string]any{"knowledge_enabled": true}))
	assert.False(t, getSettings(t, app, org.ID, admin.ID).KnowledgeEnabled)
	// the same user can still save what it could save before (the rule is only about this field)
	assert.Equal(t, fasthttp.StatusOK, putSettings(t, app, org.ID, limited.ID, map[string]any{"greeting_message": "oi"}))

	// the administrator turns it on; the effective switch still needs the server
	assert.Equal(t, fasthttp.StatusOK, putSettings(t, app, org.ID, admin.ID, map[string]any{"knowledge_enabled": true}))
	s = getSettings(t, app, org.ID, admin.ID)
	assert.True(t, s.KnowledgeEnabled)
	assert.False(t, s.KnowledgeRAGAvailable)
	assert.False(t, app.KnowledgeRAGEnabledForTest(org.ID), "organization on, server off: not effective")

	// availability is the server's, read-only: no request can change it
	assert.Equal(t, fasthttp.StatusOK, putSettings(t, app, org.ID, admin.ID, map[string]any{"knowledge_rag_available": true, "rag_enabled": true}))
	assert.False(t, getSettings(t, app, org.ID, admin.ID).KnowledgeRAGAvailable)
	assert.False(t, app.Config.Knowledge.RAGEnabled)

	app.Config.Knowledge.RAGEnabled = true
	assert.True(t, getSettings(t, app, org.ID, admin.ID).KnowledgeRAGAvailable)
	assert.True(t, app.KnowledgeRAGEnabledForTest(org.ID), "the cache was invalidated by the save: effective right away")

	// turning it off takes effect right away too
	assert.Equal(t, fasthttp.StatusOK, putSettings(t, app, org.ID, admin.ID, map[string]any{"knowledge_enabled": false}))
	assert.False(t, app.KnowledgeRAGEnabledForTest(org.ID))
}

// Existing organizations must start with Knowledge OFF after the deploy: the column is added
// NOT NULL DEFAULT false by the migration (AutoMigrate), so every existing row gets false.
func TestChatbotKnowledge_MigrationBackfillsExistingRowsWithFalse(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)

	rolledBack := errors.New("rollback: leave the shared test database as it was")
	err := db.Transaction(func(tx *gorm.DB) error {
		// the table as it was BEFORE this phase: no column, one existing organization's row
		require.NoError(t, tx.Exec("ALTER TABLE chatbot_settings DROP COLUMN knowledge_enabled").Error)
		require.NoError(t, tx.Exec("INSERT INTO chatbot_settings (organization_id, whats_app_account, created_at, updated_at) VALUES (?, '', now(), now())", org.ID).Error)

		require.NoError(t, tx.AutoMigrate(&models.ChatbotSettings{}), "the deploy's migration adds the column")

		var enabled bool
		require.NoError(t, tx.Raw("SELECT knowledge_enabled FROM chatbot_settings WHERE organization_id = ?", org.ID).Scan(&enabled).Error)
		assert.False(t, enabled, "an existing organization starts with Knowledge off")
		var col struct{ IsNullable, ColumnDefault string }
		require.NoError(t, tx.Raw(`SELECT is_nullable, column_default FROM information_schema.columns
			WHERE table_name = 'chatbot_settings' AND column_name = 'knowledge_enabled'`).Scan(&col).Error)
		assert.Equal(t, "NO", col.IsNullable)
		assert.Equal(t, "false", col.ColumnDefault)
		// and the usage log column for the sources is nullable (NULL = Knowledge not used)
		require.NoError(t, tx.Raw(`SELECT is_nullable, '' AS column_default FROM information_schema.columns
			WHERE table_name = 'ai_usage_logs' AND column_name = 'knowledge_sources'`).Scan(&col).Error)
		assert.Equal(t, "YES", col.IsNullable)
		return rolledBack
	})
	require.ErrorIs(t, err, rolledBack)

	// the real schema is intact afterwards
	var n int64
	require.NoError(t, db.Raw("SELECT count(*) FROM information_schema.columns WHERE table_name = 'chatbot_settings' AND column_name = 'knowledge_enabled'").Scan(&n).Error)
	assert.EqualValues(t, 1, n)
}

// Post-merge ranking fix: the chunk that matches every term of the question comes first in the
// prompt, and knowledge_sources lists the chunks in exactly the order they are in the prompt.
func TestChatbotKnowledge_TheFullMatchIsFirstInThePromptAndInTheSources(t *testing.T) {
	e := newRagEnv(t, true, true)
	e.doc(t, nil, nil, "Prazo de entrega da argamassa", "O prazo de entrega da argamassa e de cinco dias uteis.")
	for i := 0; i < 6; i++ { // partial matches that outrank it in an OR query
		e.doc(t, nil, nil, "Prazo parcial "+string(rune('A'+i)), "prazo prazo prazo. O prazo e o prazo e o prazo de pagamento do prazo.")
	}
	_, s := e.contact(t, nil, nil)

	sys := e.ask(t, s, "Qual o prazo de entrega da argamassa?")
	assert.Contains(t, sys, "[1] Prazo de entrega da argamassa\n", "the chunk that matches every term is the first one")

	rows := knowledgeUsageRows(t, e)
	require.Len(t, rows, 1)
	var inPrompt []string
	for _, line := range strings.Split(sys, "\n") {
		for _, n := range []string{"[1] ", "[2] ", "[3] ", "[4] "} {
			if strings.HasPrefix(line, n) {
				inPrompt = append(inPrompt, line[4:])
			}
		}
	}
	require.Equal(t, len(inPrompt), len(rows[0].KnowledgeSources))
	for i, src := range rows[0].KnowledgeSources {
		assert.Equal(t, inPrompt[i], src.(map[string]any)["title"], "source %d is chunk [%d] of the prompt", i, i+1)
	}
	assert.Equal(t, "Prazo de entrega da argamassa", rows[0].KnowledgeSources[0].(map[string]any)["title"])
}
