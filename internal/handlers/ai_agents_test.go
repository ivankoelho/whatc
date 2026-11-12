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

func TestAIAgents_CRUD(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	acc := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("pilot-"+uuid.NewString()[:8]))

	// create
	req := testutil.NewJSONRequest(t, map[string]any{
		"name": "Atendente Comercial", "objective": "Qualificar compras",
		"instructions": "Pergunte metragem e ambiente", "company_info": "Atacadao dos Pisos",
		"tone": "cordial", "handoff_rules": "Transferir quando houver intencao de compra",
		"is_enabled": true, "whatsapp_accounts": []string{acc.Name},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateAIAgent(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &created))
	id, err := uuid.Parse(created.Data.ID)
	require.NoError(t, err)

	var stored models.AIAgent
	require.NoError(t, app.DB.First(&stored, "id = ?", id).Error)
	assert.Equal(t, org.ID, stored.OrganizationID)
	assert.Equal(t, "Atendente Comercial", stored.Name)
	assert.True(t, stored.IsEnabled)
	assert.Equal(t, models.StringArray{acc.Name}, stored.WhatsAppAccounts)

	// get
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", id.String())
	require.NoError(t, app.GetAIAgent(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	// update only touches what is sent
	req = testutil.NewJSONRequest(t, map[string]any{"tone": "direto"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", id.String())
	require.NoError(t, app.UpdateAIAgent(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	require.NoError(t, app.DB.First(&stored, "id = ?", id).Error)
	assert.Equal(t, "direto", stored.Tone)
	assert.Equal(t, "Qualificar compras", stored.Objective)
	assert.Equal(t, models.StringArray{acc.Name}, stored.WhatsAppAccounts)

	// list
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListAIAgents(req))
	var list struct {
		Data struct {
			Agents []models.AIAgent `json:"agents"`
			Total  int              `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &list))
	assert.Equal(t, 1, list.Data.Total)

	// delete clears the default pointer
	require.NoError(t, app.DB.Create(&models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		AI: models.AIConfig{DefaultAgentID: &id},
	}).Error)
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", id.String())
	require.NoError(t, app.DeleteAIAgent(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var n int64
	app.DB.Model(&models.AIAgent{}).Where("id = ?", id).Count(&n)
	assert.Equal(t, int64(0), n)
	var cs models.ChatbotSettings
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&cs).Error)
	assert.Nil(t, cs.AI.DefaultAgentID)
}

func TestAIAgents_OrgIsolation(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	userB := testutil.CreateTestUser(t, app.DB, orgB.ID, testutil.WithSuperAdmin())
	agent := models.AIAgent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID, Name: "A"}
	require.NoError(t, app.DB.Create(&agent).Error)

	for name, call := range map[string]func(*testing.T) int{
		"get": func(t *testing.T) int {
			req := testutil.NewGETRequest(t)
			testutil.SetAuthContext(req, orgB.ID, userB.ID)
			testutil.SetPathParam(req, "id", agent.ID.String())
			require.NoError(t, app.GetAIAgent(req))
			return testutil.GetResponseStatusCode(req)
		},
		"update": func(t *testing.T) int {
			req := testutil.NewJSONRequest(t, map[string]any{"name": "hacked"})
			testutil.SetAuthContext(req, orgB.ID, userB.ID)
			testutil.SetPathParam(req, "id", agent.ID.String())
			require.NoError(t, app.UpdateAIAgent(req))
			return testutil.GetResponseStatusCode(req)
		},
		"delete": func(t *testing.T) int {
			req := testutil.NewGETRequest(t)
			testutil.SetAuthContext(req, orgB.ID, userB.ID)
			testutil.SetPathParam(req, "id", agent.ID.String())
			require.NoError(t, app.DeleteAIAgent(req))
			return testutil.GetResponseStatusCode(req)
		},
		"readiness": func(t *testing.T) int {
			req := testutil.NewGETRequest(t)
			testutil.SetAuthContext(req, orgB.ID, userB.ID)
			testutil.SetPathParam(req, "id", agent.ID.String())
			require.NoError(t, app.GetAIAgentReadiness(req))
			return testutil.GetResponseStatusCode(req)
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, fasthttp.StatusNotFound, call(t))
		})
	}
	var still models.AIAgent
	require.NoError(t, app.DB.First(&still, "id = ?", agent.ID).Error)
	assert.Equal(t, "A", still.Name)
}

func TestAIAgents_RejectsForeignAccountName(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	userA := testutil.CreateTestUser(t, app.DB, orgA.ID, testutil.WithSuperAdmin())
	foreign := testutil.CreateTestWhatsAppAccountWith(t, app.DB, orgB.ID, testutil.WithAccountName("foreign-"+uuid.NewString()[:8]))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "x", "whatsapp_accounts": []string{foreign.Name}})
	testutil.SetAuthContext(req, orgA.ID, userA.ID)
	require.NoError(t, app.CreateAIAgent(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestChatbotSettings_AgentFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	other := testutil.CreateTestOrganization(t, app.DB)
	agent := models.AIAgent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "A"}
	foreign := models.AIAgent{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: other.ID, Name: "B"}
	require.NoError(t, app.DB.Create(&agent).Error)
	require.NoError(t, app.DB.Create(&foreign).Error)

	put := func(body map[string]any) int {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, org.ID, user.ID)
		require.NoError(t, app.UpdateChatbotSettings(req))
		return testutil.GetResponseStatusCode(req)
	}

	require.Equal(t, fasthttp.StatusOK, put(map[string]any{
		"ai_default_agent_id": agent.ID.String(), "ai_monthly_token_limit": 250000,
	}))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.GetChatbotSettings(req))
	var got struct {
		Data struct {
			Settings struct {
				AgentID *string `json:"ai_default_agent_id"`
				Limit   int64   `json:"ai_monthly_token_limit"`
			} `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &got))
	require.NotNil(t, got.Data.Settings.AgentID)
	assert.Equal(t, agent.ID.String(), *got.Data.Settings.AgentID)
	assert.Equal(t, int64(250000), got.Data.Settings.Limit)

	// another organization's agent, a malformed id and a negative cap are refused
	assert.Equal(t, fasthttp.StatusBadRequest, put(map[string]any{"ai_default_agent_id": foreign.ID.String()}))
	assert.Equal(t, fasthttp.StatusBadRequest, put(map[string]any{"ai_default_agent_id": "nope"}))
	assert.Equal(t, fasthttp.StatusBadRequest, put(map[string]any{"ai_monthly_token_limit": -1}))

	// "" clears the default
	require.Equal(t, fasthttp.StatusOK, put(map[string]any{"ai_default_agent_id": ""}))
	var cs models.ChatbotSettings
	require.NoError(t, app.DB.Where("organization_id = ? AND whats_app_account = ''", org.ID).First(&cs).Error)
	assert.Nil(t, cs.AI.DefaultAgentID)
}
