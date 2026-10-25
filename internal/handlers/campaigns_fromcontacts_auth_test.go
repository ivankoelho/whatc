package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// L4: contacts carry no verification code, so adding them to an authentication
// campaign is refused with a clear message (and nothing is created).
func TestAuthCampaign_FromContactsIsRefusedWithAClearError(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	testutil.CreateTestContactWith(t, f.app.DB, f.org.ID, testutil.WithPhoneNumber("5571999990001"))

	req := testutil.NewJSONRequest(t, map[string]any{"ddds": []string{"71"}})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.AddRecipientsFromContacts(req))

	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, "one-time code per recipient")
	assert.NotContains(t, body, campaignOTP)

	var n int64
	require.NoError(t, f.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", f.campaign.ID).Count(&n).Error)
	assert.Zero(t, n, "no recipients without a usable code")
}

// Ordinary campaigns keep using it.
func TestNormalCampaign_FromContactsStillWorks(t *testing.T) {
	f := newNormalCampaign(t, models.CampaignStatusDraft)
	testutil.CreateTestContactWith(t, f.app.DB, f.org.ID, testutil.WithPhoneNumber("5571999990002"))

	req := testutil.NewJSONRequest(t, map[string]any{"ddds": []string{"71"}})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.AddRecipientsFromContacts(req))

	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var n int64
	require.NoError(t, f.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", f.campaign.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}
