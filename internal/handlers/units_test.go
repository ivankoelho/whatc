package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUnits_CreateAndList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Loja Alagoinhas", "type": "loja", "active": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	listReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(listReq, org.ID, user.ID)
	require.NoError(t, app.ListUnits(listReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(listReq))
	assert.Contains(t, string(testutil.GetResponseBody(listReq)), "Loja Alagoinhas")
}

func TestUnits_CreateRejectedForAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req),
		"agents administer occurrences, not the unit catalog")
}

func TestUnits_DeleteRejectedWhenTeamReferencesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Feira"}
	require.NoError(t, app.DB.Create(&unit).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Feira Logística", UnitID: &unit.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", unit.ID.String())
	require.NoError(t, app.DeleteUnit(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}

func TestUnits_DuplicateNameRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	first := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(first))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(second))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(second))
}
