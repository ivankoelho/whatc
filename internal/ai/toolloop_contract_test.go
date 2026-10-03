package ai_test

import (
	"context"
	"encoding/json"
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
