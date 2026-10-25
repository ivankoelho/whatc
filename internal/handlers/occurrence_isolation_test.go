package handlers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// isoWorld is the SAC -> Logistics scenario: one case routed to the Logistics
// team of the Porto Seguro unit, with agents of another store around it.
type isoWorld struct {
	app                                       *handlers.App
	org                                       *models.Organization
	unitPorto, unitOutra                      *models.Unit
	teamPorto, teamOutra, teamSAC             *models.Team
	sac, logPorto, logOutra, supPorto, supSAC *models.User
	occ                                       models.Occurrence
}

func newIsoWorld(t *testing.T) *isoWorld {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	w := &isoWorld{app: app, org: org}

	mkUnit := func(name string) *models.Unit {
		u := &models.Unit{OrganizationID: org.ID, Name: name + uuid.NewString()[:6], Active: true}
		require.NoError(t, app.DB.Create(u).Error)
		return u
	}
	mkTeam := func(name string, unit *models.Unit) *models.Team {
		tm := &models.Team{OrganizationID: org.ID, Name: name + uuid.NewString()[:6], IsActive: true}
		if unit != nil {
			tm.UnitID = &unit.ID
		}
		require.NoError(t, app.DB.Create(tm).Error)
		return tm
	}
	member := func(tm *models.Team, u *models.User) {
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: tm.ID, UserID: u.ID}).Error)
	}
	user := func(name string, keys ...string) *models.User {
		role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, name+uuid.NewString()[:6], keys)
		return testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	}
	base := []string{"occurrences:read", "occurrences:write", "chat:read", "chat:write", "contacts:read", "team_chat:read", "team_chat:write"}

	w.unitPorto, w.unitOutra = mkUnit("Porto"), mkUnit("Outra")
	w.teamPorto, w.teamOutra, w.teamSAC = mkTeam("LogPorto", w.unitPorto), mkTeam("LogOutra", w.unitOutra), mkTeam("SAC", nil)
	w.sac = user("sac", base...)
	w.logPorto = user("logp", base...)
	w.logOutra = user("logo", base...)
	w.supPorto = user("supp", append([]string{"conversations:view_team"}, base...)...)
	w.supSAC = user("sups", append([]string{"conversations:view_all"}, base...)...)
	member(w.teamSAC, w.sac)
	member(w.teamPorto, w.logPorto)
	member(w.teamOutra, w.logOutra)
	member(w.teamPorto, w.supPorto)

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	w.occ = models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Atraso na entrega",
		StageID: stage.ID, OpenedByUserID: w.sac.ID, TeamID: &w.teamPorto.ID, UnitID: &w.unitPorto.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&w.occ))
	return w
}

func (w *isoWorld) get(t *testing.T, orgID, userID, occID uuid.UUID) int {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", occID.String())
	require.NoError(t, w.app.GetOccurrence(req))
	return testutil.GetResponseStatusCode(req)
}

func (w *isoWorld) listContains(t *testing.T, userID uuid.UUID, protocol string) bool {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, userID)
	require.NoError(t, w.app.ListOccurrences(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	return strings.Contains(string(testutil.GetResponseBody(req)), protocol)
}

func (w *isoWorld) claim(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, w.org.ID, userID)
	testutil.SetPathParam(req, "id", w.occ.ID.String())
	require.NoError(t, w.app.ClaimOccurrence(req))
	return testutil.GetResponseStatusCode(req)
}

func TestOccurrenceIsolation_ListAndDetailFollowCaseScope(t *testing.T) {
	w := newIsoWorld(t)
	p := w.occ.ProtocolNumber

	for name, u := range map[string]*models.User{"SAC (opener)": w.sac, "Logística Porto": w.logPorto, "Sup. Loja Porto": w.supPorto, "Sup. SAC": w.supSAC} {
		assert.True(t, w.listContains(t, u.ID, p), "%s must list the case", name)
		assert.Equal(t, fasthttp.StatusOK, w.get(t, w.org.ID, u.ID, w.occ.ID), "%s must open the case", name)
	}
	assert.False(t, w.listContains(t, w.logOutra.ID, p), "another store must not list it")
	assert.Equal(t, fasthttp.StatusForbidden, w.get(t, w.org.ID, w.logOutra.ID, w.occ.ID), "direct access by ID from another store")
}

func TestOccurrenceIsolation_OtherOrganizationCannotReachByID(t *testing.T) {
	w := newIsoWorld(t)
	org2 := testutil.CreateTestOrganization(t, w.app.DB)
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, org2.ID, "adm2", []string{"occurrences:read", "occurrences:write", "conversations:view_all"})
	intruder := testutil.CreateTestUser(t, w.app.DB, org2.ID, testutil.WithRoleID(&role.ID))
	assert.Equal(t, fasthttp.StatusNotFound, w.get(t, org2.ID, intruder.ID, w.occ.ID))
}

func TestOccurrenceIsolation_OpenerFollowsButCannotWorkATeamCase(t *testing.T) {
	w := newIsoWorld(t)
	assert.Equal(t, fasthttp.StatusForbidden, w.claim(t, w.sac.ID))
}

func TestOccurrenceIsolation_OtherStoreCannotMutate(t *testing.T) {
	w := newIsoWorld(t)
	calls := map[string]func(*fastglue.Request) error{
		"claim":  w.app.ClaimOccurrence,
		"update": w.app.UpdateOccurrence,
		"stage":  w.app.ChangeOccurrenceStage,
		"event":  w.app.CreateOccurrenceEvent,
	}
	for name, call := range calls {
		req := testutil.NewJSONRequest(t, map[string]any{"title": "x", "content": "x", "stage_id": uuid.NewString()})
		testutil.SetAuthContext(req, w.org.ID, w.logOutra.ID)
		testutil.SetPathParam(req, "id", w.occ.ID.String())
		require.NoError(t, call(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req), name)
	}
}

func TestOccurrenceIsolation_LegacyCaseWithoutScopeIsRestricted(t *testing.T) {
	w := newIsoWorld(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	stage, err := w.app.InitialStageForTest(w.org.ID)
	require.NoError(t, err)
	legacy := models.Occurrence{OrganizationID: w.org.ID, ContactID: contact.ID, Title: "Antigo",
		StageID: stage.ID, OpenedByUserID: w.sac.ID}
	require.NoError(t, w.app.CreateOccurrenceForTest(&legacy))

	assert.False(t, w.listContains(t, w.logPorto.ID, legacy.ProtocolNumber), "no team/unit: not guessed into any store")
	assert.False(t, w.listContains(t, w.supPorto.ID, legacy.ProtocolNumber))
	assert.True(t, w.listContains(t, w.sac.ID, legacy.ProtocolNumber), "opener keeps it")
	assert.True(t, w.listContains(t, w.supSAC.ID, legacy.ProtocolNumber), "view_all keeps it")
}

func TestOccurrenceIsolation_ParticipantReadsButDoesNotWork(t *testing.T) {
	w := newIsoWorld(t)
	require.NoError(t, w.app.DB.Create(&models.OccurrenceParticipant{
		OrganizationID: w.org.ID, OccurrenceID: w.occ.ID, UserID: w.logOutra.ID}).Error)
	assert.Equal(t, fasthttp.StatusOK, w.get(t, w.org.ID, w.logOutra.ID, w.occ.ID))
	assert.True(t, w.listContains(t, w.logOutra.ID, w.occ.ProtocolNumber))
	assert.Equal(t, fasthttp.StatusForbidden, w.claim(t, w.logOutra.ID))
}

// The SQL list scope and the Go check must never disagree.
func TestOccurrenceScopeSQLMatchesGo(t *testing.T) {
	w := newIsoWorld(t)
	require.NoError(t, w.app.DB.Create(&models.OccurrenceParticipant{
		OrganizationID: w.org.ID, OccurrenceID: w.occ.ID, UserID: w.logOutra.ID}).Error)
	for _, u := range []*models.User{w.sac, w.logPorto, w.logOutra, w.supPorto, w.supSAC} {
		listed := w.listContains(t, u.ID, w.occ.ProtocolNumber)
		assert.Equal(t, listed, w.app.CanViewOccurrenceByID(u.ID, w.org.ID, w.occ.ID), u.FullName)
	}
}

func TestOccurrenceIsolation_RealtimeReachesOnlyAuthorizedViewers(t *testing.T) {
	w := newIsoWorld(t)
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	hub.SetOccurrenceAuthorizer(w.app.CanViewOccurrenceByID)
	inScope := websocket.NewClient(hub, nil, w.logPorto.ID, w.org.ID)
	outScope := websocket.NewClient(hub, nil, w.logOutra.ID, w.org.ID)
	hub.Register(inScope)
	hub.Register(outScope)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.GetClientCount() != 2 {
		time.Sleep(5 * time.Millisecond)
	}
	w.app.WSHub = hub

	req := testutil.NewJSONRequest(t, map[string]any{"content": "nota interna"})
	testutil.SetAuthContext(req, w.org.ID, w.logPorto.ID)
	testutil.SetPathParam(req, "id", w.occ.ID.String())
	require.NoError(t, w.app.CreateOccurrenceEvent(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	readBroadcast(t, inScope, websocket.TypeOccurrenceEventCreated)
	assertNoBroadcast(t, outScope)
}
