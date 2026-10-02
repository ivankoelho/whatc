package ai_test

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const googleTextReply = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`

func TestGoogle_ToolRequestWireFormat(t *testing.T) {
	srv, got := server(t, 200, googleTextReply, nil)
	req := toolRound()
	req.ToolChoice = ai.ToolChoiceNone
	// the provider sent an id for call_1 and a thought signature; call_2 has neither
	req.Messages[1].ToolCalls[0].Opaque = json.RawMessage(`{"sig":"SIG-1"}`)
	req.Messages[1].ToolCalls[1].Opaque = json.RawMessage(`{"synth":true}`)
	// results arrive in the opposite order of the calls
	r := req.Messages[2].ToolResults
	r[0], r[1] = r[1], r[0]

	_, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), req)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}, got.body["toolConfig"])
	decl := got.body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	assert.Equal(t, "get_order", decl["name"])
	assert.Equal(t, "object", decl["parameters"].(map[string]any)["type"])

	contents := got.body["contents"].([]any)
	require.Len(t, contents, 3)
	model := contents[1].(map[string]any)
	assert.Equal(t, "model", model["role"])
	p0 := model["parts"].([]any)[0].(map[string]any)
	assert.Equal(t, "SIG-1", p0["thoughtSignature"], "the signature goes back on the same part")
	fc0 := p0["functionCall"].(map[string]any)
	assert.Equal(t, "call_1", fc0["id"], "a provider id is sent back")
	assert.Equal(t, map[string]any{"id": "7"}, fc0["args"], "args is an object")
	p1 := model["parts"].([]any)[1].(map[string]any)
	assert.NotContains(t, p1, "thoughtSignature")
	assert.NotContains(t, p1["functionCall"], "id", "an id made up locally is never sent")

	res := contents[2].(map[string]any)
	assert.Equal(t, "user", res["role"])
	rp := res["parts"].([]any)
	require.Len(t, rp, 2)
	fr0 := rp[0].(map[string]any)["functionResponse"].(map[string]any)
	fr1 := rp[1].(map[string]any)["functionResponse"].(map[string]any)
	assert.Equal(t, "call_1", fr0["id"], "results follow the order of the calls")
	assert.Equal(t, map[string]any{"result": "shipped"}, fr0["response"])
	assert.Equal(t, "get_order", fr1["name"])
	assert.NotContains(t, fr1, "id")
	assert.Equal(t, map[string]any{"error": "error: not found"}, fr1["response"])
}

func TestGoogle_ParsesCallsPreservingIDsAndSignatures(t *testing.T) {
	srv, _ := server(t, 200, `{"modelVersion":"g","candidates":[{"finishReason":"STOP","content":{"parts":[
		{"text":"checking"},
		{"functionCall":{"id":"fc-9","name":"get_order","args":{"id":"7"}},"thoughtSignature":"SIG-A"},
		{"functionCall":{"name":"get_order","args":{"id":"8"}}}]}}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`, nil)
	resp, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)

	assert.Equal(t, ai.FinishToolCalls, resp.Finish, "STOP with function calls means tool calls")
	assert.Equal(t, "checking", resp.Text)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "fc-9", resp.ToolCalls[0].ID, "an id the provider sent is preserved")
	assert.JSONEq(t, `{"sig":"SIG-A"}`, string(resp.ToolCalls[0].Opaque))
	assert.Equal(t, "call_1", resp.ToolCalls[1].ID, "no id: deterministic local id, by position")
	assert.JSONEq(t, `{"synth":true}`, string(resp.ToolCalls[1].Opaque))
	assert.Equal(t, 7, resp.Usage.TotalTokens)
}

// the whole loop at the adapter level: what Google returned is what Whatc sends back
func TestGoogle_ReplayRoundTrip(t *testing.T) {
	reply := `{"candidates":[{"finishReason":"STOP","content":{"parts":[
		{"functionCall":{"id":"fc-9","name":"get_order","args":{"id":"7"}},"thoughtSignature":"SIG-A"},
		{"functionCall":{"name":"get_order","args":{"id":"8"}}}]}}]}`
	srv, _ := server(t, 200, reply, nil)
	first, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), ai.Request{
		Model: "m", MaxTokens: 10, Tools: []ai.ToolDefinition{orderTool},
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "go"}},
	})
	require.NoError(t, err)

	srv2, got := server(t, 200, googleTextReply, nil)
	_, err = mk(t, ai.ProviderGoogle, srv2).Complete(t.Context(), ai.Request{
		Model: "m", MaxTokens: 10, Tools: []ai.ToolDefinition{orderTool},
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "go"},
			{Role: ai.RoleAssistant, ToolCalls: first.ToolCalls},
			{Role: ai.RoleTool, ToolResults: []ai.ToolResult{
				{CallID: first.ToolCalls[0].ID, Name: "get_order", Content: "a"},
				{CallID: first.ToolCalls[1].ID, Name: "get_order", Content: "b"},
			}},
		},
	})
	require.NoError(t, err)
	parts := got.body["contents"].([]any)[1].(map[string]any)["parts"].([]any)
	assert.Equal(t, "SIG-A", parts[0].(map[string]any)["thoughtSignature"])
	assert.Equal(t, "fc-9", parts[0].(map[string]any)["functionCall"].(map[string]any)["id"])
	assert.NotContains(t, parts[1].(map[string]any)["functionCall"], "id")
}

func TestGoogle_TextAnswerAndEmptyAnswer(t *testing.T) {
	srv, _ := server(t, 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hmm","thought":true},{"text":"final"}]}}]}`, nil)
	resp, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)
	assert.Equal(t, "final", resp.Text, "thought parts are not the answer")
	assert.Equal(t, ai.FinishStop, resp.Finish)
	assert.Empty(t, resp.ToolCalls)

	srv, _ = server(t, 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[]}}]}`, nil)
	_, err = mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), toolRound())
	assert.Equal(t, ai.KindEmptyResponse, ai.KindOf(err))
}

func TestGoogle_MalformedToolCallsAreInvalidToolCall(t *testing.T) {
	cases := map[string]string{
		"malformed finish":   `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
		"unexpected call":    `{"candidates":[{"finishReason":"UNEXPECTED_TOOL_CALL"}]}`,
		"args not an object": `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"n","args":[1]}}]}}]}`,
		"no name":            `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"args":{}}}]}}]}`,
		"cut off":            `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"functionCall":{"name":"n","args":{}}}]}}]}`,
	}
	for name, body := range cases {
		srv, _ := server(t, 200, body, nil)
		_, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), toolRound())
		assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err), name)
	}
}

func TestGoogle_UnsupportedSchemaKeywordIsRejectedBeforeSending(t *testing.T) {
	srv, got := server(t, 200, googleTextReply, nil)
	req := toolRound()
	req.Tools[0].Parameters = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","default":"x"}},"additionalProperties":false}`)
	_, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), req)
	assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err))
	assert.Contains(t, err.Error(), "additionalProperties")
	assert.Nil(t, got.body)

	// property NAMES are free, even when they look like keywords
	req.Tools[0].Parameters = json.RawMessage(`{"type":"object","properties":{"default":{"type":"string","enum":["a"]},"list":{"type":"array","items":{"type":"integer"}}}}`)
	_, err = mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), req)
	assert.NoError(t, err)
}

func TestGoogle_ConversationIsValidatedBeforeAnyRequest(t *testing.T) {
	srv, got := server(t, 200, googleTextReply, nil)
	req := toolRound()
	req.Messages = req.Messages[:2]
	_, err := mk(t, ai.ProviderGoogle, srv).Complete(t.Context(), req)
	assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err))
	assert.Nil(t, got.body)
}
