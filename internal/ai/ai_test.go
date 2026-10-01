package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const key = "sk-live-SECRET-9999"

type seen struct {
	method, path, rawQuery string
	header                 http.Header
	body                   map[string]any
}

// server replies with status/body and records the request it got.
func server(t *testing.T, status int, reply string, extra map[string]string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.rawQuery, got.header = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &got.body)
		}
		for k, v := range extra {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func mk(t *testing.T, name string, srv *httptest.Server) ai.Provider {
	t.Helper()
	p, err := ai.New(name, ai.Config{APIKey: key, BaseURL: srv.URL})
	require.NoError(t, err)
	return p
}

var chat = ai.Request{
	Model: "m-1", System: "be brief", MaxTokens: 123, Temperature: 0.4,
	Messages: []ai.Message{
		{Role: ai.RoleUser, Content: "hi"},
		{Role: ai.RoleAssistant, Content: "hello"},
		{Role: ai.RoleUser, Content: "again"},
	},
}

func TestNew_KnownAndUnknownProviders(t *testing.T) {
	for _, n := range ai.Supported() {
		p, err := ai.New(n, ai.Config{APIKey: "k"})
		require.NoError(t, err, n)
		assert.Equal(t, n, p.Name())
		assert.True(t, p.Capabilities().ListModels)
	}
	_, err := ai.New("nope", ai.Config{})
	assert.ErrorIs(t, err, ai.ErrUnsupportedProvider)
	assert.Equal(t, []string{"openai", "anthropic", "google", "groq"}, ai.Supported())
}

// --- OpenAI / Groq (Chat Completions dialect) ---

const openAIReply = `{"model":"m-1-0613","choices":[{"message":{"content":"  pong  "}}],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`

func TestOpenAICompat_CompleteShapeForOpenAIAndGroq(t *testing.T) {
	for _, name := range []string{ai.ProviderOpenAI, ai.ProviderGroq} {
		t.Run(name, func(t *testing.T) {
			srv, got := server(t, 200, openAIReply, nil)
			resp, err := mk(t, name, srv).Complete(context.Background(), chat)
			require.NoError(t, err)

			assert.Equal(t, http.MethodPost, got.method)
			assert.Equal(t, "/chat/completions", got.path)
			assert.Equal(t, "Bearer "+key, got.header.Get("Authorization"))
			assert.Equal(t, "m-1", got.body["model"])
			assert.EqualValues(t, 123, got.body["max_tokens"])
			assert.EqualValues(t, 0.4, got.body["temperature"])
			msgs := got.body["messages"].([]any)
			require.Len(t, msgs, 4)
			assert.Equal(t, map[string]any{"role": "system", "content": "be brief"}, msgs[0], "system prompt is the first message")
			assert.Equal(t, map[string]any{"role": "assistant", "content": "hello"}, msgs[2])

			assert.Equal(t, "pong", resp.Text, "text is trimmed")
			assert.Equal(t, "m-1-0613", resp.Model)
			assert.Equal(t, ai.Usage{InputTokens: 11, OutputTokens: 4, TotalTokens: 15}, resp.Usage)
		})
	}
}

func TestOpenAICompat_OmitsTemperatureAndSystemWhenUnset(t *testing.T) {
	srv, got := server(t, 200, openAIReply, nil)
	_, err := mk(t, ai.ProviderOpenAI, srv).Complete(context.Background(),
		ai.Request{Model: "m", MaxTokens: 10, Messages: []ai.Message{{Role: ai.RoleUser, Content: "x"}}})
	require.NoError(t, err)
	assert.NotContains(t, got.body, "temperature", "temperature <= 0 keeps the provider default, as before")
	assert.Len(t, got.body["messages"], 1, "no empty system message")
}

func TestGroq_DefaultsToTheDocumentedBaseURL(t *testing.T) {
	// Without BaseURL the request must go to Groq's OpenAI-compatible endpoint.
	var hit string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hit = r.URL.String()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: http.Header{}}, nil
	})}
	p, err := ai.New(ai.ProviderGroq, ai.Config{APIKey: key, HTTPClient: client})
	require.NoError(t, err)
	_, err = p.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://api.groq.com/openai/v1/models", hit)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAICompat_ListModelsIsDynamicSortedAndChatOnly(t *testing.T) {
	reply := `{"object":"list","data":[
	  {"id":"zeta-chat","owned_by":"acme"},{"id":"whisper-large-v3","owned_by":"x"},
	  {"id":"alpha-chat","owned_by":"acme"},{"id":"text-embedding-3-small","owned_by":"x"},{"id":"","owned_by":"x"}]}`
	srv, got := server(t, 200, reply, nil)
	models, err := mk(t, ai.ProviderGroq, srv).ListModels(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "/models", got.path)
	assert.Equal(t, "Bearer "+key, got.header.Get("Authorization"))
	require.Len(t, models, 2, "speech/embedding/blank entries are dropped")
	assert.Equal(t, "alpha-chat", models[0].ID)
	assert.Equal(t, "zeta-chat", models[1].ID)
}

// --- Anthropic ---

func TestAnthropic_CompleteShape(t *testing.T) {
	reply := `{"model":"claude-x","content":[{"type":"thinking","text":"?"},{"type":"text","text":" ok "}],"usage":{"input_tokens":7,"output_tokens":3}}`
	srv, got := server(t, 200, reply, nil)
	resp, err := mk(t, ai.ProviderAnthropic, srv).Complete(context.Background(), chat)
	require.NoError(t, err)

	assert.Equal(t, "/messages", got.path)
	assert.Equal(t, key, got.header.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", got.header.Get("anthropic-version"))
	assert.Empty(t, got.header.Get("Authorization"))
	assert.Equal(t, "be brief", got.body["system"], "system is a top-level field, not a message")
	assert.Len(t, got.body["messages"], 3)
	assert.EqualValues(t, 123, got.body["max_tokens"])

	assert.Equal(t, "ok", resp.Text)
	assert.Equal(t, ai.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}, resp.Usage)
}

func TestAnthropic_NoTextBlockIsAnEmptyResponseError(t *testing.T) {
	srv, _ := server(t, 200, `{"content":[{"type":"tool_use"}]}`, nil)
	_, err := mk(t, ai.ProviderAnthropic, srv).Complete(context.Background(), chat)
	assert.Equal(t, ai.KindEmptyResponse, ai.KindOf(err))
}

func TestAnthropic_ListModels(t *testing.T) {
	srv, got := server(t, 200, `{"data":[{"id":"claude-b","display_name":"B"},{"id":"claude-a","display_name":"A"}]}`, nil)
	models, err := mk(t, ai.ProviderAnthropic, srv).ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/models", got.path)
	assert.Equal(t, []ai.ModelInfo{{ID: "claude-a", DisplayName: "A"}, {ID: "claude-b", DisplayName: "B"}}, models)
}

// --- Google ---

func TestGoogle_CompleteShapeAndKeyOnlyInHeader(t *testing.T) {
	reply := `{"candidates":[{"content":{"parts":[{"text":" gem "}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7},"modelVersion":"gemini-x-001"}`
	srv, got := server(t, 200, reply, nil)
	req := chat
	req.Model = "gemini-x"
	resp, err := mk(t, ai.ProviderGoogle, srv).Complete(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "/models/gemini-x:generateContent", got.path)
	assert.Empty(t, got.rawQuery, "the key must not be in the URL")
	assert.Equal(t, key, got.header.Get("x-goog-api-key"))
	contents := got.body["contents"].([]any)
	require.Len(t, contents, 3)
	assert.Equal(t, "model", contents[1].(map[string]any)["role"], "assistant maps to Gemini's 'model' role")
	assert.Contains(t, got.body, "systemInstruction")
	assert.EqualValues(t, 123, got.body["generationConfig"].(map[string]any)["maxOutputTokens"])
	assert.EqualValues(t, 0.4, got.body["generationConfig"].(map[string]any)["temperature"])

	assert.Equal(t, "gem", resp.Text)
	assert.Equal(t, ai.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}, resp.Usage)
	assert.Equal(t, "gemini-x-001", resp.Model)
}

func TestGoogle_ListModelsKeepsOnlyGenerateContentAndStripsPrefix(t *testing.T) {
	reply := `{"models":[
	 {"name":"models/gemini-b","displayName":"B","supportedGenerationMethods":["generateContent","countTokens"]},
	 {"name":"models/embedding-001","displayName":"E","supportedGenerationMethods":["embedContent"]},
	 {"name":"models/gemini-a","displayName":"A","supportedGenerationMethods":["generateContent"]}]}`
	srv, _ := server(t, 200, reply, nil)
	models, err := mk(t, ai.ProviderGoogle, srv).ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []ai.ModelInfo{{ID: "gemini-a", DisplayName: "A"}, {ID: "gemini-b", DisplayName: "B"}}, models)
}

// --- Errors (all providers) ---

func TestErrors_AreClassifiedAndNeverLeakTheKey(t *testing.T) {
	cases := []struct {
		name   string
		status int
		kind   ai.ErrorKind
	}{
		{"unauthorized", 401, ai.KindAuth},
		{"forbidden", 403, ai.KindAuth},
		{"rate limited", 429, ai.KindRateLimit},
		{"bad model", 404, ai.KindInvalidRequest},
		{"bad request", 400, ai.KindInvalidRequest},
		{"server error", 503, ai.KindProvider},
	}
	for _, provider := range ai.Supported() {
		for _, c := range cases {
			t.Run(provider+"/"+c.name, func(t *testing.T) {
				// A hostile/buggy provider that echoes the credential back.
				body := `{"error":{"message":"invalid key ` + key + ` for model"}}`
				srv, _ := server(t, c.status, body, map[string]string{"Retry-After": "7"})
				_, err := mk(t, provider, srv).Complete(context.Background(), chat)
				require.Error(t, err)

				var e *ai.Error
				require.ErrorAs(t, err, &e)
				assert.Equal(t, c.kind, e.Kind)
				assert.Equal(t, c.status, e.Status)
				assert.NotContains(t, err.Error(), key, "the key is redacted from provider error text")
				assert.Contains(t, e.Message, "[redacted]")
				assert.Equal(t, 7*time.Second, e.RetryAfter)
			})
		}
	}
}

func TestErrors_TimeoutAndNetwork(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	p, err := ai.New(ai.ProviderOpenAI, ai.Config{APIKey: key, BaseURL: slow.URL})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = p.Complete(ctx, chat)
	assert.Equal(t, ai.KindTimeout, ai.KindOf(err))

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	p, _ = ai.New(ai.ProviderOpenAI, ai.Config{APIKey: key, BaseURL: url})
	_, err = p.Complete(context.Background(), chat)
	assert.Equal(t, ai.KindNetwork, ai.KindOf(err))
	assert.NotContains(t, err.Error(), key)
}

func TestErrors_EmptyAndMalformedSuccessBodies(t *testing.T) {
	srv, _ := server(t, 200, `{"choices":[]}`, nil)
	_, err := mk(t, ai.ProviderOpenAI, srv).Complete(context.Background(), chat)
	assert.Equal(t, ai.KindEmptyResponse, ai.KindOf(err))

	srv, _ = server(t, 200, `not json`, nil)
	_, err = mk(t, ai.ProviderGroq, srv).Complete(context.Background(), chat)
	assert.Equal(t, ai.KindBadResponse, ai.KindOf(err))
}
