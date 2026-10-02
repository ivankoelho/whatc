package aitools

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Logger is the part of the application logger the package needs.
type Logger interface {
	Error(msg string, keysAndValues ...any)
}

// Attempt is what is known about a tool call when it is recorded. Call.Opaque is never read.
type Attempt struct {
	RunID uuid.UUID
	Step  int // the ordinal of the call within its run, starting at 1
	Call  ai.ToolCall
	Risk  Risk // empty for a tool that is not in the catalog
	Actor Actor
	Scope Scope
}

// Outcome closes an attempt that was allowed to run.
type Outcome struct {
	Status      string // models.AIToolCallExecuted | models.AIToolCallFailed
	ResultBytes int
	Truncated   bool
	ResultError bool   // the tool reported an error result to the model
	ErrorKind   string // tool_error | panic | timeout; "" when it did not fail
	Duration    time.Duration
}

// Auditor records tool calls. "requested" is written before the tool runs and, if it cannot be,
// the tool does not run; the outcome is written afterwards and its failure never undoes or repeats
// the execution.
type Auditor interface {
	Requested(ctx context.Context, a Attempt) (uuid.UUID, error)
	Denied(ctx context.Context, a Attempt, reason string) error
	Finish(ctx context.Context, id uuid.UUID, o Outcome) error
}

// DBAuditor writes to ai_tool_calls, synchronously. Unlike audit.LogAudit it is not fire-and-forget:
// an audit that can silently be lost cannot be the thing that authorizes an action.
type DBAuditor struct {
	DB *gorm.DB
	// Secret keys the HMAC of the arguments (the server's encryption key). Empty: no HMAC is stored.
	Secret string
}

var _ Auditor = DBAuditor{}

func (d DBAuditor) row(a Attempt) models.AIToolCall {
	name := a.Call.Name
	if len(name) > 64 { // the model may call anything; keep the column bounded
		name = name[:64]
	}
	callID := a.Call.ID
	if len(callID) > 128 {
		callID = callID[:128]
	}
	args := []byte(a.Call.Arguments)
	row := models.AIToolCall{
		OrganizationID: a.Scope.OrganizationID, RunID: a.RunID, Step: a.Step, CallID: callID,
		ToolName: name, Risk: string(a.Risk), ActorKind: string(a.Actor.Kind), ActorRef: a.Actor.Ref,
		SubjectContactID: a.Scope.ContactID, SessionID: a.Scope.SessionID, WhatsAppAccount: a.Scope.WhatsAppAccount,
		ArgsBytes: len(args), ArgsHMAC: ArgsHMAC(d.Secret, args), RequestedAt: time.Now(),
	}
	if keys := ArgsKeys(args); len(keys) > 0 {
		row.ArgsKeys = make(models.JSONBArray, len(keys))
		for i, k := range keys {
			row.ArgsKeys[i] = k
		}
	}
	return row
}

func (d DBAuditor) Requested(ctx context.Context, a Attempt) (uuid.UUID, error) {
	row := d.row(a)
	row.Status = models.AIToolCallRequested
	if err := d.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

func (d DBAuditor) Denied(ctx context.Context, a Attempt, reason string) error {
	row := d.row(a)
	row.Status, row.DenialReason = models.AIToolCallDenied, reason
	now := time.Now()
	row.FinishedAt = &now
	return d.DB.WithContext(ctx).Create(&row).Error
}

func (d DBAuditor) Finish(ctx context.Context, id uuid.UUID, o Outcome) error {
	now := time.Now()
	return d.DB.WithContext(ctx).Model(&models.AIToolCall{}).Where("id = ?", id).Updates(map[string]any{
		"status": o.Status, "result_bytes": o.ResultBytes, "result_truncated": o.Truncated,
		"result_is_error": o.ResultError, "error_kind": o.ErrorKind,
		"finished_at": now, "duration_ms": int(o.Duration.Milliseconds()),
	}).Error
}
