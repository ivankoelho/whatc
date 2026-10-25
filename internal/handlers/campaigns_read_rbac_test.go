package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Campaign reads need campaigns:read: the route hook is a no-op, so each
// handler checks it. Covers list, detail, recipients and the media preview.
func TestCampaignReads_RequireCampaignsRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("rbac-read"))
	template := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)

	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&testutil.CreateAdminRole(t, app.DB, org.ID).ID))
	// A manager's campaigns:read comes from the default role; an agent has none.
	managerRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr-read", []string{"campaigns:read", "campaigns:write"})
	manager := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&managerRole.ID))
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	// An API key acts as its owner, so the owner's role decides: a chat-only owner.
	keyOwnerRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "key-owner", []string{"chat:read"})
	keyOwner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&keyOwnerRole.ID))

	campaign := createTestCampaign(t, app, org.ID, template.ID, admin.ID, account.Name, models.CampaignStatusDraft)
	createTestRecipient(t, app, campaign.ID, "5511999990001", models.MessageStatusPending)

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	otherAdmin := testutil.CreateTestUser(t, app.DB, otherOrg.ID, testutil.WithRoleID(&testutil.CreateAdminRole(t, app.DB, otherOrg.ID).ID))

	reads := map[string]func(*fastglue.Request) error{
		"ListCampaigns":         app.ListCampaigns,
		"GetCampaign":           app.GetCampaign,
		"GetCampaignRecipients": app.GetCampaignRecipients,
		"ServeCampaignMedia":    app.ServeCampaignMedia,
	}

	call := func(h func(*fastglue.Request) error, orgID, userID uuid.UUID) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)
		testutil.SetPathParam(req, "id", campaign.ID.String())
		require.NoError(t, h(req))
		return testutil.GetResponseStatusCode(req)
	}

	for name, h := range reads {
		t.Run(name, func(t *testing.T) {
			// Allowed: the response may still be 404 (no media file) but never 403.
			assert.NotEqual(t, fasthttp.StatusForbidden, call(h, org.ID, admin.ID), "admin")
			assert.NotEqual(t, fasthttp.StatusForbidden, call(h, org.ID, manager.ID), "manager")
			// Refused.
			assert.Equal(t, fasthttp.StatusForbidden, call(h, org.ID, agent.ID), "agent")
			assert.Equal(t, fasthttp.StatusForbidden, call(h, org.ID, keyOwner.ID), "API key of a chat-only owner")
		})
	}

	t.Run("cross-org admin cannot read another org's campaign", func(t *testing.T) {
		for _, name := range []string{"GetCampaign", "GetCampaignRecipients", "ServeCampaignMedia"} {
			assert.Equal(t, fasthttp.StatusNotFound, call(reads[name], otherOrg.ID, otherAdmin.ID), name)
		}
	})

	t.Run("list does not leak across orgs", func(t *testing.T) {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, otherOrg.ID, otherAdmin.ID)
		require.NoError(t, app.ListCampaigns(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		assert.NotContains(t, string(req.RequestCtx.Response.Body()), campaign.ID.String())
	})
}
