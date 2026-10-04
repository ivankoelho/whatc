package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// AIToolConfirmationView is one AI confirmation as the API returns it: where a proposal stands and
// what became of it. An explicit list of metadata fields, so a column added later never leaks by
// default. It never carries the token or its hash, the action digest, the arguments (and with them
// the reason the model gave: the stored payload exists to reproduce the action, not to be read), the
// WhatsApp message id of the tap, or the reconciler's bookkeeping.
type AIToolConfirmationView struct {
	ID               uuid.UUID  `json:"id"`
	ToolName         string     `json:"tool_name"`
	Status           string     `json:"status"`
	Outcome          string     `json:"outcome,omitempty"`
	DenialReason     string     `json:"denial_reason,omitempty"`
	ProposedAt       time.Time  `json:"proposed_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	SubjectContactID uuid.UUID  `json:"subject_contact_id"`
	SessionID        uuid.UUID  `json:"session_id"`
	RunID            uuid.UUID  `json:"run_id"`
	TransferID       *uuid.UUID `json:"transfer_id,omitempty"`
}

func aiToolConfirmationView(c models.AIToolConfirmation) AIToolConfirmationView {
	return AIToolConfirmationView{
		ID: c.ID, ToolName: c.ToolName, Status: c.Status, Outcome: c.Outcome, DenialReason: c.DenialReason,
		ProposedAt: c.ProposedAt, ExpiresAt: c.ExpiresAt, ConfirmedAt: c.ConfirmedAt, FinishedAt: c.FinishedAt,
		SubjectContactID: c.ContactID, SessionID: c.SessionID, RunID: c.RunID, TransferID: c.TransferID,
	}
}

var aiConfirmationStatuses = map[string]bool{
	models.AIConfirmationPending: true, models.AIConfirmationConfirmed: true, models.AIConfirmationExecuted: true,
	models.AIConfirmationNotExecuted: true, models.AIConfirmationFailed: true, models.AIConfirmationDeclined: true,
	models.AIConfirmationExpired: true, models.AIConfirmationSuperseded: true, models.AIConfirmationDenied: true,
}

// ListAIToolConfirmations GET /api/ai-tools/confirmations: the AI's proposals that needed the
// customer's confirmation, read only. Filters: status, outcome, contact_id (the customer), from, to
// (read in UTC, as in /api/ai-tools/calls), page, limit (max 100). Newest first, always scoped to the
// authenticated organization. A row stuck in "confirmed" is a request whose outcome is unknown.
func (a *App) ListAIToolConfirmations(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAITools, models.ActionRead)
	if err != nil {
		return nil
	}
	args := r.RequestCtx.QueryArgs()
	q := a.DB.Model(&models.AIToolConfirmation{}).Where("organization_id = ?", orgID)
	bad := func(msg string) error { return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "") }

	if v := string(args.Peek("status")); v != "" {
		if !aiConfirmationStatuses[v] {
			return bad("Invalid status")
		}
		q = q.Where("status = ?", v)
	}
	if v := string(args.Peek("outcome")); v != "" {
		q = q.Where("outcome = ?", v)
	}
	if v := string(args.Peek("contact_id")); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			return bad("Invalid contact_id")
		}
		q = q.Where("contact_id = ?", id)
	}
	if v := string(args.Peek("from")); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return bad("Invalid from")
		}
		q = q.Where("proposed_at >= ?", t)
	}
	if v := string(args.Peek("to")); v != "" {
		t, ok := parseAuditTime(v)
		if !ok {
			return bad("Invalid to")
		}
		if _, dateErr := time.Parse(time.DateOnly, v); dateErr == nil {
			t = t.Add(24*time.Hour - time.Second)
		}
		q = q.Where("proposed_at <= ?", t)
	}

	pg := parsePagination(r)
	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		a.Log.Error("Failed to count AI tool confirmations", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list AI tool confirmations", nil, "")
	}
	var rows []models.AIToolConfirmation
	if err := pg.Apply(q.Order("proposed_at DESC, id DESC")).Find(&rows).Error; err != nil {
		a.Log.Error("Failed to list AI tool confirmations", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list AI tool confirmations", nil, "")
	}
	items := make([]AIToolConfirmationView, len(rows))
	for i, c := range rows {
		items[i] = aiToolConfirmationView(c)
	}
	return r.SendEnvelope(listEnvelope("confirmations", items, total, pg))
}
