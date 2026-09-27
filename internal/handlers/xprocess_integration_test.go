package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_UpsertXProcessIntegration_CreatesEncrypted(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:read", "xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{
		"base_url": "https://api.atacadaodospisos.com.br",
		"api_key":  "plain-key-123",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.XProcessIntegration
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&stored).Error)
	assert.NotEqual(t, "plain-key-123", stored.APIKey, "api key must be encrypted at rest")
	stored.DecryptSecrets(app.Config.App.EncryptionKey)
	assert.Equal(t, "plain-key-123", stored.APIKey)

	// Response body must never carry the key back, encrypted or not.
	body := string(testutil.GetResponseBody(req))
	assert.NotContains(t, body, "plain-key-123")
	assert.NotContains(t, body, "api_key")
}

func TestApp_UpsertXProcessIntegration_UpdatesExisting(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	first := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://old.example.com", "api_key": "key-1"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(first))

	second := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://new.example.com", "api_key": "key-2"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(second))

	var all []models.XProcessIntegration
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).Find(&all).Error)
	require.Len(t, all, 1, "upsert must update the single row, not create a second one")
	assert.Equal(t, "https://new.example.com", all[0].BaseURL)
}

func TestApp_GetXProcessIntegration_NeverReturnsKey(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:read", "xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	upsert := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://api.atacadaodospisos.com.br", "api_key": "secret-key"})
	testutil.SetAuthContext(upsert, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(upsert))

	get := testutil.NewGETRequest(t)
	testutil.SetAuthContext(get, org.ID, user.ID)
	require.NoError(t, app.GetXProcessIntegration(get))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(get))
	body := string(testutil.GetResponseBody(get))
	assert.NotContains(t, body, "secret-key")

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			BaseURL      string `json:"base_url"`
			IsConfigured bool   `json:"is_configured"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(get), &resp))
	assert.Equal(t, "https://api.atacadaodospisos.com.br", resp.Data.BaseURL)
	assert.True(t, resp.Data.IsConfigured)
}

func TestApp_TestXProcessIntegrationConnection(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/liveness" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The real xprocess.Client.ConsultarPedido posts to /api/pedido
		// (see pkg/xprocess/client.go), not /api/vendedores.
		assert.Equal(t, "/api/pedido", r.URL.Path)
		assert.Equal(t, "the-key", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pedido":[]}`))
	}))
	defer meta.Close()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"base_url": meta.URL, "api_key": "the-key"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.TestXProcessIntegrationConnection(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func TestApp_TestXProcessIntegrationConnection_Unreachable(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"base_url": "http://127.0.0.1:1", "api_key": "the-key"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.TestXProcessIntegrationConnection(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadGateway, "")
}
