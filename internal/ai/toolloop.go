package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The tool loop asks the provider, hands every ToolCall to a resolver/executor and sends the
// results back, within hard limits. It never assumes a ToolCall may be run: it only asks the
// ToolResolver, which is where authorization, the per-organization catalog and the audit will
// sit (later phases). Provider -> ToolCall -> loop -> Resolve -> Execute.

// ToolResolver finds the tools the loop may use. An empty resolver offers nothing.
type ToolResolver interface {
	Definitions() []ToolDefinition
	Resolve(name string) (Tool, bool)
}

// Tool runs one call. A failure the model should hear about is returned as
// ToolResult{IsError: true}; a Go error is an internal failure and reaches the model only as a
// generic "tool failed" (its text never leaves the process).
type Tool interface {
	Execute(ctx context.Context, call ToolCall) (ToolResult, error)
}

// Limits bound the loop. Zero fields take the defaults of DefaultLimits.
type Limits struct {
	MaxSteps       int           // provider calls that may answer with tool calls
	MaxToolCalls   int           // tool calls over the whole loop
	MaxCallsStep   int           // tool calls in one provider answer
	Timeout        time.Duration // wall clock for the whole loop
	MaxArgsBytes   int           // arguments of one call
	MaxResultBytes int           // result sent back to the model (cut with a marker)
}

func DefaultLimits() Limits {
	return Limits{MaxSteps: 4, MaxToolCalls: 8, MaxCallsStep: 4, Timeout: 20 * time.Second, MaxArgsBytes: 8 << 10, MaxResultBytes: 8 << 10}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxSteps <= 0 {
		l.MaxSteps = d.MaxSteps
	}
	if l.MaxToolCalls <= 0 {
		l.MaxToolCalls = d.MaxToolCalls
	}
	if l.MaxCallsStep <= 0 {
		l.MaxCallsStep = d.MaxCallsStep
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	if l.MaxArgsBytes <= 0 {
		l.MaxArgsBytes = d.MaxArgsBytes
	}
	if l.MaxResultBytes <= 0 {
		l.MaxResultBytes = d.MaxResultBytes
	}
	return l
}

// ErrToolLoopLimit is returned when the model keeps asking for tools past a limit.
var ErrToolLoopLimit = errors.New("tool loop limit reached")

// LoopResult is what the loop did, also returned (partial) together with an error. It is meant
// for callers that may persist or audit it, so no ToolCall in it carries Opaque.
type LoopResult struct {
	Response  *Response   // the last provider answer; its Text is the reply when the loop succeeded
	Responses []*Response // every provider answer, in order, each with its own Usage
	Messages  []Message   // what the loop appended to the conversation (assistant calls, tool results)
	ToolCalls int         // tool calls handled
	Usage     Usage       // summed over all provider answers
}

const truncMarker = " …[truncated]"

// RunToolLoop runs req against p, offering the tools of r. Without tools to offer, or with a
// provider that cannot call tools, it is one plain Complete. There is NO automatic retry here:
// provider errors and malformed tool calls (KindInvalidToolCall) end the loop and are returned.
func RunToolLoop(ctx context.Context, p Provider, req Request, r ToolResolver, lim Limits) (*LoopResult, error) {
	lim = lim.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	res := &LoopResult{}
	if r != nil && p.Capabilities().ToolCalling {
		req.Tools = r.Definitions()
	} else {
		req.Tools = nil
	}
	req.Messages = append([]Message(nil), req.Messages...)

	call := func(r Request) (*Response, error) {
		if err := ctx.Err(); err != nil {
			return nil, ctxError(p.Name(), err)
		}
		resp, err := p.Complete(ctx, r)
		if err != nil {
			return nil, err
		}
		out := *resp // what leaves the loop never carries ToolCall.Opaque (transport state)
		out.ToolCalls = withoutOpaque(resp.ToolCalls)
		res.Response = &out
		res.Responses = append(res.Responses, &out)
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		res.Usage.TotalTokens += resp.Usage.TotalTokens
		return resp, nil
	}

	for step := 0; step < lim.MaxSteps; step++ {
		resp, err := call(req)
		if err != nil {
			return res, err
		}
		if len(resp.ToolCalls) == 0 {
			return res, nil
		}
		if len(resp.ToolCalls) > lim.MaxCallsStep || res.ToolCalls+len(resp.ToolCalls) > lim.MaxToolCalls {
			// nothing of this round runs: a partial round could leave an action half done
			return res, fmt.Errorf("%w: %d tool calls requested", ErrToolLoopLimit, len(resp.ToolCalls))
		}
		results, err := runCalls(ctx, p.Name(), r, resp.ToolCalls, lim)
		if err != nil {
			return res, err
		}
		res.ToolCalls += len(resp.ToolCalls)
		asst := Message{Role: RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls}
		tool := Message{Role: RoleTool, ToolResults: results}
		req.Messages = append(req.Messages, asst, tool) // replay keeps Opaque, for the adapter only
		asst.ToolCalls = withoutOpaque(asst.ToolCalls)
		res.Messages = append(res.Messages, asst, tool)
	}

	// Out of steps: one last answer with tools switched off, to get text for the customer.
	req.ToolChoice = ToolChoiceNone
	resp, err := call(req)
	if err != nil {
		return res, err
	}
	if len(resp.ToolCalls) > 0 {
		return res, fmt.Errorf("%w: still asking for tools after %d steps", ErrToolLoopLimit, lim.MaxSteps)
	}
	return res, nil
}

// runCalls handles the calls of one round, one after the other and in the provider's order.
// Every call gets exactly one result; none is skipped.
func runCalls(ctx context.Context, provider string, r ToolResolver, calls []ToolCall, lim Limits) ([]ToolResult, error) {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		if err := ctx.Err(); err != nil {
			return nil, ctxError(provider, err)
		}
		res := runOne(ctx, r, c, lim)
		res.CallID, res.Name = c.ID, c.Name // a tool cannot answer for another call
		res.Content = truncateUTF8(res.Content, lim.MaxResultBytes)
		out = append(out, res)
	}
	return out, nil
}

func runOne(ctx context.Context, r ToolResolver, c ToolCall, lim Limits) (res ToolResult) {
	fail := func(msg string) ToolResult { return ToolResult{Content: msg, IsError: true} }
	if len(c.Arguments) > lim.MaxArgsBytes {
		return fail("error: arguments too large")
	}
	if r == nil {
		return fail("error: unknown tool")
	}
	tool, ok := r.Resolve(c.Name)
	if !ok || tool == nil {
		return fail("error: unknown tool")
	}
	defer func() {
		if recover() != nil {
			res = fail("error: tool failed")
		}
	}()
	out, err := tool.Execute(ctx, c)
	if err != nil {
		return fail("error: tool failed")
	}
	return out
}

// withoutOpaque copies calls with the adapter's transport state removed.
func withoutOpaque(calls []ToolCall) []ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, c := range calls {
		c.Opaque = nil
		out[i] = c
	}
	return out
}

func ctxError(provider string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Provider: provider, Kind: KindTimeout, Message: "tool loop timed out"}
	}
	return err
}

// truncateUTF8 cuts s to at most max bytes without splitting a character, marking the cut.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(truncMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ") + truncMarker
}
