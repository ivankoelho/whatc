package ai_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropic_ToolRequestWireFormat(t *testing.T) {
	srv, got := server(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, nil)
	req := toolRound()
	req.ToolChoice = ai.ToolChoiceNone
	_, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), req)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"type": "none"}, got.body["tool_choice"])
	tool := got.body["tools"].([]any)[0].(map[string]any)
	assert.Equal(t, "get_order", tool["name"])
	assert.Equal(t, "object", tool["input_schema"].(map[string]any)["type"])

	msgs := got.body["messages"].([]any)
	require.Len(t, msgs, 3, "all results of the round go in one user message")
	asst := msgs[1].(map[string]any)
	assert.Equal(t, "assistant", asst["role"])
	blocks := asst["content"].([]any)
	require.Len(t, blocks, 2) // no text, two tool_use
	b0 := blocks[0].(map[string]any)
	assert.Equal(t, "tool_use", b0["type"])
	assert.Equal(t, "call_1", b0["id"])
	assert.Equal(t, map[string]any{"id": "7"}, b0["input"], "input is an object, not a string")

	res := msgs[2].(map[string]any)
	assert.Equal(t, "user", res["role"])
	rb := res["content"].([]any)
	require.Len(t, rb, 2)
	r0, r1 := rb[0].(map[string]any), rb[1].(map[string]any)
	assert.Equal(t, "tool_result", r0["type"])
	assert.Equal(t, "call_1", r0["tool_use_id"])
	assert.NotContains(t, r0, "is_error")
	assert.Equal(t, true, r1["is_error"])
}

func TestAnthropic_AssistantTextIsKeptBesideToolUse(t *testing.T) {
	srv, got := server(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`, nil)
	req := toolRound()
	req.Messages[1].Content = "checking"
	_, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), req)
	require.NoError(t, err)
	blocks := got.body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	require.Len(t, blocks, 3)
	assert.Equal(t, "text", blocks[0].(map[string]any)["type"])
}

func TestAnthropic_ParsesToolUse(t *testing.T) {
	srv, _ := server(t, 200, `{"model":"m","stop_reason":"tool_use","content":[
		{"type":"text","text":" let me look "},
		{"type":"tool_use","id":"toolu_1","name":"get_order","input":{"id":"7"}},
		{"type":"tool_use","id":"toolu_2","name":"get_order","input":{}}],
		"usage":{"input_tokens":4,"output_tokens":9}}`, nil)
	resp, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)
	assert.Equal(t, ai.FinishToolCalls, resp.Finish)
	assert.Equal(t, "let me look", resp.Text)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "toolu_1", resp.ToolCalls[0].ID)
	assert.JSONEq(t, `{"id":"7"}`, string(resp.ToolCalls[0].Arguments))
	assert.Equal(t, 13, resp.Usage.TotalTokens)
}

func TestAnthropic_ToolUseWithoutTextIsNotEmpty(t *testing.T) {
	srv, _ := server(t, 200, `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"get_order"}]}`, nil)
	resp, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(resp.ToolCalls[0].Arguments), "a missing input means no arguments")
	assert.Equal(t, "", resp.Text)
}

func TestAnthropic_PlainReplyStillNeedsText(t *testing.T) {
	srv, _ := server(t, 200, `{"stop_reason":"end_turn","content":[]}`, nil)
	_, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), chat)
	assert.Equal(t, ai.KindEmptyResponse, ai.KindOf(err))
}

func TestAnthropic_MalformedToolUseIsInvalidToolCall(t *testing.T) {
	cases := map[string]string{
		"input not an object": `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"n","input":[1]}]}`,
		"no id":               `{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"n","input":{}}]}`,
		"no name":             `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","input":{}}]}`,
		"cut by max_tokens":   `{"stop_reason":"max_tokens","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]}`,
	}
	for name, body := range cases {
		srv, _ := server(t, 200, body, nil)
		_, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), toolRound())
		assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err), name)
	}
}

func TestAnthropic_ConversationIsValidatedBeforeAnyRequest(t *testing.T) {
	srv, got := server(t, 200, `{"content":[{"type":"text","text":"x"}]}`, nil)
	req := toolRound()
	req.Messages = req.Messages[:2]
	_, err := mk(t, ai.ProviderAnthropic, srv).Complete(t.Context(), req)
	assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err))
	assert.Nil(t, got.body)
}
