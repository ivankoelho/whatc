package ai_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawBody sends req through the provider and returns the exact bytes it put on the wire.
func rawBody(t *testing.T, name string, req ai.Request) string {
	t.Helper()
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	p, err := ai.New(name, ai.Config{APIKey: "k", BaseURL: srv.URL})
	require.NoError(t, err)
	_, _ = p.Complete(t.Context(), req)
	return raw
}

// Golden bodies captured from the adapters BEFORE tool calling existed. A request with no
// Tools must stay byte-for-byte identical on every provider.
var goldenNoTools = map[string]string{
	"openai":    `{"max_tokens":123,"messages":[{"content":"be brief","role":"system"},{"content":"hi","role":"user"},{"content":"hello","role":"assistant"},{"content":"again","role":"user"}],"model":"m-1","temperature":0.4}`,
	"groq":      `{"max_tokens":123,"messages":[{"content":"be brief","role":"system"},{"content":"hi","role":"user"},{"content":"hello","role":"assistant"},{"content":"again","role":"user"}],"model":"m-1","temperature":0.4}`,
	"anthropic": `{"max_tokens":123,"messages":[{"content":"hi","role":"user"},{"content":"hello","role":"assistant"},{"content":"again","role":"user"}],"model":"m-1","system":"be brief","temperature":0.4}`,
	"google":    `{"contents":[{"parts":[{"text":"hi"}],"role":"user"},{"parts":[{"text":"hello"}],"role":"model"},{"parts":[{"text":"again"}],"role":"user"}],"generationConfig":{"maxOutputTokens":123,"temperature":0.4},"systemInstruction":{"parts":[{"text":"be brief"}]}}`,
}

func TestNoTools_PayloadIsByteIdenticalToBeforeToolCalling(t *testing.T) {
	for name, want := range goldenNoTools {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, rawBody(t, name, chat))
			// empty (non-nil) Tools and an explicit ToolChoice with no tools change nothing either
			r := chat
			r.Tools, r.ToolChoice = []ai.ToolDefinition{}, ai.ToolChoiceAuto
			assert.Equal(t, want, rawBody(t, name, r))
		})
	}
}

func TestToolDefinition_Validate(t *testing.T) {
	ok := ai.ToolDefinition{Name: "get_order-1", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
	assert.NoError(t, ok.Validate())
	for _, bad := range []ai.ToolDefinition{
		{Name: "", Parameters: ok.Parameters},
		{Name: "has space", Parameters: ok.Parameters},
		{Name: string(make([]byte, 65)), Parameters: ok.Parameters},
		{Name: "n", Parameters: nil},
		{Name: "n", Parameters: json.RawMessage(`{"type":"string"}`)},
		{Name: "n", Parameters: json.RawMessage(`not json`)},
	} {
		assert.ErrorIs(t, bad.Validate(), ai.ErrInvalidTool, "%+v", bad)
	}
}
