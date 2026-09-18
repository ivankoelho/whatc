package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
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

// TestWidgetsSalesOpportunities_AssignedUserIDNotFilterable guards against
// SQLSTATE 42702 "column reference is ambiguous": tableQuerySQL's
// sales_opportunities entry joins contacts, and both sales_opportunities and
// contacts have an assigned_user_id column, but appendFilterSQL/buildFilterSQL
// interpolate filter columns unqualified. assigned_user_id must therefore stay
// out of allowedFilterFields["sales_opportunities"] until that helper is made
// alias-aware.
func TestWidgetsSalesOpportunities_AssignedUserIDNotFilterable(t *testing.T) {
	assert.False(t, handlers.IsFilterFieldAllowedForTest("sales_opportunities", "assigned_user_id"),
		"assigned_user_id must not be filterable on sales_opportunities: it is ambiguous against the contacts join in tableQuerySQL")
}

// TestWidgetsSalesOpportunities_TableWidgetIgnoresAssignedUserIDFilter proves
// the fix end-to-end against a real table-display query: a filter on
// assigned_user_id must be silently dropped (not applied, and not producing
// an ambiguous-column SQL error) rather than reaching the joined query.
func TestWidgetsSalesOpportunities_TableWidgetIgnoresAssignedUserIDFilter(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	// Two distinct contacts: a partial unique index only allows one *open*
	// sales_opportunity per (organization_id, contact_id).
	contactA := testutil.CreateTestContact(t, app.DB, org.ID)
	contactB := testutil.CreateTestContact(t, app.DB, org.ID)
	agentA := testutil.CreateTestUser(t, app.DB, org.ID)
	agentB := testutil.CreateTestUser(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactA.ID, OpportunityNumber: "OPP-20260918-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agentA.ID, StageChangedAt: time.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactB.ID, OpportunityNumber: "OPP-20260918-000002",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agentB.ID, StageChangedAt: time.Now(),
	}).Error)

	widget := models.Widget{DataSource: "sales_opportunities", DisplayType: "table"}
	filters := []handlers.FilterInput{{Field: "assigned_user_id", Operator: "equals", Value: agentA.ID.String()}}

	rows := app.GetTableRowsForTest(org.ID, widget, filters, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	// If the field were still allowed, the joined query would either error
	// (0 rows, ambiguous column) or, once alias-qualified, filter down to 1.
	// Today it must be silently ignored: both opportunities come back.
	assert.Len(t, rows, 2, "assigned_user_id filter must be dropped (not error, not applied) for sales_opportunities table widgets")
}

// TestWidgetsSalesOpportunities_AssignedUserIDStillGroupable guards against
// the round-1 filter fix over-reaching: assigned_user_id was removed from
// both allowedFilterFields and widgetDataSources for sales_opportunities,
// but widgetDataSources also gates group_by_field in CreateWidget/UpdateWidget
// and getGroupedData's query for sales_opportunities never joins contacts, so
// grouping by assigned_user_id was never actually ambiguous. It must stay
// accepted as a group-by field even though it's correctly rejected as a
// filter field (see TestWidgetsSalesOpportunities_AssignedUserIDNotFilterable).
func TestWidgetsSalesOpportunities_AssignedUserIDStillGroupable(t *testing.T) {
	assert.True(t, handlers.IsGroupByFieldAllowedForTest("sales_opportunities", "assigned_user_id"),
		"assigned_user_id must remain a valid group_by_field for sales_opportunities: getGroupedData's query path never joins contacts")
}
