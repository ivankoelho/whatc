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

func TestUnits_CNPJIsValidatedAndStoredAsDigits(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	create := func(name, cnpj string) int {
		req := testutil.NewJSONRequest(t, map[string]any{"name": name, "cnpj": cnpj, "active": true})
		testutil.SetAuthContext(req, org.ID, user.ID)
		require.NoError(t, app.CreateUnit(req))
		return testutil.GetResponseStatusCode(req)
	}

	assert.Equal(t, fasthttp.StatusOK, create("Loja Serrinha", "11.222.333/0001-81"))
	var unit models.Unit
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Loja Serrinha").First(&unit).Error)
	assert.Equal(t, "11222333000181", unit.CNPJ)

	assert.Equal(t, fasthttp.StatusBadRequest, create("Loja Porto", "11.222.333/0001-82"), "wrong check digit")
	assert.Equal(t, fasthttp.StatusBadRequest, create("Loja X", "11111111111111"), "all-equal digits")
	assert.Equal(t, fasthttp.StatusOK, create("Loja sem CNPJ", ""), "CNPJ stays optional")
}
