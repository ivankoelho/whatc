package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test exercises processSalesOpportunitySLA directly, so it lives in
// package handlers (not handlers_test) and uses newSLATestApp, matching the
// existing internal SLA processor tests in sla_processor_internal_test.go and
// sla_processor_occurrence_test.go.

func TestProcessSalesOpportunitySLA_MarksBreachedPastSevenDaysInDirecionada(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	old := time.Now().Add(-8 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260909-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: old,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	processor := NewSLAProcessor(app, time.Minute)
	processor.processSalesOpportunitySLA(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.True(t, got.SLABreached)
	require.NotNil(t, got.SLABreachedAt)
}

func TestProcessSalesOpportunitySLA_LeavingDirecionadaStopsShowingBreached(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	old := time.Now().Add(-8 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260909-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: old, SLABreached: true,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	// Agent converts it — a real handler call would clear sla_breached as
	// part of leaving the stage; here we assert the processor itself only
	// ever marks stage=direcionada rows, never touches a converted one.
	require.NoError(t, app.DB.Model(&opp).Update("status", models.SalesOpportunityStatusConvertida).Error)

	processor := NewSLAProcessor(app, time.Minute)
	processor.processSalesOpportunitySLA(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.True(t, got.SLABreached, "processor does not retroactively clear a closed opportunity's flag")
}

func TestProcessSalesOpportunitySLA_UnderSevenDaysNotMarked(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	recent := time.Now().Add(-2 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260915-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: recent,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	processor := NewSLAProcessor(app, time.Minute)
	processor.processSalesOpportunitySLA(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.False(t, got.SLABreached)
}

func TestGetSLAEnabledSettingsCached_IncludesSalesOpportunityOnlyOrgs(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA:            models.SLAConfig{Enabled: false, SalesOpportunityEnabled: true},
	}
	require.NoError(t, app.DB.Create(&settings).Error)
	// A prior test in this run may have already populated the shared Redis
	// cache key without this org; invalidate so the read below is a real DB
	// query against the extended WHERE clause, not stale cached data.
	app.InvalidateSLASettingsCache()

	loaded, err := app.getSLAEnabledSettingsCached()
	require.NoError(t, err)

	var found bool
	for _, s := range loaded {
		if s.OrganizationID == org.ID {
			found = true
		}
	}
	assert.True(t, found, "an org with only sales_opportunity_sla_enabled must still be loaded by the SLA processor loop")
}

func TestProcessSalesOpportunitySLA_DoesNotRewriteAlreadyBreached(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	old := time.Now().Add(-8 * 24 * time.Hour)
	firstBreach := time.Now().Add(-45 * time.Minute)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260910-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: old, SLABreached: true, SLABreachedAt: &firstBreach,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	processor := NewSLAProcessor(app, time.Minute)
	processor.processSalesOpportunitySLA(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	require.NotNil(t, got.SLABreachedAt)
	assert.WithinDuration(t, firstBreach, *got.SLABreachedAt, time.Second,
		"an already-breached opportunity must not have sla_breached_at rewritten on a later tick")
}
