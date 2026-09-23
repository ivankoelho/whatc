package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GATE 5. Fora da janela de 24h o envio é recusado com 422, antes de qualquer
// chamada à Meta.
func TestOccurrenceSendProtocol_RejectedOutsideServiceWindow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
	old := time.Now().Add(-30 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", old).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Fora da janela",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())

	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))

	var sent int64
	app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventProtocolSent).
		Count(&sent)
	assert.EqualValues(t, 0, sent, "nenhum evento de envio pode ser gravado")
}

// Contato que nunca enviou mensagem também está fora da janela.
func TestOccurrenceSendProtocol_RejectedWhenNeverInbound(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem inbound",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())

	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))
}

// GATE 4 aplicado ao envio: quem não enxerga o contato não envia.
func TestOccurrenceSendProtocol_DeniedForInvisibleContact(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	outsiderRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "occ-send-outsider",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write"})
	outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&outsiderRole.ID))
	enableStrictVisibility(t, app, org.ID)

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", owner.ID).Error)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", time.Now()).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Privada",
		StageID: stage.ID, OpenedByUserID: owner.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, outsider.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())

	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

// Dentro da janela: envia, grava o evento e a mensagem carrega o protocolo.
// Usa o mock de servidor WhatsApp já existente em messages_test.go (mesmo
// pacote handlers_test) — não é um mock novo.
func TestOccurrenceSendProtocol_SendsWithinWindow(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := createTestAccount(t, app, org.ID)

	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", time.Now()).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Dentro da janela",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())

	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), occ.ProtocolNumber)

	var evt models.OccurrenceEvent
	require.NoError(t, app.DB.Where("occurrence_id = ? AND type = ?",
		occ.ID, models.OccurrenceEventProtocolSent).First(&evt).Error)
	assert.Equal(t, occ.ProtocolNumber, evt.Content)

	// The send itself runs async (DefaultSendOptions); poll briefly for it to
	// land on the mock Meta server.
	require.Eventually(t, func() bool { return len(mockServer.sentMessages) == 1 },
		time.Second, 10*time.Millisecond)
	body := mockServer.sentMessages[0]["text"].(map[string]any)["body"].(string)
	assert.Contains(t, body, occ.ProtocolNumber)
}

// sendFixture prepares an occurrence whose contact is (or is not) inside the
// 24h service window, wired to the mock Meta server.
func sendFixture(t *testing.T, insideWindow bool) (*handlers.App, *mockWhatsAppServer, *models.Organization, *models.User, *models.Occurrence, *models.Contact) {
	t.Helper()
	mockServer := newMockWhatsAppServer()
	t.Cleanup(mockServer.close)

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := createTestAccount(t, app, org.ID)

	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
	inbound := time.Now()
	if !insideWindow {
		inbound = time.Now().Add(-30 * time.Hour)
	}
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", inbound).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Mensagem custom",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))
	return app, mockServer, org, user, &occ, contact
}

// sendProtocol calls the handler with the given raw body (nil = no body at
// all) and returns the request.
func sendProtocol(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, occ *models.Occurrence, rawBody []byte) *fastglue.Request {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	if rawBody != nil {
		req.RequestCtx.Request.SetBody(rawBody)
	}
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.SendOccurrenceProtocol(req))
	return req
}

func lastSentText(t *testing.T, m *mockWhatsAppServer) string {
	t.Helper()
	require.Eventually(t, func() bool { return len(m.sentMessages) == 1 },
		time.Second, 10*time.Millisecond)
	return m.sentMessages[0]["text"].(map[string]any)["body"].(string)
}

func assertLegacyProtocolText(t *testing.T, app *handlers.App, m *mockWhatsAppServer, contact *models.Contact, occ *models.Occurrence) {
	t.Helper()
	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Order("created_at DESC").First(&msg).Error)
	assert.Contains(t, msg.Content, occ.ProtocolNumber)
	assert.Contains(t, msg.Content, "Guarde este número")
	assert.Contains(t, lastSentText(t, m), "Guarde este número")
}

func TestSendOccurrenceProtocol_NoBody_KeepsLegacyText(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, true)
	req := sendProtocol(t, app, org.ID, user.ID, occ, nil)
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assertLegacyProtocolText(t, app, m, contact, occ)
}

func TestSendOccurrenceProtocol_WithMessage_SendsExactlyThatText(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, true)
	const custom = "Mensagem revisada pelo atendente."
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":"`+custom+`"}`))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Order("created_at DESC").First(&msg).Error)
	assert.Equal(t, custom, msg.Content)
	assert.Equal(t, custom, lastSentText(t, m))

	// The audit event keeps recording the protocol number, not the free text.
	var evt models.OccurrenceEvent
	require.NoError(t, app.DB.Where("occurrence_id = ? AND type = ?",
		occ.ID, models.OccurrenceEventProtocolSent).First(&evt).Error)
	assert.Equal(t, occ.ProtocolNumber, evt.Content)
}

func TestSendOccurrenceProtocol_EmptyMessage_FallsBackToLegacyText(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, true)
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":""}`))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assertLegacyProtocolText(t, app, m, contact, occ)
}

func TestSendOccurrenceProtocol_BlankMessage_FallsBackToLegacyText(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, true)
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":"   \n\t "}`))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assertLegacyProtocolText(t, app, m, contact, occ)
}

func TestSendOccurrenceProtocol_MessageTooLong_Rejected(t *testing.T) {
	app, m, org, user, occ, _ := sendFixture(t, true)
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":"`+strings.Repeat("a", 4097)+`"}`))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	assert.Empty(t, m.sentMessages)

	// Exactly at the limit is accepted.
	app2, m2, org2, user2, occ2, _ := sendFixture(t, true)
	req = sendProtocol(t, app2, org2.ID, user2.ID, occ2, []byte(`{"message":"`+strings.Repeat("a", 4096)+`"}`))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Len(t, lastSentText(t, m2), 4096)
}

func TestSendOccurrenceProtocol_MalformedBody_Rejected(t *testing.T) {
	app, m, org, user, occ, _ := sendFixture(t, true)
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":`))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	assert.Empty(t, m.sentMessages)
}

func TestSendOccurrenceProtocol_WithMessage_StillRejectedOutsideWindow(t *testing.T) {
	app, m, org, user, occ, _ := sendFixture(t, false)
	req := sendProtocol(t, app, org.ID, user.ID, occ, []byte(`{"message":"texto livre"}`))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))
	assert.Empty(t, m.sentMessages)

	var sent int64
	app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventProtocolSent).Count(&sent)
	assert.EqualValues(t, 0, sent)
}
