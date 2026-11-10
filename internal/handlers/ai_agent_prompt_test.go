package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAgentPrompt(t *testing.T) {
	full := buildAgentPrompt(&models.AIAgent{
		Objective: "Qualificar compras", Tone: "cordial", CompanyInfo: "Atacadão dos Pisos",
		Instructions: "Pergunte a metragem", HandoffRules: "Transferir se pedir humano",
	})
	order := []string{"## Objetivo", "## Tom de comunicação", "## Informações da empresa",
		"## Instruções e comportamento", "## Quando transferir para um humano"}
	last := -1
	for _, h := range order {
		i := strings.Index(full, h)
		require.GreaterOrEqual(t, i, 0, h)
		require.Greater(t, i, last, "sections keep their fixed order: %s", h)
		last = i
	}

	only := buildAgentPrompt(&models.AIAgent{Objective: "x", Tone: "   "})
	assert.Contains(t, only, "## Objetivo")
	assert.NotContains(t, only, "## Tom", "blank sections are omitted")
	assert.Empty(t, buildAgentPrompt(&models.AIAgent{}))
}

func TestExtractHandoff(t *testing.T) {
	for _, tc := range []struct {
		in, clean string
		handoff   bool
	}{
		{"Claro, vou chamar um vendedor. [[TRANSFERIR]]", "Claro, vou chamar um vendedor.", true},
		{"Claro [[TRANSFERIR]] vou chamar", "Claro  vou chamar", true},
		{"[[TRANSFERIR]] [[TRANSFERIR]]", "", true},
		{"[[TRANSFERIR]]", "", true},
		{"ok [[ transferir ]]", "ok", true},
		{"ok [[TRANSFERIR", "ok", true}, // marker cut short by the token limit
		{"Olá, tudo bem?", "Olá, tudo bem?", false},
		{"preciso transferir R$ 10 [[outro]]", "preciso transferir R$ 10 [[outro]]", false},
	} {
		clean, handoff := extractHandoff(tc.in)
		assert.Equal(t, tc.clean, clean, tc.in)
		assert.Equal(t, tc.handoff, handoff, tc.in)
		assert.NotContains(t, strings.ToLower(clean), "transferir]]")
	}
}

func TestViolatesCommercialSafety(t *testing.T) {
	for _, tc := range []struct {
		name, text, allowed string
		want                bool
	}{
		{"promise", "garanto o preço", "", true},
		{"promise upper case", "GARANTO a entrega", "", true},
		{"negated promise", "Não garanto estoque", "", false},
		{"negated promise without accent", "nao garanto", "", false},
		{"not a whole word", "garantos", "", false},
		{"warranty is not a promise", "Tem garantia de fábrica", "", false},
		{"reservation", "já reservei para você", "", true},
		{"price not configured", "O valor é R$ 25,99 o m²", "", true},
		{"price configured by the company", "O valor é R$ 25,99", "tabela: R$ 25,99", false},
		{"price said by the customer", "Com R$ 5.000 dá para a sala", "tenho R$ 5.000 de orçamento", false},
		{"price in reais", "sai por 120 reais", "", true},
		{"discount", "Posso dar 10% de desconto", "", true},
		{"discount configured", "Temos 10% de desconto à vista", "desconto de 10% à vista", false},
		{"percent without discount", "tenho 100% de certeza do tamanho", "", false},
		{"stock", "temos em estoque", "", true},
		{"stock hedged", "o vendedor confirma se está em estoque", "", false},
		{"stock conditional", "se estiver em estoque ele separa", "", false},
		{"deadline", "o prazo de 5 dias", "", true},
		{"deadline hours", "entregamos em 48 horas", "", true},
		{"deadline tomorrow", "entregamos amanhã", "", true},
		{"deadline configured", "entregamos em até 5 dias úteis", "entrega em 5 dias úteis na capital", false},
		{"no number deadline", "o vendedor confirma o prazo", "", false},
		{"area is fine", "Para 40 m² na sala, certo?", "", false},
		{"plain question", "Qual ambiente você quer revestir?", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, violatesCommercialSafety(tc.text, tc.allowed))
		})
	}
}

// seqTransport answers successive OpenAI-compatible chat completions and records the bodies.
type seqTransport struct {
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
	// onCall, when set, runs inside call i (0-based) before the reply is returned: a hook for
	// simulating something that happens while the model is "thinking" (e.g. a human takes over).
	onCall func(i int)
}

func (s *seqTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(b, &parsed)
	s.mu.Lock()
	n := len(s.bodies)
	s.bodies = append(s.bodies, parsed)
	hook := s.onCall
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	s.mu.Lock()
	text := "ok"
	if n < len(s.replies) {
		text = s.replies[n]
	} else if len(s.replies) > 0 {
		text = s.replies[len(s.replies)-1]
	}
	s.mu.Unlock()
	out, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": text}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(out))),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

func (s *seqTransport) systemPrompt(i int) string {
	msgs := s.bodies[i]["messages"].([]any)
	return msgs[0].(map[string]any)["content"].(string)
}

func (s *seqTransport) lastUserMessage(i int) string {
	msgs := s.bodies[i]["messages"].([]any)
	return msgs[len(msgs)-1].(map[string]any)["content"].(string)
}

// agentReplyFixture is the agent fixture plus a session on its number and a provider that
// answers with the given texts.
func agentReplyFixture(t *testing.T, replies ...string) (*App, aiAgentFixture, *models.ChatbotSession, *seqTransport) {
	t.Helper()
	app := newSLATestApp(t)
	tr := &seqTransport{replies: replies}
	app.HTTPClient = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	app.Config = &config.Config{App: config.AppConfig{EncryptionKey: "test-encryption-key-for-handlers-longer-than-32-chars"}}

	f := newAIAgentFixture(t, app)
	enc, err := crypto.Encrypt("sk-test", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	f.settings.AI.Enabled = true
	f.settings.AI.Provider = models.AIProvider("openai")
	f.settings.AI.Model = "gpt-test"
	f.settings.AI.APIKey = enc
	f.settings.AI.MaxTokens = 200
	f.settings.AI.SystemPrompt = "LEGACY PROMPT"
	require.NoError(t, app.DB.Model(f.agent).Updates(map[string]any{
		"objective": "Qualificar compras de pisos", "company_info": "Atacadão dos Pisos, loja Centro",
	}).Error)

	contactID := uuid.New()
	session := &models.ChatbotSession{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: contactID,
		WhatsAppAccount: f.account.Name, Status: models.SessionStatusActive, LastActivityAt: time.Now(),
	}
	return app, f, session, tr
}

func TestGenerateAIReply_NoAgentIsUnchanged(t *testing.T) {
	app, f, session, tr := agentReplyFixture(t, "Olá! Como posso ajudar?")

	// case 1: no default agent at all
	f.settings.AI.DefaultAgentID = nil
	reply, err := app.generateAIReply(f.settings, session, "oi", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, "Olá! Como posso ajudar?", reply.Text)
	assert.Empty(t, reply.Handoff)
	assert.Equal(t, "LEGACY PROMPT", tr.systemPrompt(0), "the free-text SystemPrompt is sent exactly as before")

	// case 2: a default agent exists but may not act on this number
	f.settings.AI.DefaultAgentID = &f.agent.ID
	session.WhatsAppAccount = "some-other-number"
	reply, err = app.generateAIReply(f.settings, session, "oi", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, "LEGACY PROMPT", tr.systemPrompt(1))
	assert.Empty(t, reply.Handoff)
}

func TestGenerateAIReply_WithAgentUsesLayeredPrompt(t *testing.T) {
	app, f, session, tr := agentReplyFixture(t, "Qual ambiente você quer revestir?")

	reply, err := app.generateAIReply(f.settings, session, "quero porcelanato", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, "Qual ambiente você quer revestir?", reply.Text)

	sys := tr.systemPrompt(0)
	assert.True(t, strings.HasPrefix(sys, "REGRAS DA PLATAFORMA"))
	assert.Contains(t, sys, "Qualificar compras de pisos")
	assert.Contains(t, sys, "Atacadão dos Pisos, loja Centro")
	assert.NotContains(t, sys, "LEGACY PROMPT", "the agent replaces the free-text prompt")
	assert.True(t, strings.HasSuffix(sys, aiPlatformReminder), "the hard rules close the prompt")
}

func TestGenerateAIReply_MarkerNeverReachesTheCustomer(t *testing.T) {
	app, f, session, _ := agentReplyFixture(t, "Perfeito! [[TRANSFERIR]] [[TRANSFERIR]]")
	reply, err := app.generateAIReply(f.settings, session, "quero comprar 40 m2 de porcelanato", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, aiHandoffMarker, reply.Handoff)
	assert.Equal(t, "Perfeito!", reply.Text)
	assert.NotContains(t, reply.Text, "[[")
}

func TestGenerateAIReply_UnsafeIsRegeneratedOnce(t *testing.T) {
	app, f, session, tr := agentReplyFixture(t, "O m² sai por R$ 99,90 e temos em estoque.",
		"O vendedor confirma valor e disponibilidade. Qual ambiente?")
	reply, err := app.generateAIReply(f.settings, session, "quanto custa?", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, "O vendedor confirma valor e disponibilidade. Qual ambiente?", reply.Text)
	assert.Empty(t, reply.Handoff)
	require.Len(t, tr.bodies, 2, "exactly one regeneration")
	assert.Equal(t, aiUnsafeRetryInstruction, tr.lastUserMessage(1))
}

func TestGenerateAIReply_UnsafeTwiceHandsOverWithoutText(t *testing.T) {
	app, f, session, tr := agentReplyFixture(t, "R$ 99,90 o m²", "Garanto o preço de R$ 99,90")
	reply, err := app.generateAIReply(f.settings, session, "quanto custa?", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, aiHandoffUnsafe, reply.Handoff)
	assert.Empty(t, reply.Text, "the unsafe text is never returned")
	assert.Len(t, tr.bodies, 2)
}

func TestGenerateAIReply_ConfiguredPriceIsAllowed(t *testing.T) {
	app, f, session, _ := agentReplyFixture(t, "O frete grátis vale acima de R$ 500,00.")
	require.NoError(t, app.DB.Model(f.agent).Update("company_info", "Frete grátis acima de R$ 500,00").Error)
	reply, err := app.generateAIReply(f.settings, session, "tem frete grátis?", aiFeatureChatbotReply)
	require.NoError(t, err)
	assert.Equal(t, "O frete grátis vale acima de R$ 500,00.", reply.Text)
}
