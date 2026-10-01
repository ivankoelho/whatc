package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
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

// scriptedTransport replies per host with a canned status/body and records requests.
type scriptedTransport struct {
	mu     sync.Mutex
	status int
	body   string
	reqs   []*http.Request
	bodies []map[string]any
}

func (st *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil { // GET requests have no body
		b, _ = io.ReadAll(r.Body)
	}
	var parsed map[string]any
	_ = json.Unmarshal(b, &parsed)
	st.mu.Lock()
	st.reqs = append(st.reqs, r)
	st.bodies = append(st.bodies, parsed)
	status, body := st.status, st.body
	st.mu.Unlock()
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

func scripted(status int, body string) *scriptedTransport {
	return &scriptedTransport{status: status, body: body}
}

func appWith(t *testing.T, tr *scriptedTransport) *handlers.App {
	t.Helper()
	return newTestApp(t, withHTTPClient(&http.Client{Transport: tr, Timeout: 5 * time.Second}))
}

func encryptedSettings(t *testing.T, app *handlers.App, orgID uuid.UUID, provider models.AIProvider, model, key string) *models.ChatbotSettings {
	t.Helper()
	admin := aiAdmin(t, app, orgID)
	req := testutil.NewJSONRequest(t, map[string]any{
		"ai_enabled": true, "ai_provider": string(provider), "ai_model": model, "ai_api_key": key, "ai_max_tokens": 222,
		"ai_system_prompt": "You are Whatc.",
	})
	testutil.SetAuthContext(req, orgID, admin.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	s, err := app.GetChatbotSettingsCachedForTest(orgID, "")
	require.NoError(t, err)
	return s
}

func usageRows(t *testing.T, app *handlers.App, orgID uuid.UUID) []models.AIUsageLog {
	t.Helper()
	var rows []models.AIUsageLog
	require.Eventually(t, func() bool {
		rows = nil
		app.DB.Where("organization_id = ?", orgID).Order("created_at ASC").Find(&rows)
		return len(rows) > 0
	}, 3*time.Second, 20*time.Millisecond)
	return rows
}

// --- Migration of the three existing providers: same prompts, same payloads ---

func newSession(t *testing.T, app *handlers.App, orgID uuid.UUID) (*models.ChatbotSession, *models.Contact) {
	t.Helper()
	contact := testutil.CreateTestContact(t, app.DB, orgID)
	s := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, ContactID: contact.ID,
		WhatsAppAccount: "acc-x", PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive, LastActivityAt: time.Now()}
	require.NoError(t, app.DB.Create(s).Error)
	for i, m := range []struct {
		dir models.Direction
		txt string
	}{{models.DirectionIncoming, "first question"}, {models.DirectionOutgoing, "first answer"}} {
		require.NoError(t, app.DB.Create(&models.ChatbotSessionMessage{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)},
			SessionID: s.ID, Direction: m.dir, Message: m.txt}).Error)
	}
	return s, contact
}

func TestGenerateAIResponse_OpenAIAndGroqKeepTheChatbotPayload(t *testing.T) {
	for _, provider := range []models.AIProvider{models.AIProviderOpenAI, models.AIProviderGroq} {
		t.Run(string(provider), func(t *testing.T) {
			tr := scripted(200, `{"choices":[{"message":{"content":"the answer"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			app := appWith(t, tr)
			org := testutil.CreateTestOrganization(t, app.DB)
			settings := encryptedSettings(t, app, org.ID, provider, "model-1", "sk-"+string(provider))
			session, _ := newSession(t, app, org.ID)

			out, err := app.GenerateAIResponseForTest(settings, session, "now what?")
			require.NoError(t, err)
			assert.Equal(t, "the answer", out)

			require.Len(t, tr.reqs, 1)
			wantHost := map[models.AIProvider]string{models.AIProviderOpenAI: "api.openai.com", models.AIProviderGroq: "api.groq.com"}[provider]
			assert.Equal(t, wantHost, tr.reqs[0].URL.Host)
			if provider == models.AIProviderGroq {
				assert.Equal(t, "/openai/v1/chat/completions", tr.reqs[0].URL.Path)
			}
			assert.Equal(t, "Bearer sk-"+string(provider), tr.reqs[0].Header.Get("Authorization"))
			body := tr.bodies[0]
			assert.Equal(t, "model-1", body["model"])
			assert.EqualValues(t, 222, body["max_tokens"])
			msgs := body["messages"].([]any)
			require.Len(t, msgs, 4, "system + 2 history + current")
			assert.Equal(t, "system", msgs[0].(map[string]any)["role"])
			assert.Equal(t, "You are Whatc.", msgs[0].(map[string]any)["content"])
			assert.Equal(t, "user", msgs[1].(map[string]any)["role"])
			assert.Equal(t, "assistant", msgs[2].(map[string]any)["role"], "outgoing history is the assistant")
			assert.Equal(t, "now what?", msgs[3].(map[string]any)["content"])
		})
	}
}

func TestGenerateAIResponse_AnthropicKeepsSystemTopLevel(t *testing.T) {
	tr := scripted(200, `{"content":[{"type":"text","text":"claude says hi"}],"usage":{"input_tokens":8,"output_tokens":3}}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderAnthropic, "claude-x", "sk-ant-1")
	session, _ := newSession(t, app, org.ID)

	out, err := app.GenerateAIResponseForTest(settings, session, "q")
	require.NoError(t, err)
	assert.Equal(t, "claude says hi", out)
	assert.Equal(t, "api.anthropic.com", tr.reqs[0].URL.Host)
	assert.Equal(t, "sk-ant-1", tr.reqs[0].Header.Get("x-api-key"))
	assert.Equal(t, "You are Whatc.", tr.bodies[0]["system"])
	assert.Len(t, tr.bodies[0]["messages"], 3, "history + current, no system message")
}

func TestGenerateAIResponse_GoogleUsesModelRole(t *testing.T) {
	tr := scripted(200, `{"candidates":[{"content":{"parts":[{"text":"gemini says hi"}]}}]}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderGoogle, "gemini-x", "g-key")
	session, _ := newSession(t, app, org.ID)

	out, err := app.GenerateAIResponseForTest(settings, session, "q")
	require.NoError(t, err)
	assert.Equal(t, "gemini says hi", out)
	assert.Equal(t, "/v1beta/models/gemini-x:generateContent", tr.reqs[0].URL.Path)
	assert.Empty(t, tr.reqs[0].URL.RawQuery)
	contents := tr.bodies[0]["contents"].([]any)
	assert.Equal(t, "model", contents[1].(map[string]any)["role"])
}

func TestGenerateAIResponse_UnknownStoredProviderFailsCleanly(t *testing.T) {
	tr := scripted(200, "{}")
	app := appWith(t, tr)
	settings := &models.ChatbotSettings{AI: models.AIConfig{Enabled: true, Provider: "nope", Model: "m", APIKey: "k"}}
	_, err := app.GenerateAIResponseForTest(settings, nil, "hi")
	require.Error(t, err)
	assert.Empty(t, tr.reqs)
}

// --- Observability ---

func TestAIUsageLog_SuccessRecordsTokensLatencyAndContextIds(t *testing.T) {
	tr := scripted(200, `{"model":"model-1-real","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18}}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderGroq, "model-1", "sk-g")
	session, contact := newSession(t, app, org.ID)

	_, err := app.GenerateAIResponseForTest(settings, session, "hello")
	require.NoError(t, err)

	rows := usageRows(t, app, org.ID)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, "groq", r.Provider)
	assert.Equal(t, "model-1-real", r.Model, "the model the provider reports")
	assert.Equal(t, "chatbot_reply", r.Feature)
	assert.True(t, r.Success)
	assert.Equal(t, []int{12, 6, 18}, []int{r.InputTokens, r.OutputTokens, r.TotalTokens})
	assert.GreaterOrEqual(t, r.LatencyMs, 0)
	require.NotNil(t, r.ContactID)
	assert.Equal(t, contact.ID, *r.ContactID)
	require.NotNil(t, r.SessionID)
	assert.Equal(t, session.ID, *r.SessionID)
	assert.Equal(t, "acc-x", r.WhatsAppAccount)
	assert.Empty(t, r.ErrorKind)
}

func TestAIUsageLog_FailureRecordsKindStatusAndNoSecrets(t *testing.T) {
	tr := scripted(429, `{"error":{"message":"slow down sk-g please"}}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderGroq, "model-1", "sk-g")

	_, err := app.GenerateAIResponseForTest(settings, nil, "hello")
	require.Error(t, err)

	r := usageRows(t, app, org.ID)[0]
	assert.False(t, r.Success)
	assert.Equal(t, "rate_limit", r.ErrorKind)
	assert.Equal(t, 429, r.HTTPStatus)
	assert.NotContains(t, r.ErrorMessage, "sk-g")
	assert.Contains(t, r.ErrorMessage, "[redacted]")
}

func TestAIUsageLog_UnusableCredentialIsRecordedAsCredentialsFailure(t *testing.T) {
	tr := scripted(200, "{}")
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderGroq, "m", "sk-g")
	app.Config.App.EncryptionKey = "another-key-that-cannot-decrypt-0000000000"

	_, err := app.GenerateAIResponseForTest(settings, nil, "hi")
	require.ErrorIs(t, err, handlers.ErrAIKeyUnavailable)
	assert.Empty(t, tr.reqs)
	assert.Equal(t, "credentials", usageRows(t, app, org.ID)[0].ErrorKind)
}

// --- Settings: permission and provider validation ---

func TestUpdateChatbotSettings_AIFieldsNeedChatbotSettingsPermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"ai_api_key": "sk-stolen"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	var n int64
	app.DB.Raw("SELECT COUNT(*) FROM chatbot_settings WHERE organization_id = ? AND ai_api_key <> ''", org.ID).Scan(&n)
	assert.Zero(t, n, "a user without the permission cannot plant or replace a key")

	// Non-AI fields keep their existing rules.
	req = testutil.NewJSONRequest(t, map[string]any{"greeting_message": "hello"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func TestUpdateChatbotSettings_ProviderMustBeSupported(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := aiAdmin(t, app, org.ID)

	for provider, want := range map[string]int{"groq": 200, "openai": 200, "anthropic": 200, "google": 200, "mystery": 400, "": 200} {
		req := testutil.NewJSONRequest(t, map[string]any{"ai_provider": provider})
		testutil.SetAuthContext(req, org.ID, admin.ID)
		require.NoError(t, app.UpdateChatbotSettings(req))
		assert.Equal(t, want, testutil.GetResponseStatusCode(req), "provider %q", provider)
	}
}

// --- Model listing ---

func listModels(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, body map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ListAIModels(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

func TestListAIModels_UsesTheTypedKeyAndNeverReturnsOrStoresIt(t *testing.T) {
	tr := scripted(200, `{"data":[{"id":"llama-b","owned_by":"meta"},{"id":"whisper-large","owned_by":"x"},{"id":"llama-a","owned_by":"meta"}]}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := aiAdmin(t, app, org.ID)

	status, body := listModels(t, app, org.ID, admin.ID, map[string]any{"provider": "groq", "api_key": "sk-typed-123"})
	require.Equal(t, 200, status, body)
	assert.NotContains(t, body, "sk-typed-123")
	var resp struct {
		Data struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.Len(t, resp.Data.Models, 2)
	assert.Equal(t, "llama-a", resp.Data.Models[0].ID)

	require.Len(t, tr.reqs, 1)
	assert.Equal(t, "https://api.groq.com/openai/v1/models", tr.reqs[0].URL.String())
	assert.Equal(t, "Bearer sk-typed-123", tr.reqs[0].Header.Get("Authorization"))

	var n int64
	app.DB.Raw("SELECT COUNT(*) FROM chatbot_settings WHERE organization_id = ?", org.ID).Scan(&n)
	assert.Zero(t, n, "listing models stores nothing")
	assert.Equal(t, "model_list", usageRows(t, app, org.ID)[0].Feature)
}

func TestListAIModels_SavedKeyOnlyForItsOwnProvider(t *testing.T) {
	tr := scripted(200, `{"data":[{"id":"m1"}]}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := aiAdmin(t, app, org.ID)
	encryptedSettings(t, app, org.ID, models.AIProviderOpenAI, "gpt-x", "sk-openai-saved")

	status, body := listModels(t, app, org.ID, admin.ID, map[string]any{"provider": "openai"})
	require.Equal(t, 200, status, body)
	assert.Equal(t, "Bearer sk-openai-saved", tr.reqs[0].Header.Get("Authorization"), "the saved key is decrypted for the call")
	assert.NotContains(t, body, "sk-openai-saved")

	// The OpenAI key must never be sent to another provider.
	status, _ = listModels(t, app, org.ID, admin.ID, map[string]any{"provider": "groq"})
	assert.Equal(t, 400, status)
	assert.Len(t, tr.reqs, 1, "no request was made to Groq with the OpenAI key")
}

func TestListAIModels_PermissionValidationAndProviderErrors(t *testing.T) {
	tr := scripted(401, `{"error":{"message":"bad key sk-bad"}}`)
	app := appWith(t, tr)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := aiAdmin(t, app, org.ID)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	status, _ := listModels(t, app, org.ID, agent.ID, map[string]any{"provider": "groq", "api_key": "k"})
	assert.Equal(t, 403, status)
	assert.Empty(t, tr.reqs, "an unauthorized user triggers no provider call")

	status, _ = listModels(t, app, org.ID, admin.ID, map[string]any{"provider": "mystery", "api_key": "k"})
	assert.Equal(t, 400, status)

	status, body := listModels(t, app, org.ID, admin.ID, map[string]any{"provider": "groq", "api_key": "sk-bad"})
	assert.Equal(t, 400, status, "the provider rejecting the key is reported as such")
	assert.NotContains(t, body, "sk-bad")
}
