package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
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

// TestQuerySalesOpportunities_SumEstimatedValueScopedToOpen guards the
// "Valor estimado do funil" widget (Metric: "sum", Field: "open"): the sum
// must include only aberta opportunities' estimated_value, not
// convertida/perdida ones, matching the widget's own description.
func TestQuerySalesOpportunities_SumEstimatedValueScopedToOpen(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	val := func(v float64) *float64 { return &v }
	opps := []struct {
		status models.SalesOpportunityStatus
		value  float64
	}{
		{models.SalesOpportunityStatusAberta, 100},
		{models.SalesOpportunityStatusAberta, 250},
		{models.SalesOpportunityStatusConvertida, 1000},
		{models.SalesOpportunityStatusPerdida, 500},
	}
	for i, o := range opps {
		// One contact per opportunity: idx_sales_opp_org_contact_open is a
		// partial unique index on (organization_id, contact_id) WHERE
		// status = 'aberta', so two aberta opportunities can't share a contact.
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID,
			OpportunityNumber: "OPP-20260917-00001" + string(rune('1'+i)),
			Stage:             models.SalesOpportunityStagePotencial, Status: o.status,
			EstimatedValue: val(o.value), StageChangedAt: time.Now(),
		}).Error)
	}

	got := app.QuerySalesOpportunitiesForTest(org.ID, "sum", "open", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	assert.Equal(t, float64(350), got, "only the two aberta opportunities (100+250) should count, not convertida/perdida")
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

// TestQuerySalesOpportunities_RateMetricMatchesSalesConversionRate proves
// finding 5's fix: querySalesOpportunities("rate", ...) previously had no
// case for "rate" and fell through to the final `return 0`. It must now
// delegate to salesConversionRate and return the same value.
func TestQuerySalesOpportunities_RateMetricMatchesSalesConversionRate(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	statuses := []models.SalesOpportunityStatus{
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusPerdida,
	}
	for i, s := range statuses {
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID,
			OpportunityNumber: "OPP-20260918-00002" + string(rune('1'+i)),
			Stage:             models.SalesOpportunityStagePotencial, Status: s, StageChangedAt: time.Now(),
		}).Error)
	}

	start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	got := app.QuerySalesOpportunitiesForTest(org.ID, "rate", "converted", nil, start, end)
	want := app.SalesConversionRateForTest(org.ID, start, end)
	assert.InDelta(t, 66.67, got, 0.01)
	assert.Equal(t, want, got)
}

// TestQuerySalesOpportunities_OpenAndSLABreachedIgnoreDateRange proves finding
// 6's fix: "open" (count) and "sla_breached" (count) are current-state
// snapshots and must NOT be scoped to opened_at — an opportunity opened well
// before a narrow selected range must still be counted.
func TestQuerySalesOpportunities_OpenAndSLABreachedIgnoreDateRange(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contactOpen := testutil.CreateTestContact(t, app.DB, org.ID)
	contactBreached := testutil.CreateTestContact(t, app.DB, org.ID)

	oldOpenedAt := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactOpen.ID, OpportunityNumber: "OPP-20260818-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		OpenedAt: oldOpenedAt, StageChangedAt: time.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactBreached.ID, OpportunityNumber: "OPP-20260818-000002",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		OpenedAt: oldOpenedAt, StageChangedAt: time.Now(), SLABreached: true,
	}).Error)

	// A narrow "today" range that excludes both opportunities' opened_at.
	start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	openCount := app.QuerySalesOpportunitiesForTest(org.ID, "count", "open", nil, start, end)
	assert.Equal(t, float64(2), openCount, "both open opportunities must count regardless of when they were opened")

	breachedCount := app.QuerySalesOpportunitiesForTest(org.ID, "count", "sla_breached", nil, start, end)
	assert.Equal(t, float64(1), breachedCount, "the breached opportunity must count regardless of when it was opened")
}

// TestQuerySalesOpportunities_ConvertedAndLostStayDateScoped proves the other
// half of finding 6: "converted"/"lost" ARE period metrics and must keep
// excluding opportunities opened outside the selected range.
func TestQuerySalesOpportunities_ConvertedAndLostStayDateScoped(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	oldOpenedAt := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260818-000003",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusConvertida,
		OpenedAt: oldOpenedAt, StageChangedAt: time.Now(),
	}).Error)

	start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	got := app.QuerySalesOpportunitiesForTest(org.ID, "count", "converted", nil, start, end)
	assert.Equal(t, float64(0), got, "a conversion opened outside the selected range must not count")
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
//
// This exercises the real getGroupedData query path (not just the whitelist
// lookup CreateWidget/UpdateWidget do) against opportunities assigned to two
// different users, and asserts the returned per-user counts are correct.
func TestWidgetsSalesOpportunities_AssignedUserIDStillGroupable(t *testing.T) {
	assert.True(t, handlers.IsGroupByFieldAllowedForTest("sales_opportunities", "assigned_user_id"),
		"assigned_user_id must remain a valid group_by_field for sales_opportunities: getGroupedData's query path never joins contacts")

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentA := testutil.CreateTestUser(t, app.DB, org.ID)
	agentB := testutil.CreateTestUser(t, app.DB, org.ID)

	// Two opportunities for agentA, one for agentB. Distinct contacts: a
	// partial unique index only allows one *open* sales_opportunity per
	// (organization_id, contact_id).
	seed := []struct {
		agent uuid.UUID
		num   string
	}{
		{agentA.ID, "OPP-20260918-000010"},
		{agentA.ID, "OPP-20260918-000011"},
		{agentB.ID, "OPP-20260918-000012"},
	}
	for _, s := range seed {
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		agent := s.agent
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: s.num,
			Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
			AssignedUserID: &agent, StageChangedAt: time.Now(),
		}).Error)
	}

	widget := models.Widget{DataSource: "sales_opportunities", GroupByField: "assigned_user_id"}
	points := app.GetGroupedDataForTest(org.ID, widget, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	counts := make(map[string]float64, len(points))
	for _, p := range points {
		counts[p.Label] = p.Value
	}
	assert.Equal(t, float64(2), counts[agentA.ID.String()], "agentA must have 2 opportunities grouped")
	assert.Equal(t, float64(1), counts[agentB.ID.String()], "agentB must have 1 opportunity grouped")
}

// TestWidgetsSalesOpportunities_StageGroupable proves the round-3 fix:
// "stage" is listed in widgetDataSources["sales_opportunities"] (so
// CreateWidget/UpdateWidget accept it as a group_by_field), but
// getGroupedData's own allowedGroupByFields whitelist previously lacked it,
// so a "group by stage" widget was accepted then silently rendered empty.
// This exercises the real getGroupedData query path against opportunities
// seeded across two stages and asserts the returned per-stage counts.
func TestWidgetsSalesOpportunities_StageGroupable(t *testing.T) {
	assert.True(t, handlers.IsGroupByFieldAllowedForTest("sales_opportunities", "stage"),
		"stage must be a valid group_by_field for sales_opportunities")

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	seed := []struct {
		stage models.SalesOpportunityStage
		num   string
	}{
		{models.SalesOpportunityStagePotencial, "OPP-20260918-000020"},
		{models.SalesOpportunityStagePotencial, "OPP-20260918-000021"},
		{models.SalesOpportunityStageDirecionada, "OPP-20260918-000022"},
	}
	for _, s := range seed {
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: s.num,
			Stage: s.stage, Status: models.SalesOpportunityStatusAberta, StageChangedAt: time.Now(),
		}).Error)
	}

	widget := models.Widget{DataSource: "sales_opportunities", GroupByField: "stage"}
	points := app.GetGroupedDataForTest(org.ID, widget, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	counts := make(map[string]float64, len(points))
	for _, p := range points {
		counts[p.Label] = p.Value
	}
	assert.Equal(t, float64(2), counts[string(models.SalesOpportunityStagePotencial)], "potencial must have 2 opportunities grouped")
	assert.Equal(t, float64(1), counts[string(models.SalesOpportunityStageDirecionada)], "direcionada must have 1 opportunity grouped")
}
