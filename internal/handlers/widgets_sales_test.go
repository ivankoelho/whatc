package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuerySalesOpportunities_CountOpen(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: time.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000002",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusConvertida,
		StageChangedAt: time.Now(),
	}).Error)

	got := app.QuerySalesOpportunitiesForTest(org.ID, "count", "open", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	assert.Equal(t, float64(1), got)
}

func TestQuerySalesOpportunities_ConversionRateExcludesOpenFromDenominator(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	statuses := []models.SalesOpportunityStatus{
		models.SalesOpportunityStatusAberta,
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusPerdida,
	}
	for i, s := range statuses {
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID,
			OpportunityNumber: "OPP-20260917-00000" + string(rune('1'+i)),
			Stage:             models.SalesOpportunityStagePotencial, Status: s, StageChangedAt: time.Now(),
		}).Error)
	}

	rate := app.SalesConversionRateForTest(org.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	assert.InDelta(t, 66.67, rate, 0.01, "2 convertidas / (2 convertidas + 1 perdida) * 100, aberta fora do denominador")
}
