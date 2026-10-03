package models

import (
	"time"

	"github.com/google/uuid"
)

// Status of an AIToolConfirmation (Fase 9D). Every transition is a compare-and-set from the state
// it comes from; "confirmed" is transitory (between the customer's authorization and the outcome).
const (
	AIConfirmationPending     = "pending"      // proposed, waiting for the customer
	AIConfirmationConfirmed   = "confirmed"    // the customer authorized (CAS won); outcome not yet known
	AIConfirmationExecuted    = "executed"     // the action was carried out (a transfer was CREATED)
	AIConfirmationNotExecuted = "not_executed" // authorized or closed with nothing done: see Outcome
	AIConfirmationFailed      = "failed"       // authorized, but it could not be carried out
	AIConfirmationDeclined    = "declined"     // the customer said no
	AIConfirmationExpired     = "expired"      // validity ran out
	AIConfirmationSuperseded  = "superseded"   // replaced by a newer proposal of the same contact
	AIConfirmationDenied      = "denied"       // re-authorization refused it before the CAS
)

// Outcome of the transfer service for a confirmed action.
const (
	AIConfirmationOutcomeCreated       = "created"
	AIConfirmationOutcomeAlreadyActive = "already_active"
	AIConfirmationOutcomeOutsideHours  = "outside_hours"
)

// Error kinds specific to confirmations (AIToolConfirmation.ErrorKind).
const (
	AIConfirmationErrReconcileStale     = "reconcile_stale"
	AIConfirmationErrReconcileExhausted = "reconcile_exhausted"
	AIConfirmationErrExecution          = "execution_error"
)

// AIToolDenyWriteDisabled is the policy reason for a write tool while ai_tools.write_enabled is off.
const AIToolDenyWriteDisabled = "write_disabled"

// More denial reasons of a confirmation's re-authorization.
const (
	AIToolDenyProviderNotValidated = "provider_not_validated"
	AIToolDenyChatbotDisabled      = "chatbot_disabled"
	AIToolDenyTampered             = "tampered"
)

// AIToolConfirmation is one proposal of the AI that needs the customer's authorization before
// anything happens (Fase 9D). It is the truth about what became of the proposal: ai_tool_calls only
// says that the proposing tool call ran.
//
// Args is the sanitized payload needed to reproduce exactly the confirmed action (and to recompute
// ActionDigest). It is an integrity payload, not a readable audit record: no API returns it. The
// token itself is never stored, only its SHA-256.
type AIToolConfirmation struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_ai_confirm_org_contact,priority:1" json:"organization_id"`
	ContactID      uuid.UUID `gorm:"type:uuid;not null;index:idx_ai_confirm_org_contact,priority:2" json:"contact_id"`
	SessionID      uuid.UUID `gorm:"type:uuid;not null" json:"session_id"`
	RunID          uuid.UUID `gorm:"type:uuid;not null" json:"run_id"`
	ToolName       string    `gorm:"size:64;not null" json:"tool_name"`
	Risk           string    `gorm:"size:10" json:"risk"`

	TokenHash    string `gorm:"size:64;not null;uniqueIndex:idx_ai_confirm_token" json:"-"`
	Args         JSONB  `gorm:"type:jsonb" json:"-"`
	ActionDigest string `gorm:"size:64;not null" json:"-"`

	Status       string `gorm:"size:16;not null;index:idx_ai_confirm_status_confirmed,priority:1;index:idx_ai_confirm_status_expires,priority:1" json:"status"`
	Outcome      string `gorm:"size:20" json:"outcome,omitempty"`
	DenialReason string `gorm:"size:30" json:"denial_reason,omitempty"`
	ErrorKind    string `gorm:"size:30" json:"error_kind,omitempty"`

	ProposedAt  time.Time  `gorm:"not null;index:idx_ai_confirm_org_contact,priority:3,sort:desc" json:"proposed_at"`
	ExpiresAt   time.Time  `gorm:"not null;index:idx_ai_confirm_status_expires,priority:2" json:"expires_at"`
	ConfirmedAt *time.Time `gorm:"index:idx_ai_confirm_status_confirmed,priority:2" json:"confirmed_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`

	ConfirmedByContactID *uuid.UUID `gorm:"type:uuid" json:"confirmed_by_contact_id,omitempty"`
	ConfirmWAMID         string     `gorm:"column:confirm_wamid;size:128" json:"-"`

	ReconcileClaimedAt *time.Time `json:"-"`
	ReconcileAttempts  int        `gorm:"not null;default:0" json:"-"`

	ProposalCallID  *uuid.UUID `gorm:"type:uuid" json:"proposal_call_id,omitempty"`
	ExecutionCallID *uuid.UUID `gorm:"type:uuid" json:"execution_call_id,omitempty"`
	TransferID      *uuid.UUID `gorm:"type:uuid" json:"transfer_id,omitempty"`
}

func (AIToolConfirmation) TableName() string { return "ai_tool_confirmations" }
