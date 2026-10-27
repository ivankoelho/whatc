package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// uncertainRecipient is a failed recipient that still holds its claim (message_id):
// a code whose delivery nobody confirmed.
func (f *authCampaignFixture) uncertainRecipient(t *testing.T, phone string) (*models.BulkMessageRecipient, *models.Message) {
	t.Helper()
	contact := testutil.CreateTestContactWith(t, f.app.DB, f.org.ID, testutil.WithPhoneNumber(phone))
	msg := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusFailed,
		Content: "Seu código é ••••••", ErrorMessage: "delivery_uncertain: timeout",
		Metadata: models.JSONB{"campaign_id": f.campaign.ID.String()},
	}
	require.NoError(t, f.app.DB.Create(msg).Error)
	r := createTestRecipient(t, f.app, f.campaign.ID, phone, models.MessageStatusFailed)
	until := time.Now().Add(10 * time.Minute)
	sealed, err := templateutil.SealParamsUntil(map[string]any{"1": campaignOTP}, f.app.Config.App.EncryptionKey, time.Now(), &until)
	require.NoError(t, err)
	require.NoError(t, f.app.DB.Model(r).UpdateColumns(map[string]any{
		"message_id": msg.ID, "error_message": "delivery_uncertain: timeout", "template_params": models.JSONB(sealed),
	}).Error)
	return r, msg
}

// adminOperator is a real, active user of the campaign's organization whose role may edit campaigns.
func (f *authCampaignFixture) adminOperator(t *testing.T) *models.User {
	t.Helper()
	role := testutil.CreateAdminRole(t, f.app.DB, f.org.ID)
	return testutil.CreateTestUser(t, f.app.DB, f.org.ID, testutil.WithRoleID(&role.ID), testutil.WithEmail(testutil.UniqueEmail("otp-operator")))
}

func (f *authCampaignFixture) retry(t *testing.T) (int, map[string]any) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.RetryFailed(req))
	var resp struct {
		Data    map[string]any `json:"data"`
		Message string         `json:"message"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	if resp.Data == nil {
		resp.Data = map[string]any{"message": resp.Message, "raw": string(testutil.GetResponseBody(req))}
	}
	return testutil.GetResponseStatusCode(req), resp.Data
}

// A definite failure is retried; an uncertain delivery is never retried automatically
// and its message is not reset to "pending" either.
func TestRetryFailed_SkipsUncertainDeliveriesAndRetriesTheRest(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	definite := createTestRecipient(t, f.app, f.campaign.ID, "+5511940000001", models.MessageStatusFailed)
	uncertain, uncertainMsg := f.uncertainRecipient(t, "+5511940000002")

	status, data := f.retry(t)

	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	assert.EqualValues(t, 1, data["retry_count"])
	assert.EqualValues(t, 1, data["skipped_uncertain"])
	require.Len(t, f.queue.Jobs, 1)
	assert.Equal(t, definite.ID, f.queue.Jobs[0].RecipientID, "only the definite failure is queued")

	var u models.BulkMessageRecipient
	require.NoError(t, f.app.DB.First(&u, uncertain.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, u.Status, "still failed")
	require.NotNil(t, u.MessageID, "the claim is kept")
	var m models.Message
	require.NoError(t, f.app.DB.First(&m, uncertainMsg.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, m.Status, "its message is not reset to pending")
}

func TestRetryFailed_OnlyUncertainDeliveriesIsRefusedWithAClearReason(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	f.uncertainRecipient(t, "+5511940000003")

	status, data := f.retry(t)

	assert.Equal(t, fasthttp.StatusConflict, status)
	assert.Contains(t, data["raw"], "uncertain delivery")
	assert.Empty(t, f.queue.Jobs, "nothing is queued")
}

// Releasing is the deliberate human step: after it, retry-failed sends again, and not before.
func TestReconciliation_ReleaseMakesAnUncertainRecipientRetryable(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	r, _ := f.uncertainRecipient(t, "+5511940000004")
	op := f.adminOperator(t)
	policy := templateutil.OTPPolicy{}

	list, err := authmask.ListUncertain(f.app.DB, 2*time.Minute)
	require.NoError(t, err)
	var found bool
	for _, u := range list {
		if u.RecipientID == r.ID {
			found = true
			assert.Equal(t, "0004", u.PhoneLast4, "the phone is shown masked")
			assert.Equal(t, authmask.StateUncertain, u.State)
		}
	}
	require.True(t, found)

	req := authmask.ReleaseRequest{LocalReleaseEnabled: true, RecipientID: r.ID, OperatorEmail: op.Email, Reason: "Meta message log shows no delivery for this wamid"}
	require.NoError(t, authmask.ReleaseUncertain(f.app.DB, req, policy, time.Now(), false))
	assert.Empty(t, f.queue.Jobs, "releasing queues nothing: only an explicit retry-failed sends again")
	assert.ErrorIs(t, authmask.ReleaseUncertain(f.app.DB, req, policy, time.Now(), false), authmask.ErrNotUncertain, "compare-and-set: a second release does nothing")

	// The audit entry is real, attributed to the operator, and carries no code.
	var audits []models.AuditLog
	require.NoError(t, f.app.DB.Where("organization_id = ? AND resource_id = ?", f.org.ID, f.campaign.ID).Find(&audits).Error)
	require.Len(t, audits, 1, "one release, one audit entry (the second attempt wrote none)")
	assert.Equal(t, op.ID, audits[0].UserID)
	assert.Equal(t, op.FullName, audits[0].UserName)
	raw, _ := json.Marshal(audits[0].Changes)
	assert.Contains(t, string(raw), r.ID.String())
	assert.Contains(t, string(raw), "Meta message log shows no delivery")
	assert.NotContains(t, string(raw), campaignOTP)

	status, data := f.retry(t)
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	require.Len(t, f.queue.Jobs, 1)
	assert.Equal(t, r.ID, f.queue.Jobs[0].RecipientID)
}
