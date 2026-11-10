package models

import (
	"time"

	"github.com/google/uuid"
)

// Status of an AIPendingReply.
const (
	AIPendingStatusPending    = "pending"    // waiting for the quiet period to end
	AIPendingStatusProcessing = "processing" // claimed by a worker: the answer is being produced
	AIPendingStatusDone       = "done"       // finished; Outcome says how
)

// AIPendingReply is the agent's wait before answering a customer: the messages received since the
// customer last paused, and the instant the quiet period ends. It lives in the database, not in
// memory, so a restart of the service loses neither the messages nor the timer. At most one row per
// contact is "pending" (partial unique index); a new message of that contact extends it.
//
// Finished rows stay as the audit trail of the wait: what happened (Outcome), when, how many messages
// were grouped (MessageIDs, as WhatsApp ids only, never their text) and what the typing indicator did
// (counters and a short redacted error class). No token and no message content is stored here.
type AIPendingReply struct {
	BaseModel
	OrganizationID  uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	ContactID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_ai_pending_open,where:status = 'pending'" json:"contact_id"`
	SessionID       uuid.UUID `gorm:"type:uuid;not null" json:"session_id"`
	AgentID         uuid.UUID `gorm:"type:uuid;not null" json:"agent_id"`
	WhatsAppAccount string    `gorm:"size:100;not null" json:"whatsapp_account"`
	IsNewSession    bool      `gorm:"not null;default:false" json:"is_new_session"`

	Status string    `gorm:"size:20;not null;default:'pending';index:idx_ai_pending_due,priority:1" json:"status"`
	DueAt  time.Time `gorm:"not null;index:idx_ai_pending_due,priority:2" json:"due_at"`

	// FirstMessageAt is when the first grouped message was received (it also bounds the history sent
	// to the model, which must not repeat the grouped messages). LastMessageAt is the latest one.
	FirstMessageAt time.Time   `gorm:"not null" json:"first_message_at"`
	LastMessageAt  time.Time   `gorm:"not null" json:"last_message_at"`
	MessageIDs     StringArray `gorm:"type:jsonb" json:"message_ids"`
	// LastMessageID is the customer's latest message: the one the typing indicator is shown for.
	LastMessageID string `gorm:"size:255" json:"last_message_id"`
	WaitSeconds   int    `gorm:"not null;default:0" json:"wait_seconds"`

	LockedUntil *time.Time `json:"locked_until,omitempty"`
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	// SendStartedAt is persisted, under the contact lock, right BEFORE the answer is handed to WhatsApp.
	// It is the only fact that survives a crash between "Meta accepted" and "the database knows": a row
	// that has it may have reached the customer, so it is never run again (see reclaimStaleAIReplies).
	SendStartedAt *time.Time `json:"send_started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Outcome       string     `gorm:"size:40" json:"outcome"`

	TypingStarts    int    `gorm:"not null;default:0" json:"typing_starts"`
	TypingRenewals  int    `gorm:"not null;default:0" json:"typing_renewals"`
	TypingFailures  int    `gorm:"not null;default:0" json:"typing_failures"`
	TypingLastError string `gorm:"size:200" json:"typing_last_error"`
}

func (AIPendingReply) TableName() string { return "ai_pending_replies" }
