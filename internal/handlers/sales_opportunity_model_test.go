// internal/handlers/sales_opportunity_model_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestSalesOpportunityModel_CreateAndRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp := models.SalesOpportunity{
		OrganizationID:    org.ID,
		OpportunityNumber: "OPP-20260917-000001",
		ContactID:         contact.ID,
		Stage:             models.SalesOpportunityStagePotencial,
		Status:            models.SalesOpportunityStatusAberta,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	require.Equal(t, models.SalesOpportunityStagePotencial, got.Stage)

	event := models.SalesOpportunityEvent{
		OrganizationID:     org.ID,
		SalesOpportunityID: opp.ID,
		Type:               models.SalesOpportunityEventOpened,
		Source:             models.SalesOpportunityEventSourceSystem,
	}
	require.NoError(t, app.DB.Create(&event).Error)

	var gotEvent models.SalesOpportunityEvent
	require.NoError(t, app.DB.First(&gotEvent, "id = ?", event.ID).Error)
	require.Equal(t, models.SalesOpportunityEventOpened, gotEvent.Type)
}

func TestSalesOpportunityModel_OpportunityNumberUniquePerOrgNotGlobally(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	contactA := testutil.CreateTestContact(t, app.DB, orgA.ID)
	contactB := testutil.CreateTestContact(t, app.DB, orgB.ID)

	const number = "OPP-20260917-000001"

	oppA := models.SalesOpportunity{
		OrganizationID:    orgA.ID,
		OpportunityNumber: number,
		ContactID:         contactA.ID,
		Stage:             models.SalesOpportunityStagePotencial,
		Status:            models.SalesOpportunityStatusAberta,
	}
	require.NoError(t, app.DB.Create(&oppA).Error)

	oppB := models.SalesOpportunity{
		OrganizationID:    orgB.ID,
		OpportunityNumber: number,
		ContactID:         contactB.ID,
		Stage:             models.SalesOpportunityStagePotencial,
		Status:            models.SalesOpportunityStatusAberta,
	}
	require.NoError(t, app.DB.Create(&oppB).Error)
}

func TestUserModel_XProcessSellerCode(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	code := "V123"
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(&user).Update("xprocess_seller_code", &code).Error)

	var got models.User
	require.NoError(t, app.DB.First(&got, "id = ?", user.ID).Error)
	require.NotNil(t, got.XProcessSellerCode)
	require.Equal(t, "V123", *got.XProcessSellerCode)
}
