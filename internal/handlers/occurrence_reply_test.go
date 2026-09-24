package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestReplyToOccurrence_RejectedOutsideServiceWindow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	old := time.Now().Add(-30 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", old).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Fora da janela",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Olá, tudo bem?"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(req))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.Nil(t, reloaded.SLA.FirstResponseAt, "a rejected reply must not stamp first response")

	var replyEvents int64
	app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventReply).Count(&replyEvents)
	assert.EqualValues(t, 0, replyEvents, "a rejected reply must not record a reply event")
}

func TestReplyToOccurrence_SetsFirstResponseOnlyOnce(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := createTestAccount(t, app, org.ID)

	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	recent := time.Now().Add(-1 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", recent).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Peça quebrada",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	first := testutil.NewJSONRequest(t, map[string]any{"content": "Já estamos verificando"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	testutil.SetPathParam(first, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(first))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	var afterFirst models.Occurrence
	require.NoError(t, app.DB.First(&afterFirst, "id = ?", occ.ID).Error)
	require.NotNil(t, afterFirst.SLA.FirstResponseAt)
	require.NotNil(t, afterFirst.FirstResponseByID)
	assert.Equal(t, user.ID, *afterFirst.FirstResponseByID)
	firstStamp := *afterFirst.SLA.FirstResponseAt

	second := testutil.NewJSONRequest(t, map[string]any{"content": "Update: chegou hoje"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	testutil.SetPathParam(second, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(second))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second))

	var afterSecond models.Occurrence
	require.NoError(t, app.DB.First(&afterSecond, "id = ?", occ.ID).Error)
	assert.Equal(t, firstStamp, *afterSecond.SLA.FirstResponseAt, "second reply must not move first_response_at")

	var replyEvents int64
	app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventReply).Count(&replyEvents)
	assert.EqualValues(t, 2, replyEvents)

	// Finding 5 of the final branch review: OccurrenceResponse never exposed
	// first_response_at/first_response_by_id, even though GetOccurrence funnels
	// through it. Confirm the fix by reading the occurrence back through the API.
	getReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(getReq, org.ID, user.ID)
	testutil.SetPathParam(getReq, "id", occ.ID.String())
	require.NoError(t, app.GetOccurrence(getReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(getReq))

	var getResp struct {
		Data handlers.OccurrenceResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(getReq), &getResp))
	require.NotNil(t, getResp.Data.FirstResponseAt)
	assert.Equal(t, firstStamp.Unix(), getResp.Data.FirstResponseAt.Unix())
	require.NotNil(t, getResp.Data.FirstResponseByID)
	assert.Equal(t, user.ID, *getResp.Data.FirstResponseByID)
}

func TestCreateOccurrenceEvent_NoteNeverSetsFirstResponse(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Caso comum",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Aguardando fornecedor confirmar"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.CreateOccurrenceEvent(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.Nil(t, reloaded.SLA.FirstResponseAt, "an internal note must never set first_response_at")
	assert.Nil(t, reloaded.FirstResponseByID, "an internal note must never set first_response_by_id")
}

// replyScenario sets up an agent ("Milena Souza") with the org's agent-name
// signature on, and an occurrence for a contact inside the 24h window.
func replyScenario(t *testing.T, app *handlers.App) (org *models.Organization, agent *models.User, contact *models.Contact, occ models.Occurrence) {
	t.Helper()
	org = testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID), testutil.WithFullName("Milena Souza"))
	account := createTestAccount(t, app, org.ID)
	contact = testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"last_inbound_at": time.Now().Add(-1 * time.Hour), "assigned_user_id": agent.ID,
	}).Error)
	enableAgentNameSignature(t, app, org.ID)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ = models.Occurrence{OrganizationID: org.ID, ContactID: contact.ID, Title: "Troca", StageID: stage.ID, OpenedByUserID: agent.ID}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))
	return
}

func activeTransfers(t *testing.T, app *handlers.App, contactID any) []models.AgentTransfer {
	t.Helper()
	var transfers []models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ? AND status = ?", contactID, models.TransferStatusActive).Find(&transfers).Error)
	return transfers
}

// A reply typed in the protocol is human conversation: signed, attributed to
// the agent, and it opens the attendance so the customer's answer reaches a
// person instead of the chatbot.
func TestReplyToOccurrence_IsSignedAndOpensAttendance(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	app := newMsgTestApp(t, mockServer)
	org, agent, contact, occ := replyScenario(t, app)
	require.Empty(t, activeTransfers(t, app, contact.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Conseguimos a troca. Pode confirmar o endereço?"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	app.WaitForBackgroundTasks()

	assert.Equal(t, "*Milena:*\nConseguimos a troca. Pode confirmar o endereço?", sentTextBody(t, mockServer))

	transfers := activeTransfers(t, app, contact.ID)
	require.Len(t, transfers, 1, "the reply opens an attendance")
	require.NotNil(t, transfers[0].AgentID)
	assert.Equal(t, agent.ID, *transfers[0].AgentID)

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ? AND direction = ?", contact.ID, models.DirectionOutgoing).First(&msg).Error)
	require.NotNil(t, msg.SentByUserID)
	assert.Equal(t, agent.ID, *msg.SentByUserID)
	assert.Equal(t, "Conseguimos a troca. Pode confirmar o endereço?", msg.Content, "stored text stays unsigned")

	var after models.Contact
	require.NoError(t, app.DB.First(&after, "id = ?", contact.ID).Error)
	assert.Equal(t, models.ContactStatusInProgress, after.ContactStatus)
}

// When another agent already owns the conversation, replying from the protocol
// does not take it over; only the message carries the replying agent's name.
func TestReplyToOccurrence_KeepsExistingAttendanceOwner(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	app := newMsgTestApp(t, mockServer)
	org, agent, contact, occ := replyScenario(t, app)
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithFullName("Carlos Dono"))
	require.NoError(t, app.DB.Create(&models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: contact.WhatsAppAccount,
		PhoneNumber: contact.PhoneNumber, Status: models.TransferStatusActive, AgentID: &owner.ID,
		Source: models.TransferSourceAgentInitiated, TransferredAt: time.Now(),
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Atualização da troca"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	app.WaitForBackgroundTasks()

	assert.Equal(t, "*Milena:*\nAtualização da troca", sentTextBody(t, mockServer))
	transfers := activeTransfers(t, app, contact.ID)
	require.Len(t, transfers, 1)
	assert.Equal(t, owner.ID, *transfers[0].AgentID, "the conversation stays with its owner")
}

// Sending the protocol is the company speaking: never signed, never opens an
// attendance, never changes the contact status.
func TestSendOccurrenceProtocol_IsSystemMessage(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	app := newMsgTestApp(t, mockServer)
	org, agent, contact, occ := replyScenario(t, app)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.SendOccurrenceProtocol(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	app.WaitForBackgroundTasks()

	assert.NotContains(t, sentTextBody(t, mockServer), "Milena")
	assert.Empty(t, activeTransfers(t, app, contact.ID))
	var after models.Contact
	require.NoError(t, app.DB.First(&after, "id = ?", contact.ID).Error)
	assert.Equal(t, contact.ContactStatus, after.ContactStatus)
}
