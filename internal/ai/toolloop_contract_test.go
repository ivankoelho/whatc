package ai_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tool never sees the adapter's transport state, but the provider still gets it back.
func TestLoop_ToolsNeverSeeOpaque(t *testing.T) {
	c := tc("c1", "get_order", `{"id":"7"}`)
	c.Opaque = json.RawMessage(`{"sig":"SECRET"}`)

	var seen ai.ToolCall
	spy := toolFunc(func(_ context.Context, call ai.ToolCall) (ai.ToolResult, error) {
		seen = call
		return ai.ToolResult{Content: "ok"}, nil
	})
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(c), say("done")}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(spy), ai.Limits{})
	require.NoError(t, err)

	assert.Equal(t, "c1", seen.ID)
	assert.JSONEq(t, `{"id":"7"}`, string(seen.Arguments))
	assert.Nil(t, seen.Opaque, "the tool gets the call without Opaque")
	replayed := p.requests[1].Messages[1].ToolCalls[0]
	assert.JSONEq(t, `{"sig":"SECRET"}`, string(replayed.Opaque), "the provider still gets it back on replay")
}

func TestLoop_ReportsTheDurationOfEachProviderCall(t *testing.T) {
	slow := func(d time.Duration, next func(ai.Request) (*ai.Response, error)) func(ai.Request) (*ai.Response, error) {
		return func(r ai.Request) (*ai.Response, error) { time.Sleep(d); return next(r) }
	}
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		slow(20*time.Millisecond, ask(tc("c1", "get_order", `{}`))),
		slow(10*time.Millisecond, say("done")),
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{})
	require.NoError(t, err)
	require.Len(t, res.Took, len(res.Responses))
	assert.GreaterOrEqual(t, res.Took[0], 20*time.Millisecond)
	assert.GreaterOrEqual(t, res.Took[1], 10*time.Millisecond)
	assert.Zero(t, res.FailedTook)
}

func TestLoop_ReportsTheDurationOfAFailedProviderCall(t *testing.T) {
	boom := &ai.Error{Provider: "p", Kind: ai.KindProvider}
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		func(ai.Request) (*ai.Response, error) { time.Sleep(15 * time.Millisecond); return nil, boom },
	}}
	res, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(nil), ai.Limits{})
	require.Error(t, err)
	assert.Empty(t, res.Took)
	assert.GreaterOrEqual(t, res.FailedTook, 15*time.Millisecond)
}

// rejectingTool records what the loop handed to Reject and proves Execute never ran.
type rejectingTool struct {
	executed int
	rejected []ai.RejectReason
	gotBytes int
}

func (r *rejectingTool) Execute(context.Context, ai.ToolCall) (ai.ToolResult, error) {
	r.executed++
	return ai.ToolResult{Content: "ran"}, nil
}

func (r *rejectingTool) Reject(_ context.Context, c ai.ToolCall, why ai.RejectReason) ai.ToolResult {
	r.rejected = append(r.rejected, why)
	r.gotBytes = len(c.Arguments)
	return ai.ToolResult{Content: "error: refused", IsError: true}
}

func TestLoop_OversizedArgumentsAreHandedToARejecterNotExecuted(t *testing.T) {
	rt := &rejectingTool{}
	r := resolverWith(rt)
	big := tc("c1", "get_order", `{"id":"`+strings.Repeat("x", 100)+`"}`)
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(big), say("ok")}}

	_, err := ai.RunToolLoop(t.Context(), p, userReq, r, ai.Limits{MaxArgsBytes: 50})
	require.NoError(t, err)
	assert.Zero(t, rt.executed, "a refused call never executes")
	assert.Equal(t, []ai.RejectReason{ai.RejectArgsTooLarge}, rt.rejected)
	assert.Greater(t, rt.gotBytes, 50, "the rejecter sees the call, so it can record it")
	tr := p.requests[1].Messages[2].ToolResults[0]
	assert.Equal(t, "error: refused", tr.Content, "what the rejecter answers is what the model gets")
	assert.True(t, tr.IsError)
}

func TestLoop_AToolWithoutARejecterKeepsTheGenericRefusal(t *testing.T) {
	var ran []string
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "get_order", `{"id":"`+strings.Repeat("x", 100)+`"}`)), say("ok"),
	}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(echoTool(&ran)), ai.Limits{MaxArgsBytes: 50})
	require.NoError(t, err)
	assert.Empty(t, ran)
	assert.Equal(t, "error: arguments too large", p.requests[1].Messages[2].ToolResults[0].Content)
}

func TestLoop_ArgumentsWithinTheLimitStillExecuteAndTheRejecterIsNotCalled(t *testing.T) {
	rt := &rejectingTool{}
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){ask(tc("c1", "get_order", `{}`)), say("ok")}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(rt), ai.Limits{MaxArgsBytes: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, rt.executed)
	assert.Empty(t, rt.rejected)
}

func TestLoop_AnUnknownToolStaysUnknownEvenWithOversizedArguments(t *testing.T) {
	p := &scripted{answers: []func(ai.Request) (*ai.Response, error){
		ask(tc("c1", "nope", `{"id":"`+strings.Repeat("x", 100)+`"}`)), say("ok"),
	}}
	_, err := ai.RunToolLoop(t.Context(), p, userReq, resolverWith(nil), ai.Limits{MaxArgsBytes: 50})
	require.NoError(t, err)
	assert.Equal(t, "error: unknown tool", p.requests[1].Messages[2].ToolResults[0].Content)
}
