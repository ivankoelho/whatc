package ai_test

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var orderTool = ai.ToolDefinition{
	Name: "get_order", Description: "Look up an order",
	Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
}

// a conversation in the middle of a tool round: user -> assistant(calls) -> results
func toolRound() ai.Request {
	return ai.Request{
		Model: "m-1", MaxTokens: 50, Tools: []ai.ToolDefinition{orderTool},
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "where is order 7?"},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{
				{ID: "call_1", Name: "get_order", Arguments: json.RawMessage(`{"id":"7"}`)},
				{ID: "call_2", Name: "get_order", Arguments: json.RawMessage(`{"id":"8"}`)},
			}},
			{Role: ai.RoleTool, ToolResults: []ai.ToolResult{
				{CallID: "call_1", Name: "get_order", Content: "shipped"},
				{CallID: "call_2", Name: "get_order", Content: "error: not found", IsError: true},
			}},
		},
	}
}

func TestOpenAICompat_ToolRequestWireFormat(t *testing.T) {
	for _, name := range []string{ai.ProviderOpenAI, ai.ProviderGroq} {
		t.Run(name, func(t *testing.T) {
			srv, got := server(t, 200, openAIReply, nil)
			req := toolRound()
			req.ToolChoice = ai.ToolChoiceNone
			_, err := mk(t, name, srv).Complete(t.Context(), req)
			require.NoError(t, err)

			assert.Equal(t, "none", got.body["tool_choice"])
			tools := got.body["tools"].([]any)
			require.Len(t, tools, 1)
			fn := tools[0].(map[string]any)["function"].(map[string]any)
			assert.Equal(t, "function", tools[0].(map[string]any)["type"])
			assert.Equal(t, "get_order", fn["name"])
			assert.Equal(t, "object", fn["parameters"].(map[string]any)["type"])

			msgs := got.body["messages"].([]any)
			require.Len(t, msgs, 4) // user, assistant(2 calls), 2 tool messages
			asst := msgs[1].(map[string]any)
			assert.Equal(t, "assistant", asst["role"])
			assert.NotContains(t, asst, "content")
			assert.NotContains(t, asst, "name", "messages[].name is never sent (Groq rejects it)")
			calls := asst["tool_calls"].([]any)
			require.Len(t, calls, 2)
			c0 := calls[0].(map[string]any)
			assert.Equal(t, "call_1", c0["id"])
			assert.Equal(t, `{"id":"7"}`, c0["function"].(map[string]any)["arguments"], "arguments travel as a JSON string")

			for i, id := range []string{"call_1", "call_2"} {
				tm := msgs[2+i].(map[string]any)
				assert.Equal(t, "tool", tm["role"])
				assert.Equal(t, id, tm["tool_call_id"])
				if name == ai.ProviderGroq {
					assert.Equal(t, "get_order", tm["name"], "Groq documents name on the tool result")
				} else {
					assert.NotContains(t, tm, "name")
				}
			}
			assert.NotContains(t, msgs[0].(map[string]any), "name")
		})
	}
}

func TestOpenAICompat_ToolChoiceAutoIsTheDefault(t *testing.T) {
	srv, got := server(t, 200, openAIReply, nil)
	_, err := mk(t, ai.ProviderOpenAI, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)
	assert.NotContains(t, got.body, "tool_choice")
}

const toolsReply = `{"model":"m","choices":[{"message":{"content":"let me check","tool_calls":[
 {"id":"call_a","type":"function","function":{"name":"get_order","arguments":"{\"id\":\"7\"}"}},
 {"id":"call_b","type":"function","function":{"name":"get_order","arguments":""}}]},"finish_reason":"tool_calls"}],
 "usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`

func TestOpenAICompat_ParsesToolCalls(t *testing.T) {
	for _, name := range []string{ai.ProviderOpenAI, ai.ProviderGroq} {
		srv, _ := server(t, 200, toolsReply, nil)
		resp, err := mk(t, name, srv).Complete(t.Context(), toolRound())
		require.NoError(t, err, name)
		assert.Equal(t, ai.FinishToolCalls, resp.Finish)
		assert.Equal(t, "let me check", resp.Text)
		require.Len(t, resp.ToolCalls, 2)
		assert.Equal(t, "call_a", resp.ToolCalls[0].ID)
		assert.JSONEq(t, `{"id":"7"}`, string(resp.ToolCalls[0].Arguments))
		assert.JSONEq(t, `{}`, string(resp.ToolCalls[1].Arguments), "empty arguments mean no arguments")
		assert.Equal(t, 11, resp.Usage.TotalTokens)
	}
}

func TestOpenAICompat_TextAnswerHasNoToolCalls(t *testing.T) {
	srv, _ := server(t, 200, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`, nil)
	resp, err := mk(t, ai.ProviderOpenAI, srv).Complete(t.Context(), toolRound())
	require.NoError(t, err)
	assert.Equal(t, ai.FinishStop, resp.Finish)
	assert.Empty(t, resp.ToolCalls)
}

func TestOpenAICompat_MalformedToolCallsAreInvalidToolCall(t *testing.T) {
	cases := map[string]string{
		"not json":      `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"get_order","arguments":"{oops"}}]},"finish_reason":"tool_calls"}]}`,
		"not an object": `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"get_order","arguments":"[1]"}}]},"finish_reason":"tool_calls"}]}`,
		"no id":         `{"choices":[{"message":{"tool_calls":[{"function":{"name":"get_order","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		"no name":       `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		"cut off":       `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"get_order","arguments":"{}"}}]},"finish_reason":"length"}]}`,
	}
	for name, body := range cases {
		srv, _ := server(t, 200, body, nil)
		_, err := mk(t, ai.ProviderOpenAI, srv).Complete(t.Context(), toolRound())
		assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err), name)
	}
}

func TestGroq_FailedGenerationIsInvalidToolCall(t *testing.T) {
	bodies := map[string]string{
		"object": `{"error":{"message":"Failed to call a function.","type":"invalid_request_error","failed_generation":{"reason":"bad","tool_call_id":"c","attempted_arguments":"{x"}}}`,
		"string": `{"error":{"message":"Failed to call a function.","code":"tool_use_failed","failed_generation":"<function=get_order{x}</function>"}}`,
	}
	for name, body := range bodies {
		srv, _ := server(t, 400, body, nil)
		_, err := mk(t, ai.ProviderGroq, srv).Complete(t.Context(), toolRound())
		assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err), name)
	}
	// without tools, the same 400 stays a plain invalid request
	srv, _ := server(t, 400, bodies["string"], nil)
	_, err := mk(t, ai.ProviderGroq, srv).Complete(t.Context(), chat)
	assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err))
}

func TestToolConversation_IsValidatedBeforeAnyRequest(t *testing.T) {
	srv, got := server(t, 200, openAIReply, nil)
	p := mk(t, ai.ProviderOpenAI, srv)

	broken := map[string]func(*ai.Request){
		"calls without results":   func(r *ai.Request) { r.Messages = r.Messages[:2] },
		"result for unknown call": func(r *ai.Request) { r.Messages[2].ToolResults[0].CallID = "nope" },
		"missing a result":        func(r *ai.Request) { r.Messages[2].ToolResults = r.Messages[2].ToolResults[:1] },
		"results on user msg":     func(r *ai.Request) { r.Messages[2].Role = ai.RoleUser },
		"calls on user msg":       func(r *ai.Request) { r.Messages[0].ToolCalls = r.Messages[1].ToolCalls },
		"call without id":         func(r *ai.Request) { r.Messages[1].ToolCalls[0].ID = "" },
		"bad tool name":           func(r *ai.Request) { r.Tools[0].Name = "has space" },
		"duplicate tool names":    func(r *ai.Request) { r.Tools = append(r.Tools, r.Tools[0]) },
	}
	for name, mut := range broken {
		req := toolRound()
		mut(&req)
		_, err := p.Complete(t.Context(), req)
		assert.Equal(t, ai.KindInvalidRequest, ai.KindOf(err), name)
	}
	assert.Nil(t, got.body, "nothing is sent for an invalid conversation")
}
