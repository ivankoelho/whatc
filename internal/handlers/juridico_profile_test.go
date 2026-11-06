package handlers_test

import (
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The Jurídico profile: works the protocols of its own team(s), nothing else. These are the exact permissions, and
// the behaviour that follows from them.
func TestJuridicoProfile_ExactPermissions(t *testing.T) {
	got := append([]string{}, profilePerms(t, "Jurídico")...)
	sort.Strings(got)
	assert.Equal(t, []string{"contacts:read", "occurrences:read", "occurrences:write", "team_chat:read", "team_chat:write"}, got)
	for _, forbidden := range []string{"chat:read", "chat:write", "transfers:read", "transfers:write", "sales_opportunities:read",
		"conversations:view_all", "conversations:view_team", "occurrences:view_all", "users:read", "teams:read", "settings.general:write",
		"organizations:read", "accounts:write", "templates:read", "campaigns:read", "contacts:export", "contacts:write"} {
		assert.NotContains(t, got, forbidden)
	}
}

func TestJuridicoProfile_WorksOwnTeamCasesAndNothingElse(t *testing.T) {
	w := newXDWorld(t)
	db := w.app.DB
	sac := w.u["SAC"]

	dept := &models.Department{OrganizationID: w.org.ID, Name: "Jurídico " + uuid.NewString()[:4], Active: true}
	require.NoError(t, db.Create(dept).Error)
	mkTeam := func(name string) *models.Team {
		tm := &models.Team{OrganizationID: w.org.ID, Name: name + " " + uuid.NewString()[:4], IsActive: true, DepartmentID: &dept.ID}
		require.NoError(t, db.Create(tm).Error)
		return tm
	}
	teamA, teamB := mkTeam("Jurídico A"), mkTeam("Jurídico B")
	lawyer := func(team *models.Team) *models.User {
		role := testutil.CreateTestRoleWithKeys(t, db, w.org.ID, "Juridico-"+uuid.NewString()[:5], profilePerms(t, "Jurídico"))
		u := testutil.CreateTestUser(t, db, w.org.ID, testutil.WithRoleID(&role.ID))
		require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: u.ID}).Error)
		return u
	}
	a, b := lawyer(teamA), lawyer(teamB)

	// the SAC sends a case to Jurídico A
	occ := w.open(t, sac, teamA, "Notificação extrajudicial")
	w.on(occ)

	code, _ := w.get(t, a, occ)
	assert.Equal(t, fasthttp.StatusOK, code, "the lawyer of the team opens the case")
	assert.Equal(t, fasthttp.StatusOK, w.note(t, a, occ, "Recebido pelo jurídico"), "and registers updates")
	cc, raw := chatDo(t, w.app.GetOccurrenceChat, a.OrganizationID, a.ID, nil, map[string]string{"id": occ.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, cc, "and takes part in the internal discussion: %s", raw)
	ec, _, _ := w.export(t, a, occ, []string{"summary", "history"}, "pdf")
	assert.Equal(t, fasthttp.StatusOK, ec, "exports with occurrences:read")
	ec, _, _ = w.export(t, a, occ, []string{"whatsapp"}, "pdf")
	assert.Equal(t, fasthttp.StatusForbidden, ec, "but the customer's WhatsApp is not part of it")
	assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, a, occ), "no WhatsApp channel without chat permission")

	// the other Jurídico team's lawyer does not see it: scope is the team, not the profile
	code, _ = w.get(t, b, occ)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	assert.Equal(t, fasthttp.StatusForbidden, w.note(t, b, occ, "intruso"))
	// nor another organization
	code, _ = w.get(t, w.foreign, occ)
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, code)

	// opens a case of their own (allowed with occurrences:write) toward another valid team, not toward sales
	contact := testutil.CreateTestContact(t, db, w.org.ID)
	w.customerWrote(t, contact.ID)
	code, raw = chatDo(t, w.app.CreateOccurrence, a.OrganizationID, a.ID, map[string]any{
		"contact_id": contact.ID.String(), "title": "Contrato", "team_id": w.team["SAC"].ID.String()}, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code, string(raw))

	// no CRM, no conversations, no administration
	code, _ = chatDo(t, w.app.ListSalesOpportunities, a.OrganizationID, a.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "no commercial CRM")
	code, _ = chatDo(t, w.app.ListUsers, a.OrganizationID, a.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = chatDo(t, w.app.UpdateOrganizationSettings, a.OrganizationID, a.ID, map[string]any{"timezone": "America/Manaus"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	cd, _ := chatDo(t, w.app.GetContact, a.OrganizationID, a.ID, nil, map[string]string{"id": w.customerOf(t, occ)}, nil)
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, cd, "the customer's conversation is not the lawyer's")
	mc, _ := chatDo(t, w.app.GetMessages, a.OrganizationID, a.ID, nil, map[string]string{"id": w.customerOf(t, occ)}, nil)
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, mc)
}

func (w *xdWorld) customerOf(t *testing.T, occ uuid.UUID) string {
	t.Helper()
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, occ).Error)
	return o.ContactID.String()
}

// Reading the customer's conversation through a case leaves a trace: who, which case; never the content; once per
// window so the screen's refreshes do not flood the log.
func TestCaseChannelRead_IsAudited(t *testing.T) {
	w := newXDWorld(t)
	log := w.u["Log"]
	occ := w.open(t, w.u["SAC"], w.team["Log"], "Leitura auditada")
	w.on(occ)
	acct := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	require.NoError(t, w.app.DB.Create(&models.OccurrenceNotification{OrganizationID: w.org.ID, OccurrenceID: occ, Kind: "forwarded", IdempotencyKey: uuid.NewString(), Status: "sent", WhatsAppAccount: acct.Name}).Error)
	for i := 0; i < 3; i++ {
		chatDo(t, w.app.GetOccurrenceWhatsApp, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.String()}, nil)
	}
	var logs []models.AuditLog
	require.NoError(t, w.app.DB.Where("resource_type = ? AND resource_id = ? AND user_id = ? AND action = ?", "occurrence", occ, log.ID, models.AuditActionAccessed).Find(&logs).Error)
	require.Len(t, logs, 1, "three reads inside the window leave one entry")
	assert.NotContains(t, string(mustJSON(t, logs[0].Changes)), "Mensagem", "never the content")
}

// A global privilege is not a membership: the super admin appears in an organization's people only through a
// membership of that organization, and the list says whether the person is at home or a guest there.
func TestListUsers_SuperAdminIsListedOnlyThroughMembership(t *testing.T) {
	app := newTestApp(t)
	home := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	super := testutil.CreateTestUser(t, app.DB, home.ID, testutil.WithSuperAdmin())
	admin := chatUser(t, app, other.ID, []string{"users:read"})

	type row struct {
		ID         uuid.UUID `json:"id"`
		Membership string    `json:"membership"`
		IsSuper    bool      `json:"is_super_admin"`
	}
	list := func() map[uuid.UUID]row {
		code, raw := chatDo(t, app.ListUsers, other.ID, admin.ID, nil, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		out := map[uuid.UUID]row{}
		for _, u := range decodeChat[struct {
			Users []row `json:"users"`
		}](t, raw).Users {
			out[u.ID] = u
		}
		return out
	}

	assert.NotContains(t, list(), super.ID, "privilege alone puts nobody in the list")

	role := testutil.CreateTestRoleWithKeys(t, app.DB, other.ID, "guest-role", []string{"chat:read"})
	require.NoError(t, app.DB.Create(&models.UserOrganization{UserID: super.ID, OrganizationID: other.ID, RoleID: &role.ID}).Error)
	got := list()
	require.Contains(t, got, super.ID, "a real membership lists them")
	assert.Equal(t, "guest", got[super.ID].Membership)
	assert.True(t, got[super.ID].IsSuper, "the privilege is still reported, separately")
	assert.Equal(t, "home", got[admin.ID].Membership)
}
