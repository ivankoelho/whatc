package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const campaignOTP = "558201"

type authCampaignFixture struct {
	app      *handlers.App
	queue    *testutil.MockQueue
	org      *models.Organization
	user     *models.User
	campaign *models.BulkMessageCampaign
}

func newAuthCampaign(t *testing.T, status models.CampaignStatus) *authCampaignFixture {
	t.Helper()
	q := testutil.NewMockQueue()
	app := newTestApp(t, withQueue(q))
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("auth-camp")), testutil.WithPassword("password"))
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("auth-camp-"+uuid.New().String()[:6]))
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	require.NoError(t, app.DB.Model(tpl).Update("category", "AUTHENTICATION").Error)
	c := createTestCampaign(t, app, org.ID, tpl.ID, user.ID, account.Name, status)
	return &authCampaignFixture{app, q, org, user, c}
}

func (f *authCampaignFixture) raw(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, f.app.DB.Raw("SELECT COALESCE(CAST(template_params AS text),'') FROM bulk_message_recipients WHERE id = ?", id).Scan(&s).Error)
	return s
}

func (f *authCampaignFixture) importOne(t *testing.T) *fasthttp.RequestCtx {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{
		"recipients": []map[string]any{{"phone_number": "+5511999990000", "template_params": map[string]any{"1": campaignOTP}}},
	})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.ImportRecipients(req))
	return req.RequestCtx
}

func (f *authCampaignFixture) legacyRecipient(t *testing.T) *models.BulkMessageRecipient {
	t.Helper()
	r := createTestRecipient(t, f.app, f.campaign.ID, "+5511988880000", models.MessageStatusPending)
	require.NoError(t, f.app.DB.Model(r).UpdateColumn("template_params", models.JSONB{"1": campaignOTP}).Error)
	return r
}

func TestAuthCampaign_ImportSealsCodesAndAPIHidesThem(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	ctx := f.importOne(t)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

	var r models.BulkMessageRecipient
	require.NoError(t, f.app.DB.Where("campaign_id = ?", f.campaign.ID).First(&r).Error)
	assert.True(t, templateutil.IsSealed(r.TemplateParams))
	assert.NotContains(t, f.raw(t, r.ID), campaignOTP, "database holds no clear code")

	// The sealed value is still usable by the worker.
	opened, err := templateutil.OpenParams(r.TemplateParams, f.app.Config.App.EncryptionKey, time.Now())
	require.NoError(t, err)
	assert.Equal(t, campaignOTP, opened["1"])

	// API responses expose neither the code nor the sealed blob.
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.GetCampaignRecipients(req))
	body := string(testutil.GetResponseBody(req))
	assert.NotContains(t, body, campaignOTP)
	assert.NotContains(t, body, templateutil.SealedKey)
	var resp struct {
		Data struct {
			Recipients []map[string]any `json:"recipients"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.Len(t, resp.Data.Recipients, 1)
	assert.Nil(t, resp.Data.Recipients[0]["template_params"])
}

func TestAuthCampaign_ImportRefusesToStoreWithoutEncryptionKey(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	f.app.Config.App.EncryptionKey = ""
	ctx := f.importOne(t)
	assert.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())

	var n int64
	require.NoError(t, f.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", f.campaign.ID).Count(&n).Error)
	assert.Zero(t, n, "fail closed: nothing stored")
}

func TestAuthCampaign_StartSealsLegacyClearCodesBeforeQueueing(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	r := f.legacyRecipient(t)
	assert.Contains(t, f.raw(t, r.ID), campaignOTP)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.StartCampaign(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	assert.NotContains(t, f.raw(t, r.ID), campaignOTP, "pending legacy row is sealed in place")
	require.Len(t, f.queue.Jobs, 1)
	payload, _ := json.Marshal(f.queue.Jobs[0])
	assert.NotContains(t, string(payload), campaignOTP, "queue payload carries no clear code")
	opened, err := templateutil.OpenParams(f.queue.Jobs[0].TemplateParams, f.app.Config.App.EncryptionKey, time.Now())
	require.NoError(t, err)
	assert.Equal(t, campaignOTP, opened["1"], "pending send keeps working")
}

func TestAuthCampaign_CancelAndDeleteDropStoredCodes(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := newAuthCampaign(t, models.CampaignStatusPaused)
		r := f.legacyRecipient(t)
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		require.NoError(t, f.app.CancelCampaign(req))
		assert.NotContains(t, f.raw(t, r.ID), campaignOTP)
	})
	t.Run("delete campaign (soft delete keeps rows, not codes)", func(t *testing.T) {
		f := newAuthCampaign(t, models.CampaignStatusDraft)
		r := f.legacyRecipient(t)
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		require.NoError(t, f.app.DeleteCampaign(req))
		assert.NotContains(t, f.raw(t, r.ID), campaignOTP)
	})
	t.Run("delete recipient", func(t *testing.T) {
		f := newAuthCampaign(t, models.CampaignStatusDraft)
		r := f.legacyRecipient(t)
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		testutil.SetPathParam(req, "recipientId", r.ID.String())
		require.NoError(t, f.app.DeleteCampaignRecipient(req))
		assert.NotContains(t, f.raw(t, r.ID), campaignOTP)
	})
}

func TestNonAuthCampaign_RecipientParamsStayAsBefore(t *testing.T) {
	q := testutil.NewMockQueue()
	app := newTestApp(t, withQueue(q))
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("plain-camp")), testutil.WithPassword("password"))
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("plain-camp-"+uuid.New().String()[:6]))
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	c := createTestCampaign(t, app, org.ID, tpl.ID, user.ID, account.Name, models.CampaignStatusDraft)

	req := testutil.NewJSONRequest(t, map[string]any{
		"recipients": []map[string]any{{"phone_number": "+5511977770000", "template_params": map[string]any{"1": "John"}}},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", c.ID.String())
	require.NoError(t, app.ImportRecipients(req))

	var r models.BulkMessageRecipient
	require.NoError(t, app.DB.Where("campaign_id = ?", c.ID).First(&r).Error)
	assert.False(t, templateutil.IsSealed(r.TemplateParams))
	assert.Equal(t, "John", r.TemplateParams["1"])
}

func (f *authCampaignFixture) importWith(t *testing.T, rec map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"recipients": []map[string]any{rec}})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.ImportRecipients(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

func TestAuthCampaign_ValidUntilIsStoredInUTCWhateverTheOffset(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	future := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Second)
	brt := time.FixedZone("BRT", -3*3600)

	status, body := f.importWith(t, map[string]any{
		"phone_number": "+5511999990001", "template_params": map[string]any{"1": campaignOTP},
		"valid_until": future.In(brt).Format(time.RFC3339), // same instant, written in UTC-3
	})
	require.Equal(t, fasthttp.StatusOK, status, body)

	var r models.BulkMessageRecipient
	require.NoError(t, f.app.DB.Where("campaign_id = ?", f.campaign.ID).First(&r).Error)
	assert.Equal(t, future.Unix(), int64(r.TemplateParams[templateutil.SealedValidUntilKey].(float64)))
	assert.NotContains(t, f.raw(t, r.ID), campaignOTP)

	// The API never echoes the code or its validity back as clear params.
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.GetCampaignRecipients(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), campaignOTP)
}

func TestAuthCampaign_ValidUntilInThePastIsRejected(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	status, _ := f.importWith(t, map[string]any{
		"phone_number": "+5511999990002", "template_params": map[string]any{"1": campaignOTP},
		"valid_until": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	var n int64
	require.NoError(t, f.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", f.campaign.ID).Count(&n).Error)
	assert.Zero(t, n, "nothing stored for a code that could never be sent")
}

func TestAuthCampaign_ValidUntilRequirementIsOptInAndFailsSafe(t *testing.T) {
	// Default (transition): recipients without valid_until are still accepted.
	f := newAuthCampaign(t, models.CampaignStatusDraft)
	status, body := f.importWith(t, map[string]any{"phone_number": "+5511999990003", "template_params": map[string]any{"1": campaignOTP}})
	require.Equal(t, fasthttp.StatusOK, status, body)

	// Enforced: the whole request is refused and nothing is stored.
	g := newAuthCampaign(t, models.CampaignStatusDraft)
	g.app.Config.OTP.RequireValidUntil = true
	status, _ = g.importWith(t, map[string]any{"phone_number": "+5511999990004", "template_params": map[string]any{"1": campaignOTP}})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	var n int64
	require.NoError(t, g.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", g.campaign.ID).Count(&n).Error)
	assert.Zero(t, n)
}

func TestNonAuthCampaign_ValidUntilIsIgnoredAndNotRequired(t *testing.T) {
	q := testutil.NewMockQueue()
	app := newTestApp(t, withQueue(q))
	app.Config.OTP.RequireValidUntil = true // must not affect normal templates
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("plain-vu")), testutil.WithPassword("password"))
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("plain-vu-"+uuid.New().String()[:6]))
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	c := createTestCampaign(t, app, org.ID, tpl.ID, user.ID, account.Name, models.CampaignStatusDraft)

	req := testutil.NewJSONRequest(t, map[string]any{"recipients": []map[string]any{
		{"phone_number": "+5511977770001", "template_params": map[string]any{"1": "John"}},
		{"phone_number": "+5511977770002", "template_params": map[string]any{"1": "Ann"}, "valid_until": "2001-01-01T00:00:00Z"},
	}})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", c.ID.String())
	require.NoError(t, app.ImportRecipients(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var rs []models.BulkMessageRecipient
	require.NoError(t, app.DB.Where("campaign_id = ?", c.ID).Find(&rs).Error)
	require.Len(t, rs, 2)
	for _, r := range rs {
		assert.False(t, templateutil.IsSealed(r.TemplateParams), "normal params stay as before")
	}
}
