package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise processOccurrenceSLA and getSLAEnabledSettingsCached
// directly, so this file lives in package handlers (not handlers_test) and
// uses newSLATestApp, matching the existing internal SLA processor tests in
// sla_processor_internal_test.go.

func TestProcessOccurrenceSLA_MarksResponseBreach(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem resposta",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResponseDeadline: &past},
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.True(t, reloaded.SLA.Breached)
	assert.NotNil(t, reloaded.SLA.BreachedAt)
}

func TestProcessOccurrenceSLA_DoesNotBreachWhenAlreadyReplied(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	replied := time.Now().Add(-30 * time.Minute)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Já respondido",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResponseDeadline: &past, FirstResponseAt: &replied},
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.False(t, reloaded.SLA.Breached, "a case with a reply before the deadline must not be marked breached")
}

func TestProcessOccurrenceSLA_MarksResolutionBreach(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem resolução",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResolutionDeadline: &past},
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.True(t, reloaded.SLA.Breached)
	assert.NotNil(t, reloaded.SLA.BreachedAt)
}

func TestProcessOccurrenceSLA_DoesNotRewriteAlreadyBreached(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	firstBreach := time.Now().Add(-45 * time.Minute)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Já quebrado",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResponseDeadline: &past, Breached: true, BreachedAt: &firstBreach},
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	require.NotNil(t, reloaded.SLA.BreachedAt)
	assert.WithinDuration(t, firstBreach, *reloaded.SLA.BreachedAt, time.Second,
		"an already-breached occurrence must not have breached_at rewritten on a later tick")
}

func TestGetSLAEnabledSettingsCached_IncludesOccurrenceOnlyOrgs(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA:            models.SLAConfig{Enabled: false, OccurrenceEnabled: true},
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
	assert.True(t, found, "an org with only occurrence_sla_enabled must still be loaded by the SLA processor loop")
}
