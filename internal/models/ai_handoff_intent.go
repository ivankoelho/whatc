package models

import (
	"time"

	"github.com/google/uuid"
)

// Status of an AIHandoffIntent.
const (
	AIHandoffIntentPending   = "pending"   // recorded; the handoff has not finished
	AIHandoffIntentDone      = "done"      // finished; Outcome says how
	AIHandoffIntentAbandoned = "abandoned" // retries exhausted: a person must look at this contact
)

// AIHandoffIntent is the agent's promise to hand a conversation to people, written to the database BEFORE
// the customer is told anything. The customer must never read "vou chamar um atendente" while nothing
// recoverable exists that makes it true: if the process dies between that message and the attendance being
// created, this row is what a later run (this or another instance) finds and completes.
//
// At most one row per contact is "pending" (partial unique index), which is what makes recording it
// idempotent. Completing it is idempotent as well: the attendance table allows one active attendance per
// contact, so running the handoff twice never produces two attendances or two waiting messages.
//
// It holds no message text and no token: only ids, the reason code and what happened.
type AIHandoffIntent struct {
	BaseModel
	OrganizationID  uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	ContactID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_ai_handoff_pending,where:status = 'pending'" json:"contact_id"`
	SessionID       uuid.UUID `gorm:"type:uuid;not null" json:"session_id"`
	WhatsAppAccount string    `gorm:"size:100;not null" json:"whatsapp_account"`
	Reason          string    `gorm:"size:40;not null" json:"reason"`

	Status string `gorm:"size:20;not null;default:'pending';index:idx_ai_handoff_due,priority:1" json:"status"`
	// LockedUntil is the lease of whoever is carrying the handoff out right now (the live request, or a
	// recovery run). Only when it has passed may another run take it over.
	LockedUntil *time.Time `gorm:"index:idx_ai_handoff_due,priority:2" json:"locked_until,omitempty"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Outcome     string     `gorm:"size:40" json:"outcome"`
	// RecoveredAt is set when a recovery run (not the live request) carried the handoff out.
	RecoveredAt *time.Time `json:"recovered_at,omitempty"`
	LastError   string     `gorm:"size:200" json:"last_error"`
}

func (AIHandoffIntent) TableName() string { return "ai_handoff_intents" }
