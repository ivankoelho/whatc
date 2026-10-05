package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestDepartments_CreateAndList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Logística", "active": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateDepartment(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	listReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(listReq, org.ID, user.ID)
	require.NoError(t, app.ListDepartments(listReq))
	assert.Contains(t, string(testutil.GetResponseBody(listReq)), "Logística")
}

func TestDepartments_DeleteRejectedWhenTeamReferencesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	department := models.Department{OrganizationID: org.ID, Name: "ADM"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas ADM", DepartmentID: &department.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", department.ID.String())
	require.NoError(t, app.DeleteDepartment(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}

// A department created as inactive must be stored inactive (gorm:"default:true" used to win over false).
func TestDepartments_CreateInactiveStaysInactive(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Setor Antigo", "active": false})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateDepartment(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.Department
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Setor Antigo").First(&stored).Error)
	assert.False(t, stored.Active)
}
