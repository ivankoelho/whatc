package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveFor calls the live panel as userID and returns the status and payload.
func liveFor(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) (int, handlers.LiveAgentsResponse) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GetLiveAgents(req))
	var resp struct {
		Data handlers.LiveAgentsResponse `json:"data"`
	}
	if testutil.GetResponseStatusCode(req) == 200 {
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	}
	return testutil.GetResponseStatusCode(req), resp.Data
}

func TestGetLiveAgents_ScopeByPermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	mkAgent := func() *models.User {
		return testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithEmail(testutil.UniqueEmail("agent")))
	}
	a1, a2, b1 := mkAgent(), mkAgent(), mkAgent()
	teamA, _ := createTestTeamWithAgent(t, app, org.ID, a1.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{
		BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: teamA.ID, UserID: a2.ID, Role: models.TeamRoleAgent,
	}).Error)
	createTestTeamWithAgent(t, app, org.ID, b1.ID)

	all := testutil.GetOrCreateTestPermissions(t, app.DB)
	pick := func(resource, action string) models.Permission {
		for _, p := range all {
			if p.Resource == resource && p.Action == action {
				return p
			}
		}
		t.Fatalf("permission %s:%s not in the default set", resource, action)
		return models.Permission{}
	}
	userWith := func(name string, perms ...models.Permission) *models.User {
		role := testutil.CreateTestRoleExact(t, app.DB, org.ID, name, false, false, perms)
		return testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail(name)), testutil.WithPassword("password"), testutil.WithRoleID(&role.ID))
	}

	ids := func(r handlers.LiveAgentsResponse) []string {
		out := make([]string, 0, len(r.Agents))
		for _, a := range r.Agents {
			out = append(out, a.AgentID)
		}
		return out
	}

	t.Run("view_team sees only the agents of the supervisor's teams", func(t *testing.T) {
		sup := userWith("Team Supervisor", pick("analytics.agents", "read"), pick("analytics.agents", "view_team"))
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: teamA.ID, UserID: sup.ID, Role: models.TeamRoleManager,
		}).Error)

		status, resp := liveFor(t, app, org.ID, sup.ID)
		require.Equal(t, 200, status)
		assert.Equal(t, "team", resp.Scope)
		assert.ElementsMatch(t, []string{a1.ID.String(), a2.ID.String()}, ids(resp))
	})

	t.Run("analytics:read keeps the whole organization", func(t *testing.T) {
		viewer := userWith("Org Viewer", pick("analytics", "read"))
		status, resp := liveFor(t, app, org.ID, viewer.ID)
		require.Equal(t, 200, status)
		assert.Equal(t, "all", resp.Scope)
		assert.Subset(t, ids(resp), []string{a1.ID.String(), a2.ID.String(), b1.ID.String()})
	})

	t.Run("agents:read alone does not open the live panel", func(t *testing.T) {
		plain := userWith("Plain Reader", pick("analytics.agents", "read"))
		status, _ := liveFor(t, app, org.ID, plain.ID)
		assert.Equal(t, 403, status)
	})

	t.Run("a supervisor with no team sees nobody", func(t *testing.T) {
		lone := userWith("Lone Supervisor", pick("analytics.agents", "read"), pick("analytics.agents", "view_team"))
		status, resp := liveFor(t, app, org.ID, lone.ID)
		require.Equal(t, 200, status)
		assert.Empty(t, resp.Agents)
	})
}
