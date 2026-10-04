package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placementEnv is a graph fixture with a unit, a department and helpers to tag teams and to place
// the contact.
type placementEnv struct {
	app     *App
	org     *models.Organization
	account *models.WhatsAppAccount
	contact *models.Contact
	session *models.ChatbotSession
	unit    models.Unit
	dept    models.Department
}

func newPlacementEnv(t *testing.T) placementEnv {
	t.Helper()
	app, org, account, contact, session := newGraphTestFixtures(t)
	e := placementEnv{app: app, org: org, account: account, contact: contact, session: session,
		unit: models.Unit{OrganizationID: org.ID, Name: "Feira 2"}, dept: models.Department{OrganizationID: org.ID, Name: "Expedição"}}
	require.NoError(t, app.DB.Create(&e.unit).Error)
	require.NoError(t, app.DB.Create(&e.dept).Error)
	return e
}

func (e placementEnv) team(t *testing.T, mut func(*models.Team)) *models.Team {
	t.Helper()
	team := &models.Team{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: e.org.ID, Name: "Time " + uuid.NewString()[:8],
		IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin,
		UnitID: &e.unit.ID, DepartmentID: &e.dept.ID,
	}
	if mut != nil {
		mut(team)
	}
	require.NoError(t, e.app.DB.Create(team).Error)
	return team
}

// place sets the contact's type and placement on the row and on the struct the flow runs with.
func (e placementEnv) place(t *testing.T, typ models.ContactType, unit, dept *uuid.UUID) {
	t.Helper()
	require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", e.contact.ID).Updates(map[string]any{
		"contact_type": string(typ), "unit_id": unit, "department_id": dept,
	}).Error)
	require.NoError(t, e.app.DB.First(e.contact, e.contact.ID).Error)
}

func (e placementEnv) run(t *testing.T, cfg map[string]any) models.AgentTransfer {
	t.Helper()
	flow := newTransferFlow(t, e.app, e.org, e.account, cfg)
	require.NoError(t, e.app.runChatGraph(e.account, e.contact, e.session, flow, "help", "", nil))
	var transfers []models.AgentTransfer
	require.NoError(t, e.app.DB.Where("organization_id = ? AND contact_id = ?", e.org.ID, e.contact.ID).Find(&transfers).Error)
	require.Len(t, transfers, 1, "the contact is always transferred somewhere")
	return transfers[0]
}

func reasonOf(tr models.AgentTransfer) string {
	if tr.RoutingReason == nil {
		return "<null>"
	}
	return *tr.RoutingReason
}

func TestFlowTransfer_ContactPlacementRoutesToTheTeamOfTheUnitAndDepartment(t *testing.T) {
	e := newPlacementEnv(t)
	match := e.team(t, nil)
	e.team(t, func(tm *models.Team) { tm.DepartmentID = nil }) // other tags: not a match
	e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)

	tr := e.run(t, map[string]any{"destination": "contact_placement"})
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, match.ID, *tr.TeamID)
	assert.Equal(t, models.RoutingReasonContactPlacement, reasonOf(tr))
	assert.Equal(t, models.TransferSourceFlow, tr.Source)
}

func TestFlowTransfer_ContactPlacementFallsBackWithTheRightReason(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e placementEnv)
		want  string
	}{
		{"a cliente has no placement to route by", func(t *testing.T, e placementEnv) {
			e.team(t, nil)
			e.place(t, models.ContactTypeCliente, &e.unit.ID, &e.dept.ID) // legacy row
		}, models.RoutingReasonNoContactPlacement},
		{"a colaborador without a unit", func(t *testing.T, e placementEnv) {
			e.team(t, nil)
			e.place(t, models.ContactTypeColaborador, nil, &e.dept.ID)
		}, models.RoutingReasonNoContactPlacement},
		{"a colaborador without a department", func(t *testing.T, e placementEnv) {
			e.team(t, nil)
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, nil)
		}, models.RoutingReasonNoContactPlacement},
		{"no team is tagged with the pair", func(t *testing.T, e placementEnv) {
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		}, models.RoutingReasonNoTeamForPlacement},
		{"an inactive team does not count", func(t *testing.T, e placementEnv) {
			tm := e.team(t, nil)
			// gorm's default:true would turn a false into true on Create, so it is set afterwards
			require.NoError(t, e.app.DB.Model(&models.Team{}).Where("id = ?", tm.ID).Update("is_active", false).Error)
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		}, models.RoutingReasonNoTeamForPlacement},
		{"a deleted team does not count", func(t *testing.T, e placementEnv) {
			tm := e.team(t, nil)
			require.NoError(t, e.app.DB.Delete(tm).Error)
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		}, models.RoutingReasonNoTeamForPlacement},
		{"a team of another organization does not count", func(t *testing.T, e placementEnv) {
			other := testutil.CreateTestOrganization(t, e.app.DB)
			e.team(t, func(tm *models.Team) { tm.OrganizationID = other.ID })
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		}, models.RoutingReasonNoTeamForPlacement},
		{"two active teams with the pair are ambiguous, whatever the order", func(t *testing.T, e placementEnv) {
			e.team(t, nil)
			e.team(t, nil)
			e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		}, models.RoutingReasonAmbiguousPlacement},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newPlacementEnv(t)
			c.setup(t, e)
			tr := e.run(t, map[string]any{"destination": "contact_placement"})
			assert.Nil(t, tr.TeamID, "no fallback team configured: the general queue")
			assert.Equal(t, c.want, reasonOf(tr))
		})
	}
}

func TestFlowTransfer_ContactPlacementFallbackTeam(t *testing.T) {
	e := newPlacementEnv(t)
	fallback := e.team(t, func(tm *models.Team) { tm.UnitID, tm.DepartmentID = nil, nil })
	e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID) // nobody is tagged with the pair

	tr := e.run(t, map[string]any{"destination": "contact_placement", "fallback_team_id": fallback.ID.String()})
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, fallback.ID, *tr.TeamID)
	assert.Equal(t, models.RoutingReasonNoTeamForPlacement, reasonOf(tr), "the reason is why it fell back, not where it went")
}

func TestFlowTransfer_FallbackTeamMustBeAnActiveTeamOfTheOrganization(t *testing.T) {
	for name, fb := range map[string]string{"unknown id": uuid.NewString(), "not a uuid": "nope"} {
		t.Run(name, func(t *testing.T) {
			e2 := newPlacementEnv(t)
			e2.place(t, models.ContactTypeColaborador, &e2.unit.ID, &e2.dept.ID)
			tr := e2.run(t, map[string]any{"destination": "contact_placement", "fallback_team_id": fb})
			assert.Nil(t, tr.TeamID)
			assert.Equal(t, models.RoutingReasonNoTeamForPlacement, reasonOf(tr))
		})
	}

	inactive := newPlacementEnv(t)
	team := inactive.team(t, func(tm *models.Team) { tm.UnitID, tm.DepartmentID = nil, nil })
	// gorm's default:true would turn a false into true on Create, so it is set afterwards
	require.NoError(t, inactive.app.DB.Model(&models.Team{}).Where("id = ?", team.ID).Update("is_active", false).Error)
	inactive.place(t, models.ContactTypeColaborador, &inactive.unit.ID, &inactive.dept.ID)
	tr := inactive.run(t, map[string]any{"destination": "contact_placement", "fallback_team_id": team.ID.String()})
	assert.Nil(t, tr.TeamID, "an inactive fallback team is not used")
}

// A fallback_team_id that points at a live team of ANOTHER organization is rejected: the contact
// goes to the general queue, never to a foreign team. The same fallback of its own organization works.
func TestFlowTransfer_FallbackTeamOfAnotherOrganizationIsRejected(t *testing.T) {
	e := newPlacementEnv(t)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreign := e.team(t, func(tm *models.Team) { tm.OrganizationID = other.ID; tm.UnitID, tm.DepartmentID = nil, nil })
	e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID) // nobody of this organization has the pair

	tr := e.run(t, map[string]any{"destination": "contact_placement", "fallback_team_id": foreign.ID.String()})
	assert.Nil(t, tr.TeamID, "a team of another organization is never the destination")
	assert.Equal(t, models.RoutingReasonNoTeamForPlacement, reasonOf(tr))

	own := newPlacementEnv(t)
	mine := own.team(t, func(tm *models.Team) { tm.UnitID, tm.DepartmentID = nil, nil })
	own.place(t, models.ContactTypeColaborador, &own.unit.ID, &own.dept.ID)
	tr = own.run(t, map[string]any{"destination": "contact_placement", "fallback_team_id": mine.ID.String()})
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, mine.ID, *tr.TeamID)
}

// The original behavior is untouched: no destination, "team" and an unknown value all mean the fixed team.
func TestFlowTransfer_WithoutContactPlacementTheNodeBehavesAsBefore(t *testing.T) {
	for _, cfg := range []map[string]any{{}, {"destination": "team"}, {"destination": "something-new"}} {
		e := newPlacementEnv(t)
		e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
		e.team(t, nil) // a team a placement routing would have used: must be ignored here
		tr := e.run(t, cfg)
		assert.Nil(t, tr.TeamID, "no team_id: the general queue, whatever the contact's placement")
		assert.Equal(t, models.RoutingReasonTeamFixed, reasonOf(tr), "%v", cfg)
	}

	e := newPlacementEnv(t)
	fixed := e.team(t, func(tm *models.Team) { tm.UnitID, tm.DepartmentID = nil, nil })
	tr := e.run(t, map[string]any{"team_id": fixed.ID.String()})
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, fixed.ID, *tr.TeamID)
	assert.Equal(t, models.RoutingReasonTeamFixed, reasonOf(tr))
}

func TestFlowTransfer_OtherTransfersKeepANullReason(t *testing.T) {
	e := newPlacementEnv(t)
	e.app.createTransferToQueue(e.account, e.contact, models.TransferSourceChatbotDisabled)
	var tr models.AgentTransfer
	require.NoError(t, e.app.DB.Where("contact_id = ?", e.contact.ID).First(&tr).Error)
	assert.Nil(t, tr.RoutingReason)
}

func TestFlowTransfer_ContactPlacementKeepsTheActiveTransferGuard(t *testing.T) {
	e := newPlacementEnv(t)
	e.team(t, nil)
	e.place(t, models.ContactTypeColaborador, &e.unit.ID, &e.dept.ID)
	e.run(t, map[string]any{"destination": "contact_placement"})

	// a second run does not create a second active transfer
	flow := newTransferFlow(t, e.app, e.org, e.account, map[string]any{"destination": "contact_placement"})
	require.NoError(t, e.app.runChatGraph(e.account, e.contact, e.session, flow, "again", "", nil))
	var n int64
	e.app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ? AND status = ?", e.contact.ID, models.TransferStatusActive).Count(&n)
	assert.Equal(t, int64(1), n)
}
