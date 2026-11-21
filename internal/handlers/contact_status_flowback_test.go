package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Concluding an attendance means: the human attendance ends and the conversation goes back to the
// automatic flow. While a person has the conversation nothing automatic is sent; after the conclusion the
// next message of the customer is handled by the chatbot again. (It is not a permanent block of the bot.)

func inboundText(from, wamid, body string) IncomingTextMessage {
	var m IncomingTextMessage
	raw, _ := json.Marshal(map[string]any{"from": from, "id": wamid, "timestamp": "1700000000", "type": "text", "text": map[string]any{"body": body}})
	_ = json.Unmarshal(raw, &m)
	return m
}

func countMessages(t *testing.T, app *App, contactID uuid.UUID, direction models.Direction) int64 {
	t.Helper()
	var n int64
	require.NoError(t, app.DB.Model(&models.Message{}).
		Where("contact_id = ? AND direction = ? AND sent_by_user_id IS NULL", contactID, direction).Count(&n).Error)
	return n
}

func TestConclude_ReturnsTheConversationToTheAutomaticFlow(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, IsEnabled: true,
		DefaultResponse: "Olá! Sou o atendimento automático.", SessionTimeoutMins: 30,
	}).Error)
	app.InvalidateChatbotSettingsCache(org.ID)

	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	// The customer is found by the SAME normalized phone the webhook uses (a hand-made fixture contact with
	// another phone format would make the webhook create a second contact, and every check would be vacuous).
	const phone = "5511977771234"
	contact, _, err := contactutil.GetOrCreateContact(app.DB, org.ID, phone, "Cliente")
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID, "contact_status": models.ContactStatusInProgress, "whats_app_account": account.Name,
	}).Error)
	tr := &models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, PhoneNumber: contact.PhoneNumber,
		WhatsAppAccount: account.Name, Status: models.TransferStatusActive, Source: models.TransferSourceManual, AgentID: &agent.ID, TransferredAt: time.Now(),
	}
	require.NoError(t, app.DB.Create(tr).Error)

	// 10. while a person has the conversation nothing automatic is sent, however many times the customer writes
	app.processIncomingMessageFull(account.PhoneID, inboundText(phone, "wamid.during.1", "oi, alguém aí?"), "Cliente")
	app.processIncomingMessageFull(account.PhoneID, inboundText(phone, "wamid.during.2", "ainda preciso de ajuda"), "Cliente")
	require.EqualValues(t, 2, countMessages(t, app, contact.ID, models.DirectionIncoming), "the customer's messages reached THIS contact")
	assert.Zero(t, countMessages(t, app, contact.ID, models.DirectionOutgoing), "no automatic reply during the human attendance")

	// the attendant concludes it (the agent role as shipped: no contacts:write)
	req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, app.UpdateContactStatus(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	// 8. the human attendance is over and the conversation is back to the automatic flow
	assert.False(t, app.hasActiveAgentTransfer(org.ID, contact.ID))
	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", tr.ID).Error)
	assert.Equal(t, models.TransferStatusResumed, stored.Status)
	assert.Zero(t, countMessages(t, app, contact.ID, models.DirectionOutgoing), "concluding sends nothing by itself")

	// 9. a new message of the customer starts the automatic flow again
	app.processIncomingMessageFull(account.PhoneID, inboundText(phone, "wamid.after.1", "oi, tenho outra dúvida"), "Cliente")
	assert.EqualValues(t, 3, countMessages(t, app, contact.ID, models.DirectionIncoming))
	assert.EqualValues(t, 1, countMessages(t, app, contact.ID, models.DirectionOutgoing), "the chatbot answers the next message after the conclusion")
}
