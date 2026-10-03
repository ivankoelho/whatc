package aitools

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// genericUnavailable is what the model hears when a call is refused, whatever the reason. The
// reason (unknown tool, not enabled, global switch, write without confirmation, audit down) lives
// only in the audit, so the model learns nothing about the catalog, the policy or the settings.
const genericUnavailable = "error: this tool is not available"

const genericFailed = "error: tool failed"

// finishTimeout bounds the write of an outcome made after the tool ran (the request context may
// already be cancelled by then).
const finishTimeout = 5 * time.Second

// ResolverConfig builds the governed ai.ToolResolver of ONE run (one RunToolLoop).
type ResolverConfig struct {
	Catalog       *Catalog
	GlobalEnabled bool            // ai_tools.enabled
	Enabled       map[string]bool // the tools the organization enabled (a snapshot taken for the run)
	Auditor       Auditor
	Actor         Actor
	Scope         Scope
	RunID         uuid.UUID // a fresh one when zero
	// MaxResultBytes is the size above which a result is recorded as truncated (the loop cuts it
	// at this size before the model sees it). ai.DefaultLimits().MaxResultBytes when zero.
	MaxResultBytes int
	Log            Logger
}

// Resolver is the governed ai.ToolResolver: Definitions offers only what is effectively
// available, and Resolve answers ANY name the model sends with either a GovernedTool or a
// DeniedTool, so every attempt, including a forced call to something never offered, is recorded.
// It never instantiates a real tool at resolution time.
type Resolver struct {
	cfg  ResolverConfig
	step atomic.Int64
}

var _ ai.ToolResolver = (*Resolver)(nil)

// NewResolver builds the resolver of one run. A nil source, or one that fails, leaves every tool
// disabled (fail-closed) and the failure is logged.
func NewResolver(ctx context.Context, cfg ResolverConfig, src EnabledSource) *Resolver {
	if cfg.Enabled == nil {
		cfg.Enabled = map[string]bool{}
		if src != nil && cfg.GlobalEnabled {
			enabled, err := src.EnabledTools(ctx, cfg.Scope.OrganizationID)
			if err != nil {
				logError(cfg.Log, "ai tools: could not read the organization's enabled tools; none is available",
					"organization_id", cfg.Scope.OrganizationID.String(), "error", err.Error())
			} else {
				cfg.Enabled = enabled
			}
		}
	}
	if cfg.RunID == uuid.Nil {
		cfg.RunID = uuid.New()
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = ai.DefaultLimits().MaxResultBytes
	}
	if cfg.Catalog == nil {
		cfg.Catalog = DefaultCatalog()
	}
	return &Resolver{cfg: cfg}
}

// RunID identifies the run in the audit.
func (r *Resolver) RunID() uuid.UUID { return r.cfg.RunID }

func (r *Resolver) verdict(name string) (ToolSpec, Verdict) {
	spec, known := r.cfg.Catalog.Get(name)
	return spec, Authorize(PolicyInput{
		GlobalEnabled: r.cfg.GlobalEnabled, Known: known, OrgEnabled: r.cfg.Enabled[name], Risk: spec.Risk,
	})
}

// Definitions lists only the tools that are effectively available: in the catalog, enabled for the
// organization, allowed by the policy and with the global switch on.
func (r *Resolver) Definitions() []ai.ToolDefinition {
	var defs []ai.ToolDefinition
	for _, name := range r.cfg.Catalog.Names() {
		if spec, v := r.verdict(name); v.Allowed {
			defs = append(defs, spec.Definition())
		}
	}
	return defs
}

// Resolve never fails: an available name gets a GovernedTool, anything else a DeniedTool.
func (r *Resolver) Resolve(name string) (ai.Tool, bool) {
	spec, v := r.verdict(name)
	if !v.Allowed {
		return deniedTool{r: r, reason: v.Reason}, true
	}
	return governedTool{r: r, spec: spec}, true
}

// RecordAborted records, as denied with reason loop_limit, the calls of a round the loop refused
// to run because it went over a limit. Nothing of that round executed.
func (r *Resolver) RecordAborted(ctx context.Context, calls []ai.ToolCall) {
	for _, c := range calls {
		spec, _ := r.cfg.Catalog.Get(c.Name)
		c.Opaque = nil
		r.recordDenied(ctx, r.attempt(c, spec.Risk), DenyLoopLimit)
	}
}

func (r *Resolver) attempt(c ai.ToolCall, risk Risk) Attempt {
	c.Opaque = nil // transport state never goes near the audit
	return Attempt{RunID: r.cfg.RunID, Step: int(r.step.Add(1)), Call: c, Risk: risk, Actor: r.cfg.Actor, Scope: r.cfg.Scope}
}

func (r *Resolver) recordDenied(ctx context.Context, a Attempt, reason string) {
	if err := r.cfg.Auditor.Denied(ctx, a, reason); err != nil {
		logError(r.cfg.Log, "ai tools: could not record a denied call",
			"organization_id", a.Scope.OrganizationID.String(), "run_id", a.RunID.String(), "tool", a.Call.Name, "reason", reason, "error", err.Error())
	}
}

// deniedTool records the refused attempt and tells the model only that the tool is unavailable.
// It holds no Factory and no real tool: there is nothing it could run.
type deniedTool struct {
	r      *Resolver
	reason string
}

func (d deniedTool) Execute(ctx context.Context, call ai.ToolCall) (ai.ToolResult, error) {
	spec, _ := d.r.cfg.Catalog.Get(call.Name)
	d.r.recordDenied(ctx, d.r.attempt(call, spec.Risk), d.reason)
	return ai.ToolResult{Content: genericUnavailable, IsError: true}, nil
}

// governedTool is a tool the policy allowed at resolution time. It is decided again at Execute.
type governedTool struct {
	r    *Resolver
	spec ToolSpec
}

func (g governedTool) Execute(ctx context.Context, call ai.ToolCall) (ai.ToolResult, error) {
	r := g.r
	if _, v := r.verdict(g.spec.Name); !v.Allowed {
		return deniedTool{r: r, reason: v.Reason}.Execute(ctx, call)
	}
	at := r.attempt(call, g.spec.Risk)

	// 1. "requested" is written BEFORE anything of the tool runs; if it cannot be, nothing runs.
	id, err := r.cfg.Auditor.Requested(ctx, at)
	if err != nil {
		logError(r.cfg.Log, "ai tools: could not record the request; the tool was not run",
			"organization_id", at.Scope.OrganizationID.String(), "run_id", at.RunID.String(), "tool", g.spec.Name, "error", err.Error())
		r.recordDenied(ctx, at, DenyAuditUnavailable) // best effort, usually fails the same way
		return ai.ToolResult{Content: genericUnavailable, IsError: true}, nil
	}

	// 2. Only now is the real tool built and run.
	started := time.Now()
	res, errKind := g.run(ctx, call)
	out := Outcome{
		Status: models.AIToolCallExecuted, ResultBytes: len(res.Content), Truncated: len(res.Content) > r.cfg.MaxResultBytes,
		ResultError: res.IsError, ErrorKind: errKind, Duration: time.Since(started),
	}
	if errKind != "" {
		out.Status = models.AIToolCallFailed
		res = ai.ToolResult{Content: genericFailed, IsError: true} // internal error text never leaves
	}

	// 3. The outcome. If it cannot be written the action already happened: never undo it, never
	// run it again; the row stays "requested" (attempted, outcome unknown) and the failure is logged.
	r.finish(ctx, at, id, out)
	return res, nil
}

// run builds and runs the real tool, turning a panic or an error into an error kind.
func (g governedTool) run(ctx context.Context, call ai.ToolCall) (res ai.ToolResult, errKind string) {
	defer func() {
		if recover() != nil {
			res, errKind = ai.ToolResult{}, "panic"
		}
	}()
	tool := g.spec.Factory(g.r.cfg.Scope)
	if tool == nil {
		return ai.ToolResult{}, "tool_error"
	}
	res, err := tool.Execute(ctx, call)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ai.ToolResult{}, "timeout"
	case err != nil:
		return ai.ToolResult{}, "tool_error"
	}
	return res, ""
}

func (r *Resolver) finish(ctx context.Context, at Attempt, id uuid.UUID, o Outcome) {
	var err error
	for try := 0; try < 2; try++ { // a second try of the WRITE of the outcome, never of the tool
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		err = r.cfg.Auditor.Finish(fctx, id, o)
		cancel()
		if err == nil {
			return
		}
	}
	logError(r.cfg.Log, "ai tools: the tool ran but its outcome could not be recorded; the row stays 'requested'",
		"organization_id", at.Scope.OrganizationID.String(), "run_id", at.RunID.String(), "tool", at.Call.Name,
		"audit_row", id.String(), "status", o.Status, "error", fmt.Sprint(err))
}

func logError(l Logger, msg string, kv ...any) {
	if l != nil {
		l.Error(msg, kv...)
	}
}
