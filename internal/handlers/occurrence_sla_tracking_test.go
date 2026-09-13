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
