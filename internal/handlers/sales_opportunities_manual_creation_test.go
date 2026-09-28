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
	assert.Equal(t, agent.ID, *opp.AssignedUserID, "assignee is the creating agent")

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

// Regression test: an earlier build inherited contact.AssignedUserID (the
// chatbot automatic-creation rule) for manual creation too, so an agent who
// created an opportunity from a colleague's contact never saw it on their
// own board — it silently went to the colleague instead. Spec item E's
// approved answer is "responsável = usuário que criou".
func TestCreateSalesOpportunity_AssigneeIsCreatorNotContactsExistingAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	colleague := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	creator := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", colleague.ID).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "interest": "teste"})
	testutil.SetAuthContext(req, org.ID, creator.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&opp).Error)
	require.NotNil(t, opp.AssignedUserID)
	assert.Equal(t, creator.ID, *opp.AssignedUserID, "must belong to the creating agent, not the contact's own assignee")
}

// The creator override must never apply to a retrigger — that would let
// anyone steal an existing open opportunity away from its real owner just
// by clicking "Criar oportunidade" on someone else's contact.
func TestCreateSalesOpportunity_RetriggerDoesNotReassignOwnership(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	otherAgent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", owner.ID).Error)
	contact.AssignedUserID = &owner.ID

	existing, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	require.NotNil(t, existing.AssignedUserID)
	require.Equal(t, owner.ID, *existing.AssignedUserID)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String()})
	testutil.SetAuthContext(req, org.ID, otherAgent.ID)
	require.NoError(t, app.CreateSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.First(&opp, "id = ?", existing.ID).Error)
	require.NotNil(t, opp.AssignedUserID)
	assert.Equal(t, owner.ID, *opp.AssignedUserID, "retriggering an existing open opportunity must not reassign it")
}
