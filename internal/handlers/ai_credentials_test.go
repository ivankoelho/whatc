package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const secretKey = "sk-test-SECRET-0123456789"

// recordingTransport answers every provider call with a canned OpenAI-shaped
// reply and remembers what was sent, so a test can assert on the credential.
type recordingTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	rt.mu.Lock()
	rt.requests = append(rt.requests, r)
	rt.bodies = append(rt.bodies, string(b))
	rt.mu.Unlock()
	body := `{"choices":[{"message":{"content":" hello "}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

func (rt *recordingTransport) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.requests)
}

// aiAdmin is a user allowed to change AI settings (settings.chatbot:write).
func aiAdmin(t *testing.T, app *handlers.App, orgID uuid.UUID) *models.User {
	t.Helper()
	role := testutil.CreateAdminRole(t, app.DB, orgID)
	return testutil.CreateTestUser(t, app.DB, orgID, testutil.WithRoleID(&role.ID))
}

func saveAIKeyViaAPI(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, key string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{
		"ai_enabled": true, "ai_provider": "openai", "ai_model": "gpt-4o-mini", "ai_api_key": key,
	})
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.UpdateChatbotSettings(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func storedAIKey(t *testing.T, app *handlers.App, orgID uuid.UUID) string {
	t.Helper()
	var stored string
	require.NoError(t, app.DB.Raw("SELECT ai_api_key FROM chatbot_settings WHERE organization_id = ? AND whats_app_account = ''", orgID).Scan(&stored).Error)
	return stored
}

func TestAIKey_IsEncryptedAtRestAndNeverReturned(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)

	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)

	stored := storedAIKey(t, app, org.ID)
	assert.True(t, crypto.IsEncrypted(stored), "the database holds ciphertext")
	assert.NotContains(t, stored, secretKey)

	// Reading settings back: configured flag yes, key text never.
	getReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(getReq, org.ID, user.ID)
	require.NoError(t, app.GetChatbotSettings(getReq))
	raw := string(testutil.GetResponseBody(getReq))
	assert.NotContains(t, raw, secretKey)
	assert.NotContains(t, raw, stored, "not even the ciphertext is returned")
	var resp struct {
		Data struct {
			Settings handlers.ChatbotSettingsResponse `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	assert.True(t, resp.Data.Settings.AIAPIKeyConfigured)
}

func TestAIKey_SavingOtherSettingsDoesNotWipeOrReEncryptTheKey(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)
	before := storedAIKey(t, app, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"ai_max_tokens": 321})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))

	assert.Equal(t, before, storedAIKey(t, app, org.ID), "an update that omits the key leaves it untouched")
}

func TestAIKey_RedisCacheHoldsCiphertextNeverPlaintext(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)

	settings, err := app.GetChatbotSettingsCachedForTest(org.ID, "") // populates the cache
	require.NoError(t, err)
	assert.True(t, crypto.IsEncrypted(settings.AI.APIKey), "in-memory settings carry ciphertext")

	ctx := context.Background()
	keys, err := app.Redis.Keys(ctx, "chatbot:settings:"+org.ID.String()+":*").Result()
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	for _, k := range keys {
		v, err := app.Redis.Get(ctx, k).Result()
		require.NoError(t, err)
		assert.NotContains(t, v, secretKey, "plaintext key must never be written to Redis")
		assert.Contains(t, v, "enc:", "the encrypted form is what is cached")
	}
}

func TestAIKey_ProviderCallUsesDecryptedKeyOnlyAtCallTime(t *testing.T) {
	rt := &recordingTransport{}
	app := newTestApp(t, withHTTPClient(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)
	settings, err := app.GetChatbotSettingsCachedForTest(org.ID, "")
	require.NoError(t, err)

	out, err := app.GenerateAIResponseForTest(settings, nil, "hi")
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
	require.Equal(t, 1, rt.count())
	assert.Equal(t, "Bearer "+secretKey, rt.requests[0].Header.Get("Authorization"), "the provider gets the real key")
	assert.True(t, crypto.IsEncrypted(settings.AI.APIKey), "the settings struct still holds ciphertext afterwards")
}

func TestAIKey_UndecryptableKeyNeverReachesTheProvider(t *testing.T) {
	rt := &recordingTransport{}
	app := newTestApp(t, withHTTPClient(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)
	settings, err := app.GetChatbotSettingsCachedForTest(org.ID, "")
	require.NoError(t, err)

	// The encryption key changes after the key was stored.
	app.Config.App.EncryptionKey = "a-completely-different-encryption-key-0000000"
	_, err = app.GenerateAIResponseForTest(settings, nil, "hi")
	require.ErrorIs(t, err, handlers.ErrAIKeyUnavailable)
	assert.Zero(t, rt.count(), "ciphertext must never be sent to a provider as if it were the key")

	// And with no encryption key configured at all.
	app.Config.App.EncryptionKey = ""
	_, err = app.GenerateAIResponseForTest(settings, nil, "hi")
	require.ErrorIs(t, err, handlers.ErrAIKeyUnavailable)
	assert.Zero(t, rt.count())
}

func TestAIKey_LegacyPlaintextStillWorksUntilMigrated(t *testing.T) {
	rt := &recordingTransport{}
	app := newTestApp(t, withHTTPClient(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	settings := &models.ChatbotSettings{AI: models.AIConfig{Enabled: true, Provider: models.AIProviderOpenAI, Model: "m", APIKey: "sk-legacy-plain"}}

	_, err := app.GenerateAIResponseForTest(settings, nil, "hi")
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-legacy-plain", rt.requests[0].Header.Get("Authorization"))
}

func TestAIKey_GoogleKeyIsSentInHeaderNotInTheURL(t *testing.T) {
	rt := &recordingTransport{}
	app := newTestApp(t, withHTTPClient(&http.Client{Transport: rt, Timeout: 5 * time.Second}))
	settings := &models.ChatbotSettings{AI: models.AIConfig{Enabled: true, Provider: models.AIProviderGoogle, Model: "gemini-x", APIKey: "g-key-123"}}

	_, _ = app.GenerateAIResponseForTest(settings, nil, "hi") // the canned reply is not Gemini-shaped; only the request matters
	require.Equal(t, 1, rt.count())
	assert.NotContains(t, rt.requests[0].URL.String(), "g-key-123")
	assert.Empty(t, rt.requests[0].URL.RawQuery)
	assert.Equal(t, "g-key-123", rt.requests[0].Header.Get("x-goog-api-key"))
}

func TestAIKey_NoEncryptionKeyRefusesToStorePlaintext(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	app.Config.App.EncryptionKey = ""

	req := testutil.NewJSONRequest(t, map[string]any{
		"ai_enabled": true, "ai_provider": "openai", "ai_model": "gpt-4o-mini", "ai_api_key": secretKey,
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))

	assert.Equal(t, fasthttp.StatusServiceUnavailable, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, "AIEncryptionKeyUnavailable", "the frontend can tell this from a bad key")
	assert.NotContains(t, body, secretKey)

	var count int64
	require.NoError(t, app.DB.Raw("SELECT count(*) FROM chatbot_settings WHERE organization_id = ?", org.ID).Scan(&count).Error)
	assert.Zero(t, count, "nothing was persisted, not even the other fields")
}

func TestAIKey_NoEncryptionKeyLeavesExistingKeyIntact(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := aiAdmin(t, app, org.ID)
	saveAIKeyViaAPI(t, app, org.ID, user.ID, secretKey)
	before := storedAIKey(t, app, org.ID)
	require.True(t, crypto.IsEncrypted(before))

	app.Config.App.EncryptionKey = ""
	req := testutil.NewJSONRequest(t, map[string]any{"ai_provider": "anthropic", "ai_model": "other", "ai_api_key": "sk-new-REPLACEMENT"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UpdateChatbotSettings(req))

	assert.Equal(t, fasthttp.StatusServiceUnavailable, testutil.GetResponseStatusCode(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), "sk-new-REPLACEMENT")
	assert.Equal(t, before, storedAIKey(t, app, org.ID), "neither replaced by plaintext nor erased")
	var provider string
	require.NoError(t, app.DB.Raw("SELECT ai_provider FROM chatbot_settings WHERE organization_id = ? AND whats_app_account = ''", org.ID).Scan(&provider).Error)
	assert.Equal(t, "openai", provider, "other fields of the rejected request were not applied")
}

func TestAIKey_ResolveWithValidKeyReturnsPlaintext(t *testing.T) {
	app := newTestApp(t)
	enc, err := crypto.Encrypt(secretKey, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	got, err := app.ResolveAIAPIKeyForTest(&models.ChatbotSettings{AI: models.AIConfig{APIKey: enc}})
	require.NoError(t, err)
	assert.Equal(t, secretKey, got)
}
