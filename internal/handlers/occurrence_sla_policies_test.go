package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOccurrenceSLAPolicies_SeedsDefaultsOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListOccurrenceSLAPolicies(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.OccurrenceSLAPolicy{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 4, count)
}

func TestOccurrenceSLAPolicies_UpsertChangesOnlyThatPriority(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"response_minutes": 15, "resolution_minutes": 60})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "priority", "urgent")
	require.NoError(t, app.UpsertOccurrenceSLAPolicy(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	urgent, err := app.GetSLAPolicyForTest(org.ID, models.OccurrencePriorityUrgent)
	require.NoError(t, err)
	assert.Equal(t, 15, urgent.ResponseMinutes)

	normal, err := app.GetSLAPolicyForTest(org.ID, models.OccurrencePriorityNormal)
	require.NoError(t, err)
	assert.Equal(t, 240, normal.ResponseMinutes, "unrelated priority must be untouched")
}

func TestOccurrenceSLAPolicies_RejectedForAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"response_minutes": 5, "resolution_minutes": 10})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "priority", "urgent")
	require.NoError(t, app.UpsertOccurrenceSLAPolicy(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

// This is the test that matters most: it proves the partial unique index
// leaves room for the future department/unit/category-scoped phase without
// a schema change, by inserting a more specific row next to the org-wide one.
func TestOccurrenceSLAPolicies_ScopedRowDoesNotConflictWithOrgWideRow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.EnsureDefaultSLAPoliciesForTest(org.ID))

	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)

	scoped := models.OccurrenceSLAPolicy{
		OrganizationID: org.ID, DepartmentID: &department.ID,
		Priority: models.OccurrencePriorityUrgent, ResponseMinutes: 10, ResolutionMinutes: 30,
	}
	require.NoError(t, app.DB.Create(&scoped).Error, "a department-scoped row must not conflict with the org-wide one")

	var count int64
	app.DB.Model(&models.OccurrenceSLAPolicy{}).
		Where("organization_id = ? AND priority = ?", org.ID, models.OccurrencePriorityUrgent).
		Count(&count)
	assert.EqualValues(t, 2, count, "org-wide row plus the new department-scoped row")
}
