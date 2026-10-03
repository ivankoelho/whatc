package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// AIToolCallView is one audited AI tool call as the API returns it. It is an explicit list of
// fields, not the database model, so a column added later never leaks by default. It carries
// metadata only: never argument values, never result content, never ToolCall.Opaque, and not the
// argument HMAC or the provider's call id (those stay in the database for forensics).
type AIToolCallView struct {
	ID               uuid.UUID  `json:"id"`
	RunID            uuid.UUID  `json:"run_id"`
	Step             int        `json:"step"` // the order of the call within its run
	ToolName         string     `json:"tool_name"`
	Risk             string     `json:"risk,omitempty"`
	ActorKind        string     `json:"actor_kind"`
	ActorRef         string     `json:"actor_ref"`
	SubjectContactID *uuid.UUID `json:"subject_contact_id,omitempty"`
	SessionID        *uuid.UUID `json:"session_id,omitempty"`
	WhatsAppAccount  string     `json:"whatsapp_account,omitempty"`
	// Status: requested | denied | executed | failed. A call still "requested" long after it was
	// made was attempted and its outcome is unknown.
	Status          string            `json:"status"`
	DenialReason    string            `json:"denial_reason,omitempty"`
	ArgsKeys        models.JSONBArray `json:"args_keys,omitempty"` // names of the top-level arguments only
	ArgsBytes       int               `json:"args_bytes"`
	ResultBytes     int               `json:"result_bytes"`
	ResultTruncated bool              `json:"result_truncated"`
	ResultIsError   bool              `json:"result_is_error"`
	ErrorKind       string            `json:"error_kind,omitempty"`
	RequestedAt     time.Time         `json:"requested_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	DurationMs      int               `json:"duration_ms"`
}

func aiToolCallView(c models.AIToolCall) AIToolCallView {
	return AIToolCallView{
		ID: c.ID, RunID: c.RunID, Step: c.Step, ToolName: c.ToolName, Risk: c.Risk,
		ActorKind: c.ActorKind, ActorRef: c.ActorRef, SubjectContactID: c.SubjectContactID, SessionID: c.SessionID,
		WhatsAppAccount: c.WhatsAppAccount, Status: c.Status, DenialReason: c.DenialReason, ArgsKeys: c.ArgsKeys,
		ArgsBytes: c.ArgsBytes, ResultBytes: c.ResultSize, ResultTruncated: c.Truncated, ResultIsError: c.ResultErr,
		ErrorKind: c.ErrorKind, RequestedAt: c.RequestedAt, FinishedAt: c.FinishedAt, DurationMs: c.DurationMs,
	}
}

// ListAIToolCalls GET /api/ai-tools/calls: the audit of the AI's tool calls, read only.
// Filters: tool, status, denial_reason, contact_id (the customer the call was about), run_id,
// from, to (YYYY-MM-DD or RFC 3339), page, limit (max 100). Newest first. The organization is
// always the authenticated one. There is no way to change this audit through the API.
func (a *App) ListAIToolCalls(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAITools, models.ActionRead)
	if err != nil {
		return nil
	}
	args := r.RequestCtx.QueryArgs()
	q := a.DB.Model(&models.AIToolCall{}).Where("organization_id = ?", orgID)

	bad := func(msg string) error { return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "") }

	if v := string(args.Peek("tool")); v != "" {
		q = q.Where("tool_name = ?", v)
	}
	if v := string(args.Peek("status")); v != "" {
		switch v {
		case models.AIToolCallRequested, models.AIToolCallDenied, models.AIToolCallExecuted, models.AIToolCallFailed:
			q = q.Where("status = ?", v)
		default:
			return bad("Invalid status")
		}
	}
	if v := string(args.Peek("denial_reason")); v != "" {
		q = q.Where("denial_reason = ?", v)
	}
	for param, column := range map[string]string{"contact_id": "subject_contact_id", "run_id": "run_id"} {
		if v := string(args.Peek(param)); v != "" {
			id, perr := uuid.Parse(v)
			if perr != nil {
				return bad("Invalid " + param)
			}
			q = q.Where(column+" = ?", id)
		}
	}
	if v := string(args.Peek("from")); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return bad("Invalid from")
		}
		q = q.Where("requested_at >= ?", t)
	}
	if v := string(args.Peek("to")); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return bad("Invalid to")
		}
		if _, dateErr := time.Parse(time.DateOnly, v); dateErr == nil {
			t = t.Add(24*time.Hour - time.Second) // a plain date means the whole day
		}
		q = q.Where("requested_at <= ?", t)
	}

	pg := parsePagination(r)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		a.Log.Error("Failed to count AI tool calls", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list AI tool calls", nil, "")
	}
	var rows []models.AIToolCall
	if err := pg.Apply(q.Order("requested_at DESC, id DESC")).Find(&rows).Error; err != nil {
		a.Log.Error("Failed to list AI tool calls", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list AI tool calls", nil, "")
	}
	items := make([]AIToolCallView, len(rows))
	for i, c := range rows {
		items[i] = aiToolCallView(c)
	}
	return r.SendEnvelope(listEnvelope("calls", items, total, pg))
}

func parseAuditTime(v string) (time.Time, bool) {
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}
