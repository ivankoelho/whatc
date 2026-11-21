package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type alertList struct {
	Data struct {
		Alerts []handlers.OccurrenceAlertItem `json:"alerts"`
		Unseen int                            `json:"unseen"`
	} `json:"data"`
}

func listAlerts(t *testing.T, app *handlers.App, org, user uuid.UUID) alertList {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org, user)
	require.NoError(t, app.ListOccurrenceAlerts(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var out alertList
	testutil.ParseJSONResponse(t, req, &out)
	return out
}

func createOcc(t *testing.T, app *handlers.App, org, actor, contact uuid.UUID, body map[string]any) uuid.UUID {
	t.Helper()
	body["contact_id"] = contact.String()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, org, actor)
	require.NoError(t, app.CreateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ? AND title = ?", contact, body["title"]).First(&occ).Error)
	return occ.ID
}

func TestOccurrenceAlerts_AssignTransferDismissIsolation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	_ = admin
	supRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "occ-supervisor",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write", "occurrences:view_all", "conversations:view_all"})
	creator := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&supRole.ID))
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	ana := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	bob := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	sup := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "T", IsActive: true}
	require.NoError(t, app.DB.Create(team).Error)
	for u, r := range map[uuid.UUID]models.TeamRole{ana.ID: models.TeamRoleAgent, sup.ID: models.TeamRoleManager} {
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: u, Role: r}).Error)
	}

	// Opening on ana: ana gets "assigned", supervisor gets "team", creator none.
	id := createOcc(t, app, org.ID, creator.ID, contact.ID, map[string]any{"title": "n1", "team_id": team.ID.String(), "assigned_user_id": ana.ID.String()})
	a1 := listAlerts(t, app, org.ID, ana.ID)
	require.Len(t, a1.Data.Alerts, 1)
	assert.Equal(t, "assigned", a1.Data.Alerts[0].Kind)
	assert.Equal(t, 1, a1.Data.Unseen)
	assert.NotEmpty(t, a1.Data.Alerts[0].ProtocolNumber)
	s1 := listAlerts(t, app, org.ID, sup.ID)
	require.Len(t, s1.Data.Alerts, 1)
	assert.Equal(t, "team", s1.Data.Alerts[0].Kind)
	assert.Empty(t, listAlerts(t, app, org.ID, creator.ID).Data.Alerts, "the actor is never notified")

	// A plain open protocol is NOT a notification: self-assigned creates none.
	createOcc(t, app, org.ID, creator.ID, contact.ID, map[string]any{"title": "mine"})
	assert.Empty(t, listAlerts(t, app, org.ID, creator.ID).Data.Alerts)

	// Seen vs new.
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, ana.ID)
	require.NoError(t, app.MarkOccurrenceAlertsSeen(req))
	a2 := listAlerts(t, app, org.ID, ana.ID)
	assert.Equal(t, 0, a2.Data.Unseen)
	require.Len(t, a2.Data.Alerts, 1, "seen is not dismissed")

	// Transfer ana -> bob: ana's is cleared, bob gets "transferred".
	req = testutil.NewJSONRequest(t, map[string]any{"title": "n1", "assigned_user_id": bob.ID.String()})
	testutil.SetAuthContext(req, org.ID, creator.ID)
	testutil.SetPathParam(req, "id", id.String())
	require.NoError(t, app.UpdateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Empty(t, listAlerts(t, app, org.ID, ana.ID).Data.Alerts)
	b := listAlerts(t, app, org.ID, bob.ID)
	require.Len(t, b.Data.Alerts, 1)
	assert.Equal(t, "transferred", b.Data.Alerts[0].Kind)

	// Another user cannot dismiss bob's notification; bob can, and it stays gone.
	nid := b.Data.Alerts[0].ID.String()
	req = testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, ana.ID)
	testutil.SetPathParam(req, "id", nid)
	require.NoError(t, app.DismissOccurrenceAlert(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

	req = testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, bob.ID)
	testutil.SetPathParam(req, "id", nid)
	require.NoError(t, app.DismissOccurrenceAlert(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Empty(t, listAlerts(t, app, org.ID, bob.ID).Data.Alerts)

	// Other organisation sees nothing, even for the same user id.
	other := testutil.CreateTestOrganization(t, app.DB)
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, other.ID, sup.ID)
	require.NoError(t, app.ListOccurrenceAlerts(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req), "no membership in that organisation")
}
