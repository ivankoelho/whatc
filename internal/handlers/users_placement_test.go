package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpdateUser_SetsUnitAndDepartment(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 05")
	dept := e.department(t, "Vendas")
	target := testutil.CreateTestUser(t, e.app.DB, e.org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"unit_id": unit.ID.String(), "department_id": dept.ID.String()})
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	req.RequestCtx.SetUserValue("id", target.ID.String())
	require.NoError(t, e.app.UpdateUser(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "%s", testutil.GetResponseBody(req))

	var got models.User
	require.NoError(t, e.app.DB.First(&got, "id = ?", target.ID).Error)
	assert.Equal(t, &unit.ID, got.UnitID)
	assert.Equal(t, &dept.ID, got.DepartmentID)

	// Clear only the unit.
	req = testutil.NewJSONRequest(t, map[string]any{"unit_id": ""})
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	req.RequestCtx.SetUserValue("id", target.ID.String())
	require.NoError(t, e.app.UpdateUser(req))
	got = models.User{} // a fresh struct: scanning NULL into a populated one keeps the old value
	require.NoError(t, e.app.DB.First(&got, "id = ?", target.ID).Error)
	assert.Nil(t, got.UnitID)
	assert.Equal(t, &dept.ID, got.DepartmentID)
}

func TestUpdateUser_PlacementFromAnotherOrganizationRejected(t *testing.T) {
	e := newPhase2Env(t)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreign := models.Unit{OrganizationID: other.ID, Name: "Alheia"}
	require.NoError(t, e.app.DB.Create(&foreign).Error)
	target := testutil.CreateTestUser(t, e.app.DB, e.org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"unit_id": foreign.ID.String()})
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	req.RequestCtx.SetUserValue("id", target.ID.String())
	require.NoError(t, e.app.UpdateUser(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

	var got models.User
	require.NoError(t, e.app.DB.First(&got, "id = ?", target.ID).Error)
	assert.Nil(t, got.UnitID)
}

func TestUpdateUser_SelfCannotChangeOwnPlacementWithoutUsersWrite(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 02")
	agentRole := testutil.CreateAgentRole(t, e.app.DB, e.org.ID)
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"unit_id": unit.ID.String()})
	testutil.SetAuthContext(req, e.org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", agent.ID.String())
	require.NoError(t, e.app.UpdateUser(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestCreateUser_WithPlacement(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Escritório")
	dept := e.department(t, "TI")

	req := testutil.NewJSONRequest(t, map[string]any{
		"email": "novo@example.com", "password": "Senha#12345", "full_name": "Novo",
		"unit_id": unit.ID.String(), "department_id": dept.ID.String(),
	})
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	require.NoError(t, e.app.CreateUser(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "%s", testutil.GetResponseBody(req))

	var got models.User
	require.NoError(t, e.app.DB.First(&got, "email = ?", "novo@example.com").Error)
	assert.Equal(t, &unit.ID, got.UnitID)
	assert.Equal(t, &dept.ID, got.DepartmentID)

	req = testutil.NewJSONRequest(t, map[string]any{
		"email": "outro@example.com", "password": "Senha#12345", "full_name": "Outro", "unit_id": "bad",
	})
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	require.NoError(t, e.app.CreateUser(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}
