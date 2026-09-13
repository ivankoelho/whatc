package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOccurrence_ComputesDeadlinesFromPriority(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Urgente", "priority": "urgent",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.SLA.ResponseDeadline)
	require.NotNil(t, occ.SLA.ResolutionDeadline)
	// Default urgent policy: 30min response, 4h resolution (see defaultSLAPolicies).
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *occ.SLA.ResponseDeadline, 5*time.Second)
	assert.WithinDuration(t, time.Now().Add(4*time.Hour), *occ.SLA.ResolutionDeadline, 5*time.Second)
}

func TestUpdateOccurrence_RecomputesDeadlinesOnPriorityChange(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	createReq := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Baixa prioridade", "priority": "low",
	})
	testutil.SetAuthContext(createReq, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(createReq))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	originalDeadline := *occ.SLA.ResponseDeadline

	updateReq := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "priority": "urgent"})
	testutil.SetAuthContext(updateReq, org.ID, user.ID)
	testutil.SetPathParam(updateReq, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(updateReq))

	require.NoError(t, app.DB.First(&occ, "id = ?", occ.ID).Error)
	assert.True(t, occ.SLA.ResponseDeadline.Before(originalDeadline),
		"urgent's response deadline must be sooner than low's")
}

// TestCreateOccurrence_SLAPolicyLookupFailure_StillCreatesWithoutDeadlines
// covers the brief's central safety guarantee: a failure to resolve an SLA
// policy must never block opening a case, it must only leave the deadlines
// unset. Neither CreateOccurrence nor getSLAPolicy validates priority against
// the low/normal/high/urgent enum before the policy lookup, so an unrecognized
// priority value reaches getSLAPolicy unfiltered: ensureDefaultSLAPolicies
// seeds the four known priorities, then the `First` query filtered on this
// bogus priority matches no row and returns a real gorm.ErrRecordNotFound —
// a genuine error path through production code, not a contrived one, and
// reached without mocking the DB connection.
func TestCreateOccurrence_SLAPolicyLookupFailure_StillCreatesWithoutDeadlines(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Prioridade desconhecida", "priority": "nonexistent_priority",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req), "SLA policy lookup failure must not block occurrence creation")

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	assert.Equal(t, "nonexistent_priority", string(occ.Priority))
	assert.Nil(t, occ.SLA.ResponseDeadline, "deadlines must stay unset, not computed, when the policy lookup errors")
	assert.Nil(t, occ.SLA.ResolutionDeadline, "deadlines must stay unset, not computed, when the policy lookup errors")
}

// TestUpdateOccurrence_SLAPolicyLookupFailure_StillUpdatesWithoutDeadlines is
// the update-path counterpart: the occurrence is opened with the same
// unrecognized priority (so it starts with no deadlines), then updated to a
// different unrecognized priority (so the update path also hits the policy
// lookup failure). The update must still succeed and the deadlines must stay
// nil rather than being partially computed or the request being blocked.
func TestUpdateOccurrence_SLAPolicyLookupFailure_StillUpdatesWithoutDeadlines(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	createReq := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Prioridade desconhecida", "priority": "nonexistent_priority",
	})
	testutil.SetAuthContext(createReq, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(createReq))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.Nil(t, occ.SLA.ResponseDeadline)
	require.Nil(t, occ.SLA.ResolutionDeadline)

	updateReq := testutil.NewJSONRequest(t, map[string]any{
		"title": "Ainda desconhecida", "priority": "still_bogus_priority",
	})
	testutil.SetAuthContext(updateReq, org.ID, user.ID)
	testutil.SetPathParam(updateReq, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(updateReq), "SLA policy lookup failure must not block occurrence update")

	require.NoError(t, app.DB.First(&occ, "id = ?", occ.ID).Error)
	assert.Equal(t, "Ainda desconhecida", occ.Title)
	assert.Equal(t, "still_bogus_priority", string(occ.Priority))
	assert.Nil(t, occ.SLA.ResponseDeadline, "deadlines must stay unset, not computed, when the policy lookup errors")
	assert.Nil(t, occ.SLA.ResolutionDeadline, "deadlines must stay unset, not computed, when the policy lookup errors")
}
