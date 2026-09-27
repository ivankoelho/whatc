package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestCreateSalesOpportunity_CreatesManualOpportunity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", agent.ID).Error)

	value := 4200.0
	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":      contact.ID.String(),
		"interest":        "Piso laminado 60m2",
		"estimated_value": value,
	})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&opp).Error)
	assert.Equal(t, contact.ID, opp.ContactID)
	assert.Equal(t, "Piso laminado 60m2", opp.Interest)
	require.NotNil(t, opp.EstimatedValue)
	assert.Equal(t, value, *opp.EstimatedValue)
	assert.Equal(t, models.SalesOpportunityStagePotencial, opp.Stage)
	assert.Equal(t, models.SalesOpportunityStatusAberta, opp.Status)
	require.NotNil(t, opp.AssignedUserID)
	assert.Equal(t, agent.ID, *opp.AssignedUserID, "assignee stays the contact's own assigned user, not necessarily the creator")

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventOpened, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventSourceManual, events[0].Source)
}

func TestCreateSalesOpportunity_ContactIDRequired(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"interest": "sem contato"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestCreateSalesOpportunity_RequiresWritePermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "readonly", []string{"sales_opportunities:read"})
	viewer := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String()})
	testutil.SetAuthContext(req, org.ID, viewer.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestCreateSalesOpportunity_RetriggersExistingOpenOpportunity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	existing, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "interest": "novo pedido"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).Where("contact_id = ?", contact.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "must not create a second open opportunity for the same contact")

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.First(&opp, "id = ?", existing.ID).Error)
	assert.NotEqual(t, "novo pedido", opp.Interest, "retrigger must not overwrite the existing opportunity's fields")
}
