package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buttonTap is the webhook payload of a customer tapping a reply button.
func buttonTap(from, wamid, buttonID string) IncomingTextMessage {
	var m IncomingTextMessage
	raw, _ := json.Marshal(map[string]any{
		"from": from, "id": wamid, "timestamp": "1700000000", "type": "interactive",
		"interactive": map[string]any{"type": "button_reply", "button_reply": map[string]any{"id": buttonID, "title": "x"}},
	})
	_ = json.Unmarshal(raw, &m)
	return m
}

func TestWebhook_ConfirmationTapIsConsumedBeforeTheChatbotSeesIt(t *testing.T) {
	app := newProcessorTestApp(t)
	require.NoError(t, app.DB.Exec("DELETE FROM ai_tool_confirmations").Error)
	t.Cleanup(func() { app.DB.Exec("DELETE FROM ai_tool_confirmations") })
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511987654321"))

	// the chatbot is on with AI configured; if the tap leaked into normal handling it would be answered
	s := models.ChatbotSettings{OrganizationID: org.ID, IsEnabled: true, DefaultResponse: "SENTINEL-DEFAULT-RESPONSE", FallbackMessage: "SENTINEL-FALLBACK"}
	s.AI = models.AIConfig{Enabled: true, Provider: models.AIProviderOpenAI}
	require.NoError(t, app.DB.Create(&s).Error)
	app.Config.AITools.Enabled, app.Config.AITools.WriteEnabled, app.Config.AITools.Providers = true, true, []string{"openai"}
	require.NoError(t, app.DB.Create(&models.AIToolSetting{OrganizationID: org.ID, ToolName: "request_agent_transfer", Enabled: true}).Error)

	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive, LastActivityAt: time.Now()}
	require.NoError(t, app.DB.Create(session).Error)
	token, conf, err := app.aiToolConfirmationStore().Propose(context.Background(), aitools.ProposeInput{
		Scope: aitools.Scope{OrganizationID: org.ID, ContactID: &contact.ID, SessionID: &session.ID, WhatsAppAccount: account.Name},
		RunID: uuid.New(), Tool: "request_agent_transfer", Args: models.JSONB{"reason": "x"},
	})
	require.NoError(t, err)

	// the customer taps the confirmation button
	app.processIncomingMessageFull(account.PhoneID, buttonTap(contact.PhoneNumber, "wamid.in.1", "aitc:"+token), "Customer")

	var transfers []models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Find(&transfers).Error)
	require.Len(t, transfers, 1, "the tap carried the action out")
	assert.Equal(t, models.TransferSourceAIConfirmed, transfers[0].Source)
	got, err := app.aiToolConfirmationStore().Get(context.Background(), conf.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AIConfirmationExecuted, got.Status)

	for _, sent := range outgoingTexts(t, app, contact.ID) {
		assert.NotContains(t, sent, "SENTINEL", "the tap never reached the chatbot's normal handling")
	}
	assert.Contains(t, outgoingTexts(t, app, contact.ID), "Pronto! Você entrou na fila de atendimento. Um atendente vai responder em breve.")

	// redelivery of the same webhook: still one transfer and nothing more is said
	said := len(outgoingTexts(t, app, contact.ID))
	app.processIncomingMessageFull(account.PhoneID, buttonTap(contact.PhoneNumber, "wamid.in.1", "aitc:"+token), "Customer")
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Find(&transfers).Error)
	assert.Len(t, transfers, 1)
	assert.Len(t, outgoingTexts(t, app, contact.ID), said)
}

func TestWebhook_AnUnknownConfirmationButtonIsIgnoredNotAnswered(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511987654322"))
	s := models.ChatbotSettings{OrganizationID: org.ID, IsEnabled: true, FallbackMessage: "SENTINEL-FALLBACK"}
	require.NoError(t, app.DB.Create(&s).Error)

	app.processIncomingMessageFull(account.PhoneID, buttonTap(contact.PhoneNumber, "wamid.in.9", "aitc:not-a-real-token"), "Customer")
	app.processIncomingMessageFull(account.PhoneID, buttonTap(contact.PhoneNumber, "wamid.in.10", "aitd:not-a-real-token"), "Customer")

	assert.Empty(t, outgoingTexts(t, app, contact.ID), "nothing is said, and the chatbot did not take it as a message")
	var n int64
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&n).Error)
	assert.Zero(t, n)
}

func TestIsAIToolConfirmationButton(t *testing.T) {
	assert.True(t, isAIToolConfirmationButton("aitc:abc"))
	assert.True(t, isAIToolConfirmationButton("aitd:abc"))
	for _, id := range []string{"", "yes", "AITC:abc", "xaitc:abc", "ait:abc"} {
		assert.False(t, isAIToolConfirmationButton(id), id)
	}
}

func TestAIToolConfirmationsNeedTheServerSecret(t *testing.T) {
	app := newProcessorTestApp(t)
	assert.True(t, app.aiToolConfirmationsReady())
	app.Config.App.EncryptionKey = ""
	assert.False(t, app.aiToolConfirmationsReady(), "without the secret nothing can be bound to an action")
}

func TestWebhook_ATapFromAnotherSessionOfTheSameContactIsIgnored(t *testing.T) {
	app := newProcessorTestApp(t)
	require.NoError(t, app.DB.Exec("DELETE FROM ai_tool_confirmations").Error)
	t.Cleanup(func() { app.DB.Exec("DELETE FROM ai_tool_confirmations") })
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511987654322"))
	s := models.ChatbotSettings{OrganizationID: org.ID, IsEnabled: true}
	s.AI = models.AIConfig{Enabled: true, Provider: models.AIProviderOpenAI}
	require.NoError(t, app.DB.Create(&s).Error)
	app.Config.AITools.Enabled, app.Config.AITools.WriteEnabled, app.Config.AITools.Providers = true, true, []string{"openai"}
	require.NoError(t, app.DB.Create(&models.AIToolSetting{OrganizationID: org.ID, ToolName: "request_agent_transfer", Enabled: true}).Error)

	old := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		StartedAt: time.Now().Add(-time.Hour), LastActivityAt: time.Now()}
	require.NoError(t, app.DB.Create(old).Error)
	token, conf, err := app.aiToolConfirmationStore().Propose(context.Background(), aitools.ProposeInput{
		Scope: aitools.Scope{OrganizationID: org.ID, ContactID: &contact.ID, SessionID: &old.ID, WhatsAppAccount: account.Name},
		RunID: uuid.New(), Tool: "request_agent_transfer", Args: models.JSONB{"reason": "x"},
	})
	require.NoError(t, err)

	// the contact is now in a newer session
	cur := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		StartedAt: time.Now(), LastActivityAt: time.Now()}
	require.NoError(t, app.DB.Create(cur).Error)

	app.processIncomingMessageFull(account.PhoneID, buttonTap(contact.PhoneNumber, "wamid.in.s1", "aitc:"+token), "Customer")

	var transfers []models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Find(&transfers).Error)
	assert.Empty(t, transfers, "an old confirmation cannot be used from another session")
	got, err := app.aiToolConfirmationStore().Get(context.Background(), conf.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AIConfirmationPending, got.Status, "nothing was consumed")
}
