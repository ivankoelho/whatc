package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// attemptMessage is the record of one earlier send attempt of a one-time code
// (what the worker's claim creates): masked, tied to the campaign, already over.
func (f *authCampaignFixture) attemptMessage(t *testing.T, phone, errText string) *models.Message {
	t.Helper()
	contact := testutil.CreateTestContactWith(t, f.app.DB, f.org.ID, testutil.WithPhoneNumber(phone))
	msg := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusFailed,
		Content: "Seu código é ••••••", ErrorMessage: errText,
		Metadata: models.JSONB{"campaign_id": f.campaign.ID.String(), "auth_code_masked": true},
	}
	require.NoError(t, f.app.DB.Create(msg).Error)
	return msg
}

func (f *authCampaignFixture) message(t *testing.T, id uuid.UUID) models.Message {
	t.Helper()
	var m models.Message
	require.NoError(t, f.app.DB.First(&m, id).Error)
	return m
}

func (f *authCampaignFixture) campaignFailedCount(t *testing.T) int {
	t.Helper()
	var c models.BulkMessageCampaign
	require.NoError(t, f.app.DB.First(&c, f.campaign.ID).Error)
	return c.FailedCount
}

// B6. An attempt that ended for good (Meta refused it, the claim was released) stays
// in the history as failed. Resetting it to "pending" would leave a message nobody will
// ever send, and it would no longer show what happened.
func TestRetryFailed_FinishedOTPAttemptsStayFailedInTheHistory(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	refused := createTestRecipient(t, f.app, f.campaign.ID, "+5511930000001", models.MessageStatusFailed)
	old := f.attemptMessage(t, "+5511930000001", "meta_refused: invalid parameter")
	// The claim was released: the recipient points at no message any more.
	require.NoError(t, f.app.DB.Model(refused).UpdateColumn("error_message", "meta_refused: invalid parameter").Error)

	status, data := f.retry(t)

	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	require.Len(t, f.queue.Jobs, 1)
	assert.Equal(t, refused.ID, f.queue.Jobs[0].RecipientID)
	m := f.message(t, old.ID)
	assert.Equal(t, models.MessageStatusFailed, m.Status, "the finished attempt is history, not work to do")
	assert.Equal(t, "meta_refused: invalid parameter", m.ErrorMessage)
	var r models.BulkMessageRecipient
	require.NoError(t, f.app.DB.First(&r, refused.ID).Error)
	assert.Equal(t, models.MessageStatusPending, r.Status, "the recipient itself is retried")
}

// Counters: after a retry the failed count is what is still failed (the uncertain
// deliveries), not one per historical attempt.
func TestRetryFailed_CountersIgnoreFinishedAttemptsOfRetriedRecipients(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	createTestRecipient(t, f.app, f.campaign.ID, "+5511930000002", models.MessageStatusFailed)
	f.attemptMessage(t, "+5511930000002", "meta_refused: invalid parameter")
	f.uncertainRecipient(t, "+5511930000003")

	status, data := f.retry(t)

	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	assert.Equal(t, 1, f.campaignFailedCount(t), "only the uncertain delivery is still failed")
}

// The protection of uncertain deliveries does not change: their record stays failed and
// their claim stays.
func TestRetryFailed_UncertainDeliveryIsStillProtectedNextToAttemptRecords(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	createTestRecipient(t, f.app, f.campaign.ID, "+5511930000004", models.MessageStatusFailed)
	f.attemptMessage(t, "+5511930000004", "meta_transient: throttled")
	uncertain, uncertainMsg := f.uncertainRecipient(t, "+5511930000005")

	status, _ := f.retry(t)

	require.Equal(t, fasthttp.StatusOK, status)
	require.Len(t, f.queue.Jobs, 1)
	assert.NotEqual(t, uncertain.ID, f.queue.Jobs[0].RecipientID)
	assert.Equal(t, models.MessageStatusFailed, f.message(t, uncertainMsg.ID).Status)
	var u models.BulkMessageRecipient
	require.NoError(t, f.app.DB.First(&u, uncertain.ID).Error)
	require.NotNil(t, u.MessageID)
	assert.Equal(t, models.MessageStatusFailed, u.Status)
}

// Ordinary campaigns are untouched: their failed messages still go back to pending, and
// the failed counter is still taken from the messages table.
func TestRetryFailed_NormalCampaignMessagesStillGoBackToPending(t *testing.T) {
	f := newNormalCampaign(t, models.CampaignStatusCompleted)
	createTestRecipient(t, f.app, f.campaign.ID, "+5511930000006", models.MessageStatusFailed)
	contact := testutil.CreateTestContactWith(t, f.app.DB, f.org.ID, testutil.WithPhoneNumber("+5511930000006"))
	msg := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusFailed,
		ErrorMessage: "boom", Metadata: models.JSONB{"campaign_id": f.campaign.ID.String()},
	}
	require.NoError(t, f.app.DB.Create(msg).Error)
	failedStillThere := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusSent,
		Metadata: models.JSONB{"campaign_id": f.campaign.ID.String()},
	}
	require.NoError(t, f.app.DB.Create(failedStillThere).Error)

	status, data := f.retry(t)

	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	m := f.message(t, msg.ID)
	assert.Equal(t, models.MessageStatusPending, m.Status)
	assert.Empty(t, m.ErrorMessage)
	assert.Equal(t, 0, f.campaignFailedCount(t))
}
