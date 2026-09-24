package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestListSalesOpportunities_AgentSeesOnlyOwn(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID)
	contactMine := testutil.CreateTestContact(t, app.DB, org.ID)
	contactTheirs := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactMine.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agent.ID, StageChangedAt: time.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactTheirs.ID, OpportunityNumber: "OPP-20260917-000002",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &other.ID, StageChangedAt: time.Now(),
	}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.ListSalesOpportunities(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Opportunities []models.SalesOpportunity `json:"opportunities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.Opportunities, 1)
	assert.Equal(t, contactMine.ID, resp.Data.Opportunities[0].ContactID)
	require.NotNil(t, resp.Data.Opportunities[0].Contact, "list response should preload Contact so the frontend can show a name instead of a UUID")
	assert.Equal(t, contactMine.ProfileName, resp.Data.Opportunities[0].Contact.ProfileName)
}

func TestListSalesOpportunities_ManagerWithViewAllSeesEverything(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	managerRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "manager",
		[]string{"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all"})
	manager := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&managerRole.ID))
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agent.ID, StageChangedAt: time.Now(),
	}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, manager.ID)
	require.NoError(t, app.ListSalesOpportunities(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Opportunities []models.SalesOpportunity `json:"opportunities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.Opportunities, 1)
}

func TestGetSalesOpportunity_403ForNonOwnerWithoutViewAll(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	owner := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &owner.ID, StageChangedAt: time.Now(),
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.GetSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestGetSalesOpportunity_404ForUnknownID(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", "00000000-0000-0000-0000-000000000000")
	require.NoError(t, app.GetSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestGetSalesOpportunity_OwnerCanView(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agent.ID, StageChangedAt: time.Now(),
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.GetSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data models.SalesOpportunity `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.NotNil(t, resp.Data.Contact, "get response should preload Contact so the frontend can show a name instead of a UUID")
	assert.Equal(t, contact.ProfileName, resp.Data.Contact.ProfileName)
}

func TestListSalesOpportunityEvents_ReturnsOpenedEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(opp).Update("assigned_user_id", agent.ID).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityEvents(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Data struct {
			Events []models.SalesOpportunityEvent `json:"events"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.Events, 1)
	assert.Equal(t, models.SalesOpportunityEventOpened, resp.Data.Events[0].Type)
}

func TestListSalesOpportunityEvents_403ForNonOwnerWithoutViewAll(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityEvents(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}
