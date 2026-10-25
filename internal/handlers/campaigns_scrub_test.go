package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNormalCampaign is newAuthCampaign with an ordinary (UTILITY) template.
func newNormalCampaign(t *testing.T, status models.CampaignStatus) *authCampaignFixture {
	t.Helper()
	q := testutil.NewMockQueue()
	app := newTestApp(t, withQueue(q))
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("normal-camp")), testutil.WithPassword("password"))
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("normal-camp-"+uuid.New().String()[:6]))
	tpl := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	c := createTestCampaign(t, app, org.ID, tpl.ID, user.ID, account.Name, status)
	return &authCampaignFixture{app, q, org, user, c}
}

func (f *authCampaignFixture) recipientWithParams(t *testing.T, params models.JSONB) *models.BulkMessageRecipient {
	t.Helper()
	r := createTestRecipient(t, f.app, f.campaign.ID, "+5511966660000", models.MessageStatusPending)
	require.NoError(t, f.app.DB.Model(r).UpdateColumn("template_params", params).Error)
	return r
}

func (f *authCampaignFixture) paramsOf(t *testing.T, id uuid.UUID) string {
	t.Helper()
	return f.raw(t, id)
}

// H1: ordinary campaigns keep their per-recipient params when cancelled or deleted.
func TestNormalCampaign_CancelAndDeleteKeepRecipientParams(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := newNormalCampaign(t, models.CampaignStatusPaused)
		r := f.recipientWithParams(t, models.JSONB{"1": "John", "2": "ORD-9"})
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		require.NoError(t, f.app.CancelCampaign(req))
		assert.Contains(t, f.paramsOf(t, r.ID), "ORD-9")
	})
	t.Run("delete campaign", func(t *testing.T) {
		f := newNormalCampaign(t, models.CampaignStatusDraft)
		r := f.recipientWithParams(t, models.JSONB{"1": "John", "2": "ORD-9"})
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		require.NoError(t, f.app.DeleteCampaign(req))
		assert.Contains(t, f.paramsOf(t, r.ID), "ORD-9", "soft delete keeps the data it always kept")
	})
	t.Run("delete recipient", func(t *testing.T) {
		f := newNormalCampaign(t, models.CampaignStatusDraft)
		r := f.recipientWithParams(t, models.JSONB{"1": "John", "2": "ORD-9"})
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		testutil.SetPathParam(req, "recipientId", r.ID.String())
		require.NoError(t, f.app.DeleteCampaignRecipient(req))
		assert.Contains(t, f.paramsOf(t, r.ID), "ORD-9")
	})
}

// A sealed envelope left on a campaign whose template is no longer an
// authentication one (the category was changed after the import) is a secret
// and goes; the ordinary recipients of the same campaign stay.
func TestNormalCampaign_CancelStillDropsSealedEnvelopes(t *testing.T) {
	f := newNormalCampaign(t, models.CampaignStatusPaused)
	plain := f.recipientWithParams(t, models.JSONB{"1": "John"})
	sealed, err := templateutil.SealParams(map[string]any{"1": campaignOTP}, f.app.Config.App.EncryptionKey, time.Now())
	require.NoError(t, err)
	other := createTestRecipient(t, f.app, f.campaign.ID, "+5511955550000", models.MessageStatusPending)
	require.NoError(t, f.app.DB.Model(other).UpdateColumn("template_params", models.JSONB(sealed)).Error)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.CancelCampaign(req))

	assert.Contains(t, f.paramsOf(t, plain.ID), "John")
	assert.NotContains(t, f.paramsOf(t, other.ID), templateutil.SealedKey)
}

// Authentication secrets are dropped from every recipient row, including the
// ones that are already soft-deleted.
func TestAuthCampaign_CancelDropsSecretsOfSoftDeletedRecipientsToo(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusPaused)
	live := f.legacyRecipient(t)
	gone := f.legacyRecipient(t)
	require.NoError(t, f.app.DB.Delete(&models.BulkMessageRecipient{}, gone.ID).Error) // soft delete first

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, f.org.ID, f.user.ID)
	testutil.SetPathParam(req, "id", f.campaign.ID.String())
	require.NoError(t, f.app.CancelCampaign(req))

	assert.NotContains(t, f.raw(t, live.ID), campaignOTP)
	assert.NotContains(t, f.raw(t, gone.ID), campaignOTP, "soft-deleted rows are scrubbed as well")
}
