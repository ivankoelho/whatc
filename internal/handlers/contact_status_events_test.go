package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	wausage "github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// usageApp is a test app with usage recording on.
func usageApp(t *testing.T, opts ...appOption) *handlers.App {
	t.Helper()
	app := newTestApp(t, opts...)
	app.Usage = wausage.New(app.DB, config.UsageConfig{})
	return app
}

func statusEvents(t *testing.T, app *handlers.App, contactID uuid.UUID) []models.ContactStatusEvent {
	t.Helper()
	var evs []models.ContactStatusEvent
	require.NoError(t, app.DB.Where("contact_id = ?", contactID).Order("occurred_at").Find(&evs).Error)
	return evs
}

func TestContactStatusEvents_ManualAPIChange(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "in_progress"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, app.UpdateContactStatus(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	evs := statusEvents(t, app, contact.ID)
	require.Len(t, evs, 1)
	assert.Equal(t, "resolved", evs[0].FromStatus)
	assert.Equal(t, "in_progress", evs[0].ToStatus)
	assert.Equal(t, "agent", evs[0].ActorType)
	assert.Equal(t, &user.ID, evs[0].ActorUserID)
	assert.Equal(t, "api", evs[0].Reason)
	assert.Equal(t, org.ID, evs[0].OrganizationID)
}

func TestContactStatusEvents_ResolveAction(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, app.UpdateContactStatus(req))

	evs := statusEvents(t, app, contact.ID)
	require.Len(t, evs, 1, "exactly one event for one transition")
	assert.Equal(t, "new", evs[0].FromStatus)
	assert.Equal(t, "resolved", evs[0].ToStatus)
	assert.Equal(t, "resolve_action", evs[0].Reason)
	assert.Equal(t, &user.ID, evs[0].ActorUserID)
}

func TestContactStatusEvents_AutomaticCloseHasNoActor(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.ReleaseContactForTest(contact, nil, "sla"))

	evs := statusEvents(t, app, contact.ID)
	require.Len(t, evs, 1)
	assert.Equal(t, "system", evs[0].ActorType)
	assert.Nil(t, evs[0].ActorUserID)
	assert.Equal(t, "auto_resolve", evs[0].Reason)
}

func TestContactStatusEvents_NoChangeNoEvent(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

	changed, err := app.TransitionContactStatusForTest(contact, models.ContactStatusInProgress,
		[]models.ContactStatus{models.ContactStatusResolved}, nil)
	require.NoError(t, err)
	assert.False(t, changed)

	changed, err = app.TransitionContactStatusForTest(contact, models.ContactStatusNew, nil, nil)
	require.NoError(t, err)
	assert.False(t, changed, "already in that status")

	assert.Empty(t, statusEvents(t, app, contact.ID))
}

func TestContactStatusEvents_ARolledBackChangeLeavesNoOrphanEvent(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	rolledBack := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.ReleaseContactInTxForTest(rolledBack, nil, true))
	assert.Empty(t, statusEvents(t, app, rolledBack.ID), "a rolled-back change must not leave an event")
	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", rolledBack.ID).Error)
	assert.Equal(t, models.ContactStatusNew, stored.ContactStatus)

	committed := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.ReleaseContactInTxForTest(committed, nil, false))
	assert.Len(t, statusEvents(t, app, committed.ID), 1, "the committed change and its event go together")
}

func TestContactStatusEvents_AgentReplyOnTheFirstSend(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	app := newMsgTestApp(t, mockServer)
	app.Usage = wausage.New(app.DB, config.UsageConfig{})
	org := testutil.CreateTestOrganization(t, app.DB)
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	agentID := makeAgentUser(t, app, org.ID, "Milena Souza")

	opts := handlers.DefaultSendOptions()
	opts.SentByUserID = &agentID
	_, err := app.SendOutgoingMessage(testutil.TestContext(t), handlers.OutgoingMessageRequest{
		Account: account, Contact: contact, Type: models.MessageTypeText, Content: "Olá",
	}, opts)
	require.NoError(t, err)
	app.WaitForBackgroundTasks()

	evs := statusEvents(t, app, contact.ID)
	require.Len(t, evs, 1)
	assert.Equal(t, "new", evs[0].FromStatus)
	assert.Equal(t, "in_progress", evs[0].ToStatus)
	assert.Equal(t, "agent_reply", evs[0].Reason)
	assert.Equal(t, &agentID, evs[0].ActorUserID)
}

func TestContactStatusEvents_DisabledRecordsNothingAndChangesNothingElse(t *testing.T) {
	off := false
	app := newTestApp(t)
	app.Usage = wausage.New(app.DB, config.UsageConfig{RecordEnabled: &off})
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.ReleaseContactForTest(contact, nil, "sla"))

	assert.Empty(t, statusEvents(t, app, contact.ID))
	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus, "the status change itself is unaffected")
}

func TestContactStatusEvents_WithoutARecorderBehavesAsBefore(t *testing.T) {
	app := newTestApp(t) // Usage is nil
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.ReleaseContactForTest(contact, nil, "sla"))
	assert.Equal(t, models.ContactStatusResolved, contact.ContactStatus)
	assert.Empty(t, statusEvents(t, app, contact.ID))
}

// Fail-open: a history insert that fails must not abort the surrounding
// transaction nor undo the status change (F1).
func TestContactStatusEvents_FailureIsFailOpen(t *testing.T) {
	app := usageApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.TransitionInTxWithBadCauseForTest(contact, models.ContactStatusInProgress),
		"the transaction must stay usable after the history insert failed")

	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Equal(t, models.ContactStatusInProgress, stored.ContactStatus, "the status change was committed")
	assert.Equal(t, "written-after", stored.ProfileName, "later writes of the same transaction were committed")
	assert.Empty(t, statusEvents(t, app, contact.ID))
}
