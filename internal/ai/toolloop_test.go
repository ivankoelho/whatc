package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scripted is a Provider that plays back canned answers and records what it was asked.
type scripted struct {
	answers  []func(ai.Request) (*ai.Response, error)
	requests []ai.Request
	noTools  bool
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Capabilities() ai.Capabilities {
	return ai.Capabilities{ToolCalling: !s.noTools}
}
func (s *scripted) ListModels(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (s *scripted) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	s.requests = append(s.requests, req)
	i := len(s.requests) - 1
	if i >= len(s.answers) {
		return nil, errors.New("script exhausted")
	}
	return s.answers[i](req)
}

func say(text string) func(ai.Request) (*ai.Response, error) {
	return func(ai.Request) (*ai.Response, error) {
		return &ai.Response{Text: text, Finish: ai.FinishStop, Usage: ai.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}
}

func ask(calls ...ai.ToolCall) func(ai.Request) (*ai.Response, error) {
	return func(ai.Request) (*ai.Response, error) {
		return &ai.Response{ToolCalls: calls, Finish: ai.FinishToolCalls, Usage: ai.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
	}
}

func fail(err error) func(ai.Request) (*ai.Response, error) {
	return func(ai.Request) (*ai.Response, error) { return nil, err }
}

func tc(id, name, args string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

// fakeResolver is the test double for the resolver/executor ports.
type fakeResolver struct {
	tools    map[string]ai.Tool
	resolved []string
}

func (f *fakeResolver) Definitions() []ai.ToolDefinition { return []ai.ToolDefinition{orderTool} }
func (f *fakeResolver) Resolve(name string) (ai.Tool, bool) {
	f.resolved = append(f.resolved, name)
	t, ok := f.tools[name]
	return t, ok
}

type toolFunc func(context.Context, ai.ToolCall) (ai.ToolResult, error)

func (f toolFunc) Execute(ctx context.Context, c ai.ToolCall) (ai.ToolResult, error) {
	return f(ctx, c)
}

func echoTool(log *[]string) ai.Tool {
	return toolFunc(func(_ context.Context, c ai.ToolCall) (ai.ToolResult, error) {
		*log = append(*log, c.ID)
		return ai.ToolResult{Content: "result of " + c.ID}, nil
	})
}

func resolverWith(t ai.Tool) *fakeResolver {
	return &fakeResolver{tools: map[string]ai.Tool{"get_order": t}}
}

var userReq = ai.Request{Model: "m", MaxTokens: 10, Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}}

func TestLoop_NoToolCallIsOneAnswer(t *testing.T) {
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){say("hello")}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(nil), ai.Limits{})
	require.NoError(t, err)
	assert.Equal(t, "hello", res.Response.Text)
	assert.Len(t, p.requests, 1)
	assert.Len(t, p.requests[0].Tools, 1, "the resolver's definitions are offered")
	assert.Empty(t, res.Messages)
}

func TestLoop_RunsToolsAndSendsResultsBack(t *testing.T) {
	var ran []string
	r := resolverWith(echoTool(&ran))
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "get_order", `{"id":"7"}`), tc("c2", "get_order", `{"id":"8"}`)),
		ask(tc("c3", "get_order", `{}`)),
		say("all done"),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, r, ai.Limits{})
	require.NoError(t, err)

	assert.Equal(t, "all done", res.Response.Text)
	assert.Equal(t, []string{"c1", "c2", "c3"}, ran, "serial, in the provider's order")
	assert.Equal(t, 3, res.ToolCalls)
	assert.Len(t, res.Responses, 3)
	assert.Equal(t, 6, res.Usage.TotalTokens)

	// the second request carries the assistant calls and ONE tool message with both results
	second := p.requests[1].Messages
	require.Len(t, second, 3)
	assert.Equal(t, ai.RoleAssistant, second[1].Role)
	assert.Equal(t, ai.RoleTool, second[2].Role)
	require.Len(t, second[2].ToolResults, 2)
	assert.Equal(t, "c1", second[2].ToolResults[0].CallID)
	assert.Equal(t, "get_order", second[2].ToolResults[0].Name)
	assert.Equal(t, "result of c1", second[2].ToolResults[0].Content)
	// the caller's request is not mutated
	assert.Len(t, userReq.Messages, 1)
	assert.Len(t, res.Messages, 4)
}

func TestLoop_NothingRunsWithoutResolve(t *testing.T) {
	// a resolver that refuses: there is no Tool, so nothing can be executed
	r := &fakeResolver{tools: map[string]ai.Tool{}}
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(tc("c1", "get_order", `{}`)), say("ok")}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, r, ai.Limits{})
	require.NoError(t, err)
	assert.Equal(t, []string{"get_order"}, r.resolved, "every call goes through Resolve")
	tr := p.requests[1].Messages[2].ToolResults[0]
	assert.True(t, tr.IsError)
	assert.Equal(t, "error: unknown tool", tr.Content)
	assert.Equal(t, "ok", res.Response.Text)
}

func TestLoop_NilResolverOffersNoTools(t *testing.T) {
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){say("plain")}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, nil, ai.Limits{})
	require.NoError(t, err)
	assert.Equal(t, "plain", res.Response.Text)
	assert.Empty(t, p.requests[0].Tools)
}

func TestLoop_ProviderWithoutToolCallingIsAPlainComplete(t *testing.T) {
	p := &scripted{noTools: true, answers: []func(ai.Request) (*ai.Response, error){say("plain")}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(nil), ai.Limits{})
	require.NoError(t, err)
	assert.Empty(t, p.requests[0].Tools)
}

func TestLoop_ToolFailuresGoBackToTheModel(t *testing.T) {
	boom := toolFunc(func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		return ai.ToolResult{}, errors.New("db password=hunter2 refused")
	})
	pan := toolFunc(func(context.Context, ai.ToolCall) (ai.ToolResult, error) { panic("kaboom") })
	big := toolFunc(func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		return ai.ToolResult{Content: strings.Repeat("é", 100)}, nil
	})
	liar := toolFunc(func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		return ai.ToolResult{CallID: "other", Name: "other", Content: "x"}, nil
	})
	r := &fakeResolver{tools: map[string]ai.Tool{"boom": boom, "pan": pan, "big": big, "get_order": liar}}
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("a", "boom", `{}`), tc("b", "pan", `{}`), tc("c", "big", `{}`), tc("d", "get_order", `{}`)),
		say("recovered"),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, r, ai.Limits{MaxResultBytes: 40})
	require.NoError(t, err)
	assert.Equal(t, "recovered", res.Response.Text)

	rs := p.requests[1].Messages[2].ToolResults
	require.Len(t, rs, 4)
	assert.Equal(t, ai.ToolResult{CallID: "a", Name: "boom", Content: "error: tool failed", IsError: true}, rs[0], "the Go error text never leaves")
	assert.Equal(t, "error: tool failed", rs[1].Content, "a panic is a failed tool, not a crash")
	assert.LessOrEqual(t, len(rs[2].Content), 40)
	assert.True(t, strings.HasSuffix(rs[2].Content, "[truncated]"))
	assert.True(t, utf8Valid(rs[2].Content), "never cut inside a character")
	assert.Equal(t, "d", rs[3].CallID, "a tool cannot answer for another call")
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestLoop_OversizedArgumentsAreNotExecuted(t *testing.T) {
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "get_order", `{"id":"`+strings.Repeat("x", 100)+`"}`)), say("ok"),
	}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxArgsBytes: 50})
	require.NoError(t, err)
	assert.Empty(t, ran)
	tr := p.requests[1].Messages[2].ToolResults[0]
	assert.True(t, tr.IsError)
	assert.Equal(t, "error: arguments too large", tr.Content)
}

func TestLoop_MalformedToolCallAbortsWithoutRetry(t *testing.T) {
	bad := &ai.Error{Provider: "p", Kind: ai.KindInvalidToolCall, Message: "bad call"}
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){fail(bad), say("never")}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(nil), ai.Limits{})
	assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err))
	assert.Len(t, p.requests, 1, "one provider call: no retry")
	assert.NotNil(t, res)
}

func TestLoop_ProviderErrorIsReturnedAsIs(t *testing.T) {
	boom := &ai.Error{Provider: "p", Kind: ai.KindProvider, Status: 503}
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(tc("c1", "get_order", `{}`)), fail(boom), say("never")}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{})
	assert.Equal(t, boom, err)
	assert.Len(t, p.requests, 2)
	assert.Equal(t, 1, res.ToolCalls, "what had already run is reported")
}

func TestLoop_StepLimitEndsWithAToollessAnswer(t *testing.T) {
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "get_order", `{}`)), ask(tc("c2", "get_order", `{}`)), say("wrapping up"),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxSteps: 2})
	require.NoError(t, err)
	assert.Equal(t, "wrapping up", res.Response.Text)
	require.Len(t, p.requests, 3)
	assert.Nil(t, p.requests[2].Tools, "the last round declares no tools")
	assert.Empty(t, p.requests[2].ToolChoice, "and does not depend on tool_choice none")
	assert.NotEqual(t, ai.ToolChoiceNone, p.requests[1].ToolChoice)
	assert.Len(t, p.requests[1].Tools, 1)
}

// Defense in depth: the real adapters drop tool calls on a request without Tools, but a fake (or a
// future adapter that does not) must still not get a tool run out of the last round.
func TestLoop_StillAskingAfterTheLastChanceFails(t *testing.T) {
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "get_order", `{}`)), ask(tc("c2", "get_order", `{}`)),
	}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxSteps: 1})
	assert.ErrorIs(t, err, ai.ErrToolLoopLimit)
	assert.Equal(t, []string{"c1"}, ran, "only the allowed step ran")
}

func TestLoop_CallLimitsStopTheWholeRound(t *testing.T) {
	var ran []string
	three := ask(tc("a", "get_order", `{}`), tc("b", "get_order", `{}`), tc("c", "get_order", `{}`))
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){three}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxCallsStep: 2})
	assert.ErrorIs(t, err, ai.ErrToolLoopLimit)
	assert.Empty(t, ran, "a round over the limit runs nothing, not even part of it")

	ran = nil
	p = &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("a", "get_order", `{}`), tc("b", "get_order", `{}`)), ask(tc("c", "get_order", `{}`), tc("d", "get_order", `{}`)),
	}}
	_, err = ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxToolCalls: 3, MaxCallsStep: 2})
	assert.ErrorIs(t, err, ai.ErrToolLoopLimit)
	assert.Equal(t, []string{"a", "b"}, ran)
}

func TestLoop_TimeoutAndCancellation(t *testing.T) {
	// the total clock runs out while a tool is busy
	slow := toolFunc(func(ctx context.Context, c ai.ToolCall) (ai.ToolResult, error) {
		<-ctx.Done()
		return ai.ToolResult{}, ctx.Err()
	})
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(tc("c1", "get_order", `{}`), tc("c2", "get_order", `{}`)), say("never")}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(slow), ai.Limits{Timeout: 30 * time.Millisecond})
	assert.Equal(t, ai.KindTimeout, ai.KindOf(err))
	assert.Len(t, p.requests, 1, "no provider call after the clock ran out")

	// the caller cancels
	ctx, cancel := context.WithCancel(t.Context())
	cancelTool := toolFunc(func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		cancel()
		return ai.ToolResult{Content: "x"}, nil
	})
	p = &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(tc("c1", "get_order", `{}`), tc("c2", "get_order", `{}`)), say("never")}}
	_, err = ai.RunToolLoop(ctx, p, userReq, resolverWith(cancelTool), ai.Limits{})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, p.requests, 1)
}
