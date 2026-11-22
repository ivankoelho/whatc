package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The agent-administration fields of GET /chatbot/settings (the limit, the acceptance of no limit, the isolation
// switch and the default agent) belong to whoever may read the agents (chatbot.ai:read). Everyone else still gets
// the general settings, without those keys (absent, not false/0).

var agentAdminKeys = []string{"ai_default_agent_id", "ai_monthly_token_limit", "ai_unlimited_ack", "ai_agent_only"}

func TestChatbotSettingsRead_AgentAdminFieldsFollowChatbotAIRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := &models.AIAgent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "A"}
	require.NoError(t, app.DB.Create(agent).Error)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		AI: models.AIConfig{DefaultAgentID: &agent.ID, MonthlyTokenLimit: 777, UnlimitedAck: true, AgentOnly: true}}).Error)

	mk := func(name string, keys []string) *models.User {
		role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, name, keys)
		return testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	}
	read := func(u *models.User) map[string]json.RawMessage {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, u.ID)
		require.NoError(t, app.GetChatbotSettings(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var out struct {
			Data struct {
				Settings map[string]json.RawMessage `json:"settings"`
			} `json:"data"`
		}
		testutil.ParseJSONResponse(t, req, &out)
		return out.Data.Settings
	}

	t.Run("a user with chatbot.ai:read sees the administration fields", func(t *testing.T) {
		got := read(mk("agents-reader", []string{"chatbot.ai:read"}))
		for _, k := range agentAdminKeys {
			assert.Contains(t, got, k)
		}
		assert.JSONEq(t, "777", string(got["ai_monthly_token_limit"]))
		assert.JSONEq(t, "true", string(got["ai_unlimited_ack"]))
		assert.JSONEq(t, "true", string(got["ai_agent_only"]))
	})

	t.Run("a user without it gets the general settings and none of the four keys", func(t *testing.T) {
		got := read(mk("plain-agent", []string{"chat:read", "chat:write"}))
		assert.Contains(t, got, "enabled", "the general settings are still there")
		for _, k := range agentAdminKeys {
			assert.NotContains(t, got, k, "%s must not be exposed", k)
		}
	})

	t.Run("an administrator (super admin) sees them", func(t *testing.T) {
		admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
		got := read(admin)
		for _, k := range agentAdminKeys {
			assert.Contains(t, got, k)
		}
	})

	t.Run("reading the fields is not writing them", func(t *testing.T) {
		reader := mk("agents-reader-2", []string{"chatbot.ai:read"})
		req := testutil.NewJSONRequest(t, map[string]any{"ai_agent_only": false})
		testutil.SetAuthContext(req, org.ID, reader.ID)
		require.NoError(t, app.UpdateChatbotSettings(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})
}
