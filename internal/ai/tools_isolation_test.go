package ai_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A thoughtSignature belongs to the functionCall part that carried it. These tests pin that
// structurally; whether the real Gemini API agrees is NOT confirmed (see googleOpaque).
func TestGoogle_SignatureStaysWithItsOwnCall(t *testing.T) {
	reply := `{"candidates":[{"finishReason":"STOP","content":{"parts":[
		{"text":"thinking out loud","thoughtSignature":"SIG-TEXT"},
		{"functionCall":{"id":"fa","name":"get_order","args":{"id":"1"}}},
		{"functionCall":{"name":"get_order","args":{"id":"2"}},"thoughtSignature":"SIG-B"},
		{"functionCall":{"id":"fc","name":"get_order","args":{"id":"3"}},"thoughtSignature":"SIG-C"}]}}]}`
	srv, _ := server(t, 200, reply, nil)
	first, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), ai.Request{
		Model: "m", MaxTokens: 10, Tools: []ai.ToolDefinition{orderTool},
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "go"}},
	})
	require.NoError(t, err)
	calls := first.ToolCalls
	require.Len(t, calls, 3)

	assert.Equal(t, []string{"fa", "call_1", "fc"}, []string{calls[0].ID, calls[1].ID, calls[2].ID}, "order of the parts is the order of the calls")
	assert.Nil(t, calls[0].Opaque, "a call without a signature has none")
	assert.JSONEq(t, `{"synth":true,"sig":"SIG-B"}`, string(calls[1].Opaque), "a signature only on the second call stays on the second call")
	assert.JSONEq(t, `{"sig":"SIG-C"}`, string(calls[2].Opaque))
	for _, c := range calls {
		assert.NotContains(t, string(c.Opaque), "SIG-TEXT", "a signature on a text part is not attached to any call")
	}

	// replay, with the results handed over in the opposite order
	srv2, got := server(t, 200, googleTextReply, nil)
	_, err = mk(t, ai.ProviderGoogle, srv2).Complete(t.Context(), ai.Request{
		Model: "m", MaxTokens: 10, Tools: []ai.ToolDefinition{orderTool},
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "go"},
			{Role: ai.RoleAssistant, ToolCalls: calls},
			{Role: ai.RoleTool, ToolResults: []ai.ToolResult{
				{CallID: "fc", Name: "get_order", Content: "c"},
				{CallID: "call_1", Name: "get_order", Content: "b"},
				{CallID: "fa", Name: "get_order", Content: "a"},
			}},
		},
	})
	require.NoError(t, err)

	contents := got.body["contents"].([]any)
	parts := contents[1].(map[string]any)["parts"].([]any)
	require.Len(t, parts, 3)
	wantSig := []any{nil, "SIG-B", "SIG-C"}
	wantID := []any{"fa", nil, "fc"}
	wantArg := []string{"1", "2", "3"}
	for i, p := range parts {
		pm := p.(map[string]any)
		fc := pm["functionCall"].(map[string]any)
		assert.Equal(t, wantSig[i], pm["thoughtSignature"], "part %d", i)
		assert.Equal(t, wantID[i], fc["id"], "part %d", i)
		assert.Equal(t, wantArg[i], fc["args"].(map[string]any)["id"], "part %d", i)
	}
	assert.NotContains(t, mustJSON(t, got.body), "SIG-TEXT")

	resp := contents[2].(map[string]any)["parts"].([]any)
	require.Len(t, resp, 3)
	for i, want := range []string{"a", "b", "c"} {
		r := resp[i].(map[string]any)["functionResponse"].(map[string]any)["response"].(map[string]any)
		assert.Equal(t, want, r["result"], "results follow the order of the calls, not of the results")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// Opaque is the adapter's transport state: the loop replays it internally but nothing it returns
// (Response, Responses, Messages) carries it, since a LoopResult may be persisted or audited.
func TestLoop_NeverExposesOpaque(t *testing.T) {
	withState := func(id string) ai.ToolCall {
		c := tc(id, "get_order", `{}`)
		c.Opaque = json.RawMessage(`{"sig":"SECRET-` + id + `"}`)
		return c
	}
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(withState("c1"), withState("c2")), ask(withState("c3")), say("done"),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{})
	require.NoError(t, err)

	// internal replay keeps it: the provider receives the state it produced
	replayed := p.requests[1].Messages[1].ToolCalls
	require.Len(t, replayed, 2)
	assert.JSONEq(t, `{"sig":"SECRET-c1"}`, string(replayed[0].Opaque))
	assert.JSONEq(t, `{"sig":"SECRET-c2"}`, string(replayed[1].Opaque))
	assert.JSONEq(t, `{"sig":"SECRET-c3"}`, string(p.requests[2].Messages[3].ToolCalls[0].Opaque))

	// nothing that leaves the loop has it
	require.Len(t, res.Responses, 3)
	require.Len(t, res.Messages, 4)
	assert.Len(t, res.Responses[0].ToolCalls, 2, "the calls themselves are still reported")
	assert.Equal(t, "c1", res.Responses[0].ToolCalls[0].ID)
	for i, r := range res.Responses {
		for _, c := range r.ToolCalls {
			assert.Nil(t, c.Opaque, "Responses[%d]", i)
		}
	}
	for i, m := range res.Messages {
		for _, c := range m.ToolCalls {
			assert.Nil(t, c.Opaque, "Messages[%d]", i)
		}
	}
	assert.NotContains(t, mustJSON(t, res), "SECRET")
}

func TestLoop_PartialResultOnErrorNeverExposesOpaque(t *testing.T) {
	c := tc("c1", "get_order", `{}`)
	c.Opaque = json.RawMessage(`{"sig":"SECRET"}`)
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(c), fail(&ai.Error{Provider: "p", Kind: ai.KindProvider}),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{})
	require.Error(t, err)
	require.NotNil(t, res)
	assert.NotContains(t, mustJSON(t, res), "SECRET")
	assert.Equal(t, "c1", res.Messages[0].ToolCalls[0].ID)
}

// The Gemini schema subset is a restriction of the Gemini adapter only: the neutral definition
// and the other adapters take any JSON Schema object and pass it through untouched.
func TestSchemaRestrictionIsGeminiOnly(t *testing.T) {
	schema := `{"type":"object","additionalProperties":false,
		"properties":{"id":{"type":"string","default":"x"},"ref":{"$ref":"#/$defs/r"},"either":{"oneOf":[{"type":"string"},{"type":"integer"}]}},
		"$defs":{"r":{"type":"string"}}}`
	def := ai.ToolDefinition{Name: "get_order", Parameters: json.RawMessage(schema)}
	require.NoError(t, def.Validate(), "the neutral contract accepts it")

	reply := map[string]string{
		ai.ProviderOpenAI:    openAIReply,
		ai.ProviderGroq:      openAIReply,
		ai.ProviderAnthropic: `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`,
		ai.ProviderGoogle:    googleTextReply,
	}
	for name, body := range reply {
		srv, got := server(t, 200, body, nil)
		req := toolRound()
		req.Tools = []ai.ToolDefinition{def}
		_, err := mk(t, name, srv).Complete(t.Context(), req)
		if name == ai.ProviderGoogle {
			assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err), "Gemini adapter rejects it")
			assert.Nil(t, got.body)
			continue
		}
		require.NoError(t, err, name)
		sent := mustJSON(t, got.body["tools"])
		for _, kw := range []string{"additionalProperties", `"default"`, "$ref", "oneOf", "$defs"} {
			assert.True(t, strings.Contains(sent, kw), "%s sends %s untouched", name, kw)
		}
	}
}
