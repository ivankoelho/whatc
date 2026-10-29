package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// PUT /api/teams/{id} is a partial update: what is not sent is not touched; deactivating needs an explicit false.
func TestUpdateTeam_PartialUpdateKeepsOmittedFields(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, org.ID, []string{"teams:read", "teams:write"})
	team := &models.Team{OrganizationID: org.ID, Name: "Backoffice", Description: "desc", IsActive: true, PerAgentTimeoutSecs: 90, AssignmentStrategy: models.AssignmentStrategyLoadBalanced}
	require.NoError(t, app.DB.Create(team).Error)
	put := func(body map[string]any) int {
		c, _ := chatDo(t, app.UpdateTeam, org.ID, admin.ID, body, map[string]string{"id": team.ID.String()}, nil)
		return c
	}
	reload := func() models.Team {
		var x models.Team
		require.NoError(t, app.DB.First(&x, team.ID).Error)
		return x
	}

	require.Equal(t, fasthttp.StatusOK, put(map[string]any{"name": "Backoffice 2"}))
	x := reload()
	assert.Equal(t, "Backoffice 2", x.Name)
	assert.True(t, x.IsActive, "an omitted is_active does not deactivate")
	assert.Equal(t, "desc", x.Description)
	assert.Equal(t, 90, x.PerAgentTimeoutSecs)
	assert.Equal(t, models.AssignmentStrategyLoadBalanced, x.AssignmentStrategy)

	dept := &models.Department{OrganizationID: org.ID, Name: "Jur " + uuid.NewString()[:4], Active: true}
	require.NoError(t, app.DB.Create(dept).Error)
	require.Equal(t, fasthttp.StatusOK, put(map[string]any{"department_id": dept.ID.String()}))
	x = reload()
	assert.True(t, x.IsActive)
	assert.Equal(t, "Backoffice 2", x.Name)
	require.NotNil(t, x.DepartmentID)

	require.Equal(t, fasthttp.StatusOK, put(map[string]any{"description": ""}))
	assert.Equal(t, "", reload().Description, "an explicit empty value is applied")
	require.Equal(t, fasthttp.StatusOK, put(map[string]any{"is_active": false}))
	x = reload()
	assert.False(t, x.IsActive, "intentional deactivation works")
	assert.Equal(t, "Backoffice 2", x.Name)
}

// A sales team is recognised by its own flag, by its department, or, with no department, by its name.
func TestCommercialTeam_ClassifiedWithOrWithoutDepartment(t *testing.T) {
	w := newXDWorld(t)
	db := w.app.DB
	sac := w.u["SAC"]
	mk := func(name string, dept *models.Department, flag bool) *models.Team {
		tm := &models.Team{OrganizationID: w.org.ID, Name: name, IsActive: true, Commercial: flag}
		if dept != nil {
			tm.DepartmentID = &dept.ID
		}
		require.NoError(t, db.Create(tm).Error)
		return tm
	}
	commDept := &models.Department{OrganizationID: w.org.ID, Name: "Comercial " + uuid.NewString()[:4], Active: true, Commercial: true}
	require.NoError(t, db.Create(commDept).Error)
	create := func(tm *models.Team) int {
		c := testutil.CreateTestContact(t, db, w.org.ID)
		w.customerWrote(t, c.ID)
		code, _ := chatDo(t, w.app.CreateOccurrence, sac.OrganizationID, sac.ID, map[string]any{"contact_id": c.ID.String(), "title": "x", "team_id": tm.ID.String()}, nil, nil)
		return code
	}
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, create(mk("Loja Norte", nil, true)), "own flag, no department")
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, create(mk("Vendas Sul", nil, false)), "no department, named like sales")
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, create(mk("Atendimento Loja", commDept, false)), "commercial department")
	assert.Equal(t, fasthttp.StatusOK, create(mk("Jurídico", nil, false)), "backoffice without department is valid")
	assert.Equal(t, fasthttp.StatusOK, create(mk("Pós-venda", nil, false)), "not a sales team")

	// derived from the conversation's source attendance
	contact := testutil.CreateTestContact(t, db, w.org.ID)
	w.customerWrote(t, contact.ID)
	activeTransfer(t, w.app, w.org.ID, contact.ID, nil, &w.team["Vendas"].ID)
	var tr models.AgentTransfer
	require.NoError(t, db.Where("contact_id = ?", contact.ID).First(&tr).Error)
	require.NoError(t, db.Model(w.team["Vendas"]).Update("commercial", true).Error)
	code, _ := chatDo(t, w.app.CreateOccurrence, sac.OrganizationID, sac.ID, map[string]any{"contact_id": contact.ID.String(), "title": "y", "source_transfer_id": tr.ID.String()}, nil, nil)
	assert.NotEqual(t, fasthttp.StatusOK, code, "a team inherited from a sales conversation is refused too")
}

// Writing to the customer through the case needs chat:write; the lawyer profile has none.
func TestReplyToOccurrence_NeedsChatWrite(t *testing.T) {
	w := newXDWorld(t)
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "Jur-"+uuid.NewString()[:4], profilePerms(t, "Jurídico"))
	u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: u.ID}).Error)
	occ := w.open(t, w.u["SAC"], w.team["Log"], "Sem resposta")
	code, _ := chatDo(t, w.app.ReplyToOccurrence, u.OrganizationID, u.ID, map[string]any{"content": "oi"}, map[string]string{"id": occ.String()}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	_, raw := chatDo(t, w.app.GetOccurrence, u.OrganizationID, u.ID, nil, map[string]string{"id": occ.String()}, nil)
	assert.Contains(t, string(raw), `"reply":false`)
	assert.Contains(t, string(raw), `"open_conversation":false`)
}

// Creating an organization does not make the creator a member; the global privilege still reaches it.
func TestCreateOrganization_DoesNotAssociateTheSuperAdmin(t *testing.T) {
	app := newTestApp(t)
	home := testutil.CreateTestOrganization(t, app.DB)
	super := testutil.CreateTestUser(t, app.DB, home.ID, testutil.WithSuperAdmin())
	code, raw := chatDo(t, app.CreateOrganization, home.ID, super.ID, map[string]any{"name": "Nova Org " + uuid.NewString()[:4]}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	created := decodeChat[struct {
		ID uuid.UUID `json:"id"`
	}](t, raw)
	var n int64
	app.DB.Model(&models.UserOrganization{}).Where("user_id = ? AND organization_id = ?", super.ID, created.ID).Count(&n)
	assert.Zero(t, n, "no automatic membership")
}
