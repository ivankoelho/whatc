package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpdateTeam_TagsUnitAndDepartment(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Alagoinhas"}
	require.NoError(t, app.DB.Create(&unit).Error)
	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas Logística"}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": team.Name, "is_active": true,
		"unit_id": unit.ID.String(), "department_id": department.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", team.ID.String())
	require.NoError(t, app.UpdateTeam(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.Team
	require.NoError(t, app.DB.First(&reloaded, "id = ?", team.ID).Error)
	require.NotNil(t, reloaded.UnitID)
	require.NotNil(t, reloaded.DepartmentID)
	assert.Equal(t, unit.ID, *reloaded.UnitID)
	assert.Equal(t, department.ID, *reloaded.DepartmentID)
}

func TestUpdateTeam_ClearsUnitWithEmptyString(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Matriz"}
	require.NoError(t, app.DB.Create(&unit).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Time X", UnitID: &unit.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": team.Name, "is_active": true, "unit_id": "",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", team.ID.String())
	require.NoError(t, app.UpdateTeam(req))

	var reloaded models.Team
	require.NoError(t, app.DB.First(&reloaded, "id = ?", team.ID).Error)
	assert.Nil(t, reloaded.UnitID)
}
