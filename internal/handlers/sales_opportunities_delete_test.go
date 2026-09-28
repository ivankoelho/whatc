package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

func TestDeleteSalesOpportunity_RejectedForNonSuperAdmin(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, admin.ID, contact.ID)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.DeleteSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))

	var stillThere models.SalesOpportunity
	assert.NoError(t, app.DB.First(&stillThere, "id = ?", opp.ID).Error, "opportunity must survive a rejected delete")
}

// A super admin can delete regardless of stage/status — including one
// assigned to a different agent (loadAuthorizedSalesOpportunity's own
// ownership check is a no-op for a super admin, since HasPermission already
// treats IsSuperAdmin as "every permission").
func TestDeleteSalesOpportunity_SuperAdminHardDeletesAnyStage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	ownerRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&ownerRole.ID))
	superAdminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	superAdmin := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithRoleID(&superAdminRole.ID), testutil.WithSuperAdmin())

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, owner.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("stage", models.SalesOpportunityStageDirecionada).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventOpened,
		Source: models.SalesOpportunityEventSourceSystem,
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "1", Documento: "12345678900",
	}).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, superAdmin.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.DeleteSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var gone models.SalesOpportunity
	err := app.DB.Unscoped().First(&gone, "id = ?", opp.ID).Error
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "opportunity must be hard-deleted, not soft-deleted")

	var eventCount, linkCount int64
	require.NoError(t, app.DB.Unscoped().Model(&models.SalesOpportunityEvent{}).Where("sales_opportunity_id = ?", opp.ID).Count(&eventCount).Error)
	require.NoError(t, app.DB.Unscoped().Model(&models.SalesOpportunityXProcessLink{}).Where("sales_opportunity_id = ?", opp.ID).Count(&linkCount).Error)
	assert.Zero(t, eventCount, "events must be hard-deleted along with the opportunity")
	assert.Zero(t, linkCount, "xprocess links must be hard-deleted along with the opportunity")
}
