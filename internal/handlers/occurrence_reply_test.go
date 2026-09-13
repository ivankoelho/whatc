package handlers_test

import (
	"testing"
	"time"

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
