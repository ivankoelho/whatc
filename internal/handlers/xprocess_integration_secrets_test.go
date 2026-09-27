package handlers

// This file is `package handlers` (white-box), not `handlers_test`, because
// the end-to-end tests below need newGraphTestFixtures/runChatGraph/
// chatGraphPath (chatbot_graph_runner_test.go), which are unexported and
// live in this same internal package. newTestApp (testhelpers_test.go) is
// declared in the separate `handlers_test` package and is therefore not
// visible here — all fixtures in this file go through newProcessorTestApp
// instead, which is the internal package's own App constructor.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveIntegrationSecrets_ReplacesPlaceholder(t *testing.T) {
	app := newProcessorTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://api.atacadaodospisos.com.br", APIKey: "real-secret-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	config := map[string]any{
		"url":     "https://api.atacadaodospisos.com.br/api/pedido",
		"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}", "content-type": "application/json"},
	}

	resolved, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	require.NoError(t, err)
	headers := resolved["headers"].(map[string]any)
	assert.Equal(t, "real-secret-key", headers["x-api-key"])
	assert.Equal(t, "application/json", headers["content-type"], "other headers must pass through unchanged")

	// The original config passed in must not be mutated — resolving a
	// secret returns a copy.
	originalHeaders := config["headers"].(map[string]any)
	assert.Equal(t, "{{integrations.xprocess.api_key}}", originalHeaders["x-api-key"])
}

func TestResolveIntegrationSecrets_NoPlaceholder_PassesThroughUnchanged(t *testing.T) {
	app := newProcessorTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	config := map[string]any{"url": "https://example.com", "headers": map[string]any{"Authorization": "Bearer abc"}}

	resolved, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	require.NoError(t, err)
	headers := resolved["headers"].(map[string]any)
	assert.Equal(t, "Bearer abc", headers["Authorization"])
}

func TestResolveIntegrationSecrets_PlaceholderButNoIntegration_FailsClosed(t *testing.T) {
	app := newProcessorTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	config := map[string]any{"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"}}

	_, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	assert.Error(t, err, "must fail rather than send the literal placeholder string to a third party")
}

func TestResolveIntegrationSecrets_PlaceholderButIntegrationInactive_FailsClosed(t *testing.T) {
	app := newProcessorTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	// Created active then flipped to false via Update: the model's
	// `gorm:"default:true"` tag makes GORM omit an explicit false (its Go
	// zero value) from the INSERT, so Create alone would silently leave it
	// active (same footgun documented in xprocess_reconciler_test.go and
	// goroutines_test.go for models.Webhook).
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://x", APIKey: "key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)
	require.NoError(t, app.DB.Model(&integ).Update("is_active", false).Error)

	config := map[string]any{"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"}}
	_, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	assert.Error(t, err)
}

func TestExecChatAPICall_UsesXProcessIntegration_EndToEnd(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pedido":[]}`))
	}))
	defer srv.Close()

	app, org, account, contact, session := newGraphTestFixtures(t)
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://unused.example.com", APIKey: "real-secret-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	flow := &models.ChatbotFlow{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "xprocess-lookup",
		IsEnabled:       true,
		Graph: models.JSONB{
			"version":    2,
			"entry_node": "api",
			"nodes": []any{
				map[string]any{"id": "api", "type": "api_call", "label": "consultar pedido", "config": map[string]any{
					"url":     srv.URL,
					"method":  "POST",
					"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"},
					"body":    `{"numero_pedido":"666","documento":"12345678900"}`,
				}},
				map[string]any{"id": "end", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "api", "to": "end", "condition": "http:2xx"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))
	require.NoError(t, app.DB.First(session, session.ID).Error)
	assert.Equal(t, models.SessionStatusCompleted, session.Status)
	assert.Equal(t, "real-secret-key", gotAuth, "the flow's api_call node must send the real, decrypted X2 key — never the literal placeholder")
}

func TestExecChatAPICall_XProcessPlaceholderButNoIntegration_RoutesNon2xx(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)
	_ = org // no XProcessIntegration created for this org on purpose

	flow := &models.ChatbotFlow{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "xprocess-lookup-unconfigured",
		IsEnabled:       true,
		Graph: models.JSONB{
			"version":    2,
			"entry_node": "api",
			"nodes": []any{
				map[string]any{"id": "api", "type": "api_call", "label": "consultar pedido", "config": map[string]any{
					"url":     "https://unused.example.com/api/pedido",
					"method":  "POST",
					"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"},
				}},
				map[string]any{"id": "end", "type": "end"},
				map[string]any{"id": "fallback", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "api", "to": "end", "condition": "http:2xx"},
				map[string]any{"from": "api", "to": "fallback", "condition": "http:non2xx"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))
	require.NoError(t, app.DB.First(session, session.ID).Error)
	assert.Equal(t, models.SessionStatusCompleted, session.Status)

	path := chatGraphPath(t, session)
	require.GreaterOrEqual(t, len(path), 2)
	assert.Equal(t, "http:non2xx", path[0]["outcome"], "an unconfigured integration must fail closed via http:non2xx, never send the literal placeholder")
}
