package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type attendanceResp struct {
	Data struct {
		Scope  string                        `json:"scope"`
		Agents []handlers.AgentAttendanceRow `json:"agents"`
	} `json:"data"`
}

func getAttendance(t *testing.T, app *handlers.App, org, user uuid.UUID, q map[string]string) (int, attendanceResp) {
	t.Helper()
	// The report's default period is "today so far" in the organisation's time zone. These tests build their
	// events relative to now ("1h ago"), which falls on the previous calendar day during the first hour after
	// local midnight, so the default period would leave them out and the test would fail only at that hour.
	// Ask for a window that always contains them: from two days back, to tomorrow (the end is capped at now).
	if _, ok := q["from"]; !ok {
		merged := map[string]string{
			"from": time.Now().AddDate(0, 0, -2).UTC().Format("2006-01-02"),
			"to":   time.Now().AddDate(0, 0, 1).UTC().Format("2006-01-02"),
		}
		for k, v := range q {
			merged[k] = v
		}
		q = merged
	}
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org, user)
	for k, v := range q {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, app.GetAgentAttendance(req))
	var out attendanceResp
	if testutil.GetResponseStatusCode(req) == fasthttp.StatusOK {
		testutil.ParseJSONResponse(t, req, &out)
	}
	return testutil.GetResponseStatusCode(req), out
}

func TestAgentAttendance_NoLoginFilterScopeAndIsolation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	sup := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "sup", []string{"analytics:read"}).ID))
	plainRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "plain", []string{"chat:read"})
	ana := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&plainRole.ID), testutil.WithFullName("Ana"))
	bob := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&plainRole.ID), testutil.WithFullName("Bob"))

	// no permission -> 403
	code, _ := getAttendance(t, app, org.ID, ana.ID, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// Ana logged in 1h ago (still active) and was away 20 minutes; Bob never logged in.
	now := time.Now()
	require.NoError(t, app.DB.Create(&models.UserSessionEvent{UserID: ana.ID, OrganizationID: org.ID, Event: "login", SessionID: "s1", CreatedAt: now.Add(-time.Hour)}).Error)
	end := now.Add(-20 * time.Minute)
	require.NoError(t, app.DB.Create(&models.AgentPresenceLog{OrganizationID: org.ID, UserID: ana.ID, State: models.AgentStateAway,
		StartedAt: now.Add(-40 * time.Minute), EndedAt: &end}).Error)

	code, all := getAttendance(t, app, org.ID, sup.ID, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	byName := map[string]handlers.AgentAttendanceRow{}
	for _, r := range all.Data.Agents {
		byName[r.Name] = r
	}
	assert.Equal(t, 1, byName["Ana"].Logins)
	assert.Equal(t, 1, byName["Ana"].ActiveSessions)
	assert.Equal(t, 20, byName["Ana"].AwayMinutes)
	assert.True(t, byName["Bob"].NoLoginRecord, "no login is 'sem registro', not 'absent'")

	_, none := getAttendance(t, app, org.ID, sup.ID, map[string]string{"only": "no_login"})
	for _, r := range none.Data.Agents {
		assert.True(t, r.NoLoginRecord)
		assert.NotEqual(t, "Ana", r.Name)
	}

	// filter by agent
	_, one := getAttendance(t, app, org.ID, sup.ID, map[string]string{"agent_id": bob.ID.String()})
	require.Len(t, one.Data.Agents, 1)
	assert.Equal(t, "Bob", one.Data.Agents[0].Name)

	// another organisation sees none of these agents
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	otherSup := testutil.CreateTestUser(t, app.DB, otherOrg.ID, testutil.WithRoleID(&testutil.CreateTestRoleWithKeys(t, app.DB, otherOrg.ID, "sup2", []string{"analytics:read"}).ID))
	_, foreign := getAttendance(t, app, otherOrg.ID, otherSup.ID, nil)
	for _, r := range foreign.Data.Agents {
		assert.NotContains(t, []string{"Ana", "Bob"}, r.Name)
	}

	// team scope: view_team supervisor only sees teammates
	teamRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "teamsup", []string{"analytics.agents:view_team"})
	lead := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&teamRole.ID))
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "T", IsActive: true}
	require.NoError(t, app.DB.Create(team).Error)
	for _, u := range []*models.User{lead, ana} {
		require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
	}
	_, scoped := getAttendance(t, app, org.ID, lead.ID, nil)
	assert.Equal(t, "team", scoped.Data.Scope)
	for _, r := range scoped.Data.Agents {
		assert.NotEqual(t, "Bob", r.Name, "outside the supervisor's team")
	}
}

// Browser logout: the refresh cookie never reaches /api/auth/logout (its path is
// /api/auth/refresh), so the session must be identified by its own cookie. Two
// devices are two sessions: logging one out leaves the other active.
func TestSessionEvents_LoginLogoutPerDevice(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	email, password := "agent-"+uuid.NewString()[:8]+"@example.com", "pw-12345"
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(email), testutil.WithPassword(password))

	login := func(ua string) map[string]string {
		req := testutil.NewJSONRequest(t, map[string]string{"email": email, "password": password})
		req.RequestCtx.Request.Header.SetUserAgent(ua)
		require.NoError(t, app.Login(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		cookies := map[string]string{}
		for k, v := range req.RequestCtx.Response.Header.Cookies() {
			var c fasthttp.Cookie
			if c.ParseBytes(v) == nil {
				cookies[string(k)] = string(c.Value())
			}
		}
		return cookies
	}
	desktop, mobile := login("Mozilla/5.0 (Windows)"), login("Mozilla/5.0 (iPhone) Mobile")
	require.NotEmpty(t, desktop["whm_sid"])
	require.NotEqual(t, desktop["whm_sid"], mobile["whm_sid"])

	logout := func(c map[string]string) {
		req := testutil.NewJSONRequest(t, map[string]any{})
		req.RequestCtx.Request.Header.SetCookie("whm_sid", c["whm_sid"])
		req.RequestCtx.Request.Header.SetCookie("whm_access", c["whm_access"]) // no refresh cookie, like a real browser
		require.NoError(t, app.Logout(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	}
	logout(desktop)
	logout(desktop) // a retried logout must not duplicate the event

	var evs []models.UserSessionEvent
	require.NoError(t, app.DB.Where("user_id = ?", user.ID).Order("created_at").Find(&evs).Error)
	count := map[string]int{}
	for _, e := range evs {
		count[e.Event]++
		if e.Event == "logout" {
			assert.Equal(t, desktop["whm_sid"], e.SessionID)
			assert.Equal(t, "manual", e.Reason)
		}
	}
	assert.Equal(t, 2, count["login"])
	assert.Equal(t, 1, count["logout"], "deduplicated, and the other device's session is untouched")

	// the report agrees: one manual end, one still active
	sessions, _, active := handlers.SessionsForTest(evs, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), time.Now(), 24*time.Hour)
	require.Len(t, sessions, 2)
	assert.Equal(t, 1, active)
}
