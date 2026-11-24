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
	"github.com/valyala/fasthttp"
)

// B5: the agent cannot be activated by accident with no monthly token cap. A cap, or the administrator's
// explicit acceptance of none, must exist. These tests never choose a value: they only prove the guard.
// (The cap itself, its concurrency and its behavior at the ceiling are covered by ai_token_cap_test.go;
// the reservations live in the database under an advisory lock, so they hold across instances.)

type capWorld struct {
	app   *handlers.App
	org   *models.Organization
	user  *models.User
	agent *models.AIAgent
}

func newCapWorld(t *testing.T) *capWorld {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	acc := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("pilot-"+uuid.NewString()[:8]))
	agent := &models.AIAgent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "A", WhatsAppAccounts: models.StringArray{acc.Name}}
	require.NoError(t, app.DB.Create(agent).Error)
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID}).Error)
	return &capWorld{app: app, org: org, user: user, agent: agent}
}

func (w *capWorld) putSettings(t *testing.T, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.org.ID, w.user.ID)
	require.NoError(t, w.app.UpdateChatbotSettings(req))
	return testutil.GetResponseStatusCode(req)
}

func (w *capWorld) settings(t *testing.T) models.ChatbotSettings {
	t.Helper()
	var s models.ChatbotSettings
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.org.ID).First(&s).Error)
	return s
}

func (w *capWorld) updateAgent(t *testing.T, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.org.ID, w.user.ID)
	testutil.SetPathParam(req, "id", w.agent.ID.String())
	require.NoError(t, w.app.UpdateAIAgent(req))
	return testutil.GetResponseStatusCode(req)
}

// legacy: an organization that was already running before the guard existed (agent set, no cap, no ack).
func (w *capWorld) legacyRunningWithoutCap(t *testing.T) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", w.org.ID).
		Updates(map[string]any{"ai_default_agent_id": w.agent.ID, "ai_monthly_token_limit": 0, "ai_unlimited_ack": false}).Error)
}

func TestAICap_ActivatingTheDefaultAgentWithoutACapIsRefusedAndNothingChanges(t *testing.T) {
	w := newCapWorld(t)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.putSettings(t, map[string]any{"ai_default_agent_id": w.agent.ID.String()}))
	assert.Nil(t, w.settings(t).AI.DefaultAgentID, "the refused request saved nothing")
}

func TestAICap_ALimitOrAnExplicitAcceptanceUnlocksActivation(t *testing.T) {
	w := newCapWorld(t)
	assert.Equal(t, fasthttp.StatusOK, w.putSettings(t, map[string]any{"ai_default_agent_id": w.agent.ID.String(), "ai_monthly_token_limit": 12345}))
	s := w.settings(t)
	assert.EqualValues(t, 12345, s.AI.MonthlyTokenLimit)
	require.NotNil(t, s.AI.DefaultAgentID)

	w2 := newCapWorld(t) // the deliberate path: no limit, explicit acceptance
	assert.Equal(t, fasthttp.StatusOK, w2.putSettings(t, map[string]any{"ai_default_agent_id": w2.agent.ID.String(), "ai_unlimited_ack": true}))
	s2 := w2.settings(t)
	assert.True(t, s2.AI.UnlimitedAck)
	assert.Zero(t, s2.AI.MonthlyTokenLimit)
}

func TestAICap_RemovingTheCapWhileTheAgentIsActiveIsRefusedUnlessExplicit(t *testing.T) {
	w := newCapWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.putSettings(t, map[string]any{"ai_default_agent_id": w.agent.ID.String(), "ai_monthly_token_limit": 500}))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.putSettings(t, map[string]any{"ai_monthly_token_limit": 0}), "0 means no cap: not silently")
	assert.EqualValues(t, 500, w.settings(t).AI.MonthlyTokenLimit)
	assert.Equal(t, fasthttp.StatusOK, w.putSettings(t, map[string]any{"ai_monthly_token_limit": 0, "ai_unlimited_ack": true}))
}

func TestAICap_UnrelatedAISettingsOfAnOrganizationAlreadyRunningAreNotBlocked(t *testing.T) {
	w := newCapWorld(t)
	w.legacyRunningWithoutCap(t)
	assert.Equal(t, fasthttp.StatusOK, w.putSettings(t, map[string]any{"ai_model": "gpt-test"}), "the guard only holds requests that touch the agent or the cap")
}

func TestAICap_EnablingTheDefaultAgentWithoutACapIsRefused(t *testing.T) {
	w := newCapWorld(t)
	w.legacyRunningWithoutCap(t) // default agent, disabled, no cap, no ack
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.updateAgent(t, map[string]any{"is_enabled": true}))
	var a models.AIAgent
	require.NoError(t, w.app.DB.First(&a, "id = ?", w.agent.ID).Error)
	assert.False(t, a.IsEnabled, "still disabled")

	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", w.org.ID).Update("ai_monthly_token_limit", 777).Error)
	assert.Equal(t, fasthttp.StatusOK, w.updateAgent(t, map[string]any{"is_enabled": true}))
}

func TestAICap_DefaultAgentOfAnotherOrganizationIsRefused(t *testing.T) {
	w, other := newCapWorld(t), newCapWorld(t)
	assert.Equal(t, fasthttp.StatusBadRequest, w.putSettings(t, map[string]any{"ai_default_agent_id": other.agent.ID.String(), "ai_monthly_token_limit": 10}))
}

// The administrator can see the consumption: limit, used this month, remaining, and whether a cap was decided.
func TestAICap_ReadinessShowsTheConsumption(t *testing.T) {
	w := newCapWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.putSettings(t, map[string]any{"ai_default_agent_id": w.agent.ID.String(), "ai_monthly_token_limit": 1000}))
	require.NoError(t, w.app.DB.Create(&models.AIUsageLog{OrganizationID: w.org.ID, Provider: "openai", Feature: "chatbot_reply", Success: true, TotalTokens: 300}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, w.user.ID)
	testutil.SetPathParam(req, "id", w.agent.ID.String())
	require.NoError(t, w.app.GetAIAgentReadiness(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var out struct {
		Data struct {
			TokenCap struct {
				Defined   bool  `json:"defined"`
				Limit     int64 `json:"limit"`
				Used      int64 `json:"used"`
				Remaining int64 `json:"remaining"`
			} `json:"token_cap"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &out))
	assert.True(t, out.Data.TokenCap.Defined)
	assert.EqualValues(t, 1000, out.Data.TokenCap.Limit)
	assert.EqualValues(t, 300, out.Data.TokenCap.Used)
	assert.EqualValues(t, 700, out.Data.TokenCap.Remaining)
}
