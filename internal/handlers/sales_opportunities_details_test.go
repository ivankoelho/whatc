package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Final review finding 3: nothing ever wrote interest/estimated_value/
// estimated_quantity, so they stayed null forever despite the spec saying
// they're "preenchidos pelo agente" (§4) and the dashboard's funnel-value
// widget sums estimated_value. These tests exercise the new
// PUT /sales-opportunities/{id}/details endpoint.

func TestUpdateSalesOpportunityDetails_SetsFields(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"interest": "Quer 50 caixas do produto X", "estimated_value": 1234.56, "estimated_quantity": 50,
	})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, "Quer 50 caixas do produto X", got.Interest)
	require.NotNil(t, got.EstimatedValue)
	assert.InDelta(t, 1234.56, *got.EstimatedValue, 0.001)
	require.NotNil(t, got.EstimatedQuantity)
	assert.Equal(t, 50, *got.EstimatedQuantity)

	// Plain field edit, not a funnel transition — no event (spec has no such
	// requirement for these fields, unlike direcionamento).
	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	assert.Len(t, events, 0)
}

func TestUpdateSalesOpportunityDetails_PartialUpdateOnlyTouchesProvidedFields(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{"interest": "original interest", "estimated_quantity": 10}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"estimated_value": 500})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, "original interest", got.Interest, "untouched field must survive a partial update")
	require.NotNil(t, got.EstimatedQuantity)
	assert.Equal(t, 10, *got.EstimatedQuantity, "untouched field must survive a partial update")
	require.NotNil(t, got.EstimatedValue)
	assert.InDelta(t, 500, *got.EstimatedValue, 0.001)
}

func TestUpdateSalesOpportunityDetails_RejectsWhenNotOpen(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("status", models.SalesOpportunityStatusConvertida).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"estimated_value": 999})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Nil(t, got.EstimatedValue, "a closed opportunity's fields must not change")
}

func TestUpdateSalesOpportunityDetails_ForbiddenForOtherAgentWithoutViewAll(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, owner.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"estimated_value": 100})
	testutil.SetAuthContext(req, org.ID, other.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}
