package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestCleanDisplayName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Assistente Virtual", "Assistente Virtual"},
		{"  Atendente   Virtual  ", "Atendente Virtual"},
		{"*Bold* _it_ ~st~ `code`", "Bold it st code"},
		{"Linha 1\nLinha 2", "Linha 1Linha 2"},
		{"", ""},
		{"****", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", maxAIDisplayName)},
	} {
		assert.Equal(t, tc.want, cleanDisplayName(tc.in), tc.in)
	}
}

func TestAIDisplayName_DefaultsToAssistenteVirtual(t *testing.T) {
	assert.Equal(t, "Assistente Virtual", aiDisplayName(nil))
	assert.Equal(t, "Assistente Virtual", aiDisplayName(&models.AIAgent{}))
	assert.Equal(t, "Assistente Virtual", aiDisplayName(&models.AIAgent{DisplayName: "***"}), "a name made only of formatting characters falls back")
	assert.Equal(t, "Atendente Virtual da Loja Centro", aiDisplayName(&models.AIAgent{DisplayName: "Atendente Virtual da Loja Centro"}))
}

// lastOutgoing returns the most recent outgoing message of the contact.
func (w *wiring) lastOutgoing(t *testing.T, contactID uuid.UUID) models.Message {
	t.Helper()
	var m models.Message
	require.NoError(t, w.app.DB.Where("organization_id = ? AND contact_id = ? AND direction = ?", w.f.org.ID, contactID, models.DirectionOutgoing).
		Order("created_at DESC").First(&m).Error)
	return m
}

// The customer sees who answers: the agent's text goes to WhatsApp signed with its display name, while
// the stored message keeps the raw text, records the automated sender and has NO user.
func TestAgentMessages_AreSignedAndRecordedAsAI(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	const from = "5511900000030"
	w.inbound(t, from, "quero porcelanato")

	sent := w.wa.all()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], `*Assistente Virtual:*\nQual ambiente você quer revestir?`, "signed in the style of a human signature")

	m := w.lastOutgoing(t, w.contactFor(t, from).ID)
	assert.Equal(t, "Qual ambiente você quer revestir?", m.Content, "the stored message keeps the raw text")
	assert.Nil(t, m.SentByUserID, "the AI is never a user")
	name, typ := messageSender(&m)
	assert.Equal(t, "Assistente Virtual", name)
	assert.Equal(t, senderTypeAIAgent, typ)
	sender := m.Metadata[metadataSender].(map[string]any)
	assert.Equal(t, w.f.agent.ID.String(), sender["agent_id"])
}

func TestAgentMessages_UseTheConfiguredDisplayName(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	require.NoError(t, w.app.DB.Model(w.f.agent).Update("display_name", "Atendente Virtual Loja Centro").Error)
	const from = "5511900000031"
	w.inbound(t, from, "quero porcelanato")

	assert.Contains(t, w.wa.all()[0], `*Atendente Virtual Loja Centro:*\n`)
	name, _ := messageSender(ptr(w.lastOutgoing(t, w.contactFor(t, from).ID)))
	assert.Equal(t, "Atendente Virtual Loja Centro", name)
}

func ptr[T any](v T) *T { return &v }

// Everything the agent says is identified: the text before a handoff, the waiting message and the
// message about a failed handoff. (The out-of-hours text is the organization's own and is not.)
func TestAgentMessages_HandoffMessagesAreIdentifiedToo(t *testing.T) {
	w := newWiring(t, "Perfeito, anotei! [[TRANSFERIR]]", qualHappy)
	w.inbound(t, "5511900000032", "quero porcelanato para a sala, uns 40 m²")

	sent := w.wa.all()
	require.Len(t, sent, 2)
	for _, body := range sent {
		assert.Contains(t, body, `*Assistente Virtual:*\n`, body)
	}
	assert.Contains(t, sent[1], "atendente")

	w2 := newWiring(t, "[[TRANSFERIR]]", qualHappy)
	forceTransferFailure(t, w2, "true")
	w2.inbound(t, "5511900000033", "quero porcelanato para a sala, uns 40 m²")
	require.Len(t, w2.wa.all(), 1)
	assert.Contains(t, w2.wa.all()[0], `*Assistente Virtual:*\nTive um problema`)
}

// Messages that are not the agent's stay exactly as they were: a human's, signed or not, the chatbot's
// own AI path (no agent) and the fallback text.
func TestAgentIdentity_DoesNotChangeHumanOrNonAgentMessages(t *testing.T) {
	w := newWiring(t)
	contact, _ := w.handoffSession(t, "oi")

	// a human attendant's message: no AI signature, no sender metadata, the user is recorded
	seller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	opts := DefaultSendOptions()
	opts.Async = false
	opts.SentByUserID = &seller.ID
	_, err := w.app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: w.f.account, Contact: contact, Type: models.MessageTypeText, Content: "Oi, aqui é o Carlos.",
	}, opts)
	require.NoError(t, err)
	human := w.lastOutgoing(t, contact.ID)
	require.NotNil(t, human.SentByUserID)
	assert.Equal(t, seller.ID, *human.SentByUserID)
	_, typ := messageSender(&human)
	assert.Empty(t, typ, "a human message carries no AI sender")
	assert.NotContains(t, w.wa.all()[len(w.wa.all())-1], "Assistente Virtual")

	// the chatbot's plain text (no agent): untouched, and not identified as an agent
	require.NoError(t, w.app.sendAndSaveTextMessage(botCtx("test"), w.f.account, contact, "Mensagem do chatbot"))
	plain := w.lastOutgoing(t, contact.ID)
	assert.Equal(t, "Mensagem do chatbot", plain.Content)
	_, typ = messageSender(&plain)
	assert.Empty(t, typ)
	assert.NotContains(t, w.wa.all()[len(w.wa.all())-1], "Assistente Virtual")

	// a nil sender is exactly the plain send
	require.NoError(t, w.app.sendAndSaveAgentText(botCtx("test"), w.f.account, contact, "Sem remetente", nil))
	assert.NotContains(t, w.wa.all()[len(w.wa.all())-1], "Assistente Virtual")
}

// The human signature still works as before ("Assinar com o nome do atendente").
func TestAgentIdentity_HumanSignatureUnchanged(t *testing.T) {
	w := newWiring(t)
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).
		Update("sign_with_agent_name", true).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	contact, _ := w.handoffSession(t, "oi")
	seller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithFullName("Carlos Silva"))
	opts := DefaultSendOptions()
	opts.Async = false
	opts.SentByUserID = &seller.ID
	_, err := w.app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: w.f.account, Contact: contact, Type: models.MessageTypeText, Content: "Bom dia!",
	}, opts)
	require.NoError(t, err)
	last := w.wa.all()[len(w.wa.all())-1]
	assert.Contains(t, last, `*Carlos:*\nBom dia!`)
	assert.NotContains(t, last, "Assistente Virtual")
}

// A message of an AI agent reaches the history API and the live broadcast as an AI message, with its display
// name, and the human messages around it do not.
func TestAgentIdentity_HistoryAPIShowsTheAISender(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	const from = "5511900000034"
	w.inbound(t, from, "quero porcelanato")
	contact := w.contactFor(t, from)
	seller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithSuperAdmin(), testutil.WithFullName("Carlos Silva"))

	opts := DefaultSendOptions()
	opts.Async = false
	opts.SentByUserID = &seller.ID
	_, err := w.app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: w.f.account, Contact: contact, Type: models.MessageTypeText, Content: "Oi, aqui é o Carlos.",
	}, opts)
	require.NoError(t, err)

	req := testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(req, w.f.org.ID, seller.ID, nil, true)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, w.app.GetMessages(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"sender_type":"ai_agent"`)
	assert.Contains(t, body, `"sent_by_user_name":"Assistente Virtual"`)
	assert.Contains(t, body, `"sent_by_user_name":"Carlos Silva"`, "the human message is identified as before")
	assert.Equal(t, 1, strings.Count(body, `"sender_type":"ai_agent"`), "only the AI message is marked")
}

// The consumption ledger already says who produced a message: the agent's words, including the waiting
// message, are ledgered as the AI, never as an attendant.
func TestAgentIdentity_LedgerRecordsTheActorAsAI(t *testing.T) {
	w := newWiring(t, "[[TRANSFERIR]]", qualHappy)
	w.app.Usage = usage.New(w.app.DB, config.UsageConfig{})
	require.True(t, w.app.Usage.Enabled())
	w.inbound(t, "5511900000035", "quero porcelanato para a sala, uns 40 m²")

	var rows []models.MessageUsage
	require.NoError(t, w.app.DB.Where("organization_id = ? AND direction = ?", w.f.org.ID, "outgoing").Find(&rows).Error)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Equal(t, string(usage.ActorAI), r.ActorType, "agent messages are ledgered as ai")
		assert.Nil(t, r.ActorUserID, "and never tied to a user")
	}
}

// The agent's display name is configurable through the API, cleaned and bounded.
func TestAIAgentsAPI_DisplayName(t *testing.T) {
	w := newWiring(t)
	user := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithSuperAdmin())

	req := testutil.NewJSONRequest(t, map[string]any{"display_name": "  *Atendente*  Virtual "})
	testutil.SetAuthContext(req, w.f.org.ID, user.ID)
	testutil.SetPathParam(req, "id", w.f.agent.ID.String())
	require.NoError(t, w.app.UpdateAIAgent(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var stored models.AIAgent
	require.NoError(t, w.app.DB.First(&stored, "id = ?", w.f.agent.ID).Error)
	assert.Equal(t, "Atendente Virtual", stored.DisplayName, "formatting characters never reach the signature")

	req = testutil.NewJSONRequest(t, map[string]any{"display_name": strings.Repeat("x", 61)})
	testutil.SetAuthContext(req, w.f.org.ID, user.ID)
	testutil.SetPathParam(req, "id", w.f.agent.ID.String())
	require.NoError(t, w.app.UpdateAIAgent(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}
