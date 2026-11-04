package models

import (
	"time"

	"github.com/google/uuid"
)

// Send intent states. processing is the only non-terminal one; uncertain is terminal for
// the sender (it is never sent again) but stays open for reconciliation.
const (
	SendIntentProcessing = "processing"
	SendIntentSent       = "sent"
	SendIntentFailed     = "failed"
	SendIntentUncertain  = "uncertain"
)

// SendIntentKindProtocol is the only kind today: the protocol message sent to a customer.
const SendIntentKindProtocol = "protocol"

// OccurrenceSendIntent is the idempotency record of ONE deliberate send to a customer from a
// case. The interface generates the key per intention; the unique index on
// (organization_id, idempotency_key) is what makes "one key, one send" true in the database,
// whatever the number of requests, retries or servers.
//
// It is NOT soft-deletable on purpose: a deleted row would free its key, and a freed key can
// send a second message. Rows leave only through the retention rule (see SendIntentRetention).
type OccurrenceSendIntent struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"` // = send_id, the handle the answers and the audit trail carry
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_send_intent_key" json:"organization_id"`
	IdempotencyKey string    `gorm:"size:64;not null;uniqueIndex:uq_send_intent_key" json:"idempotency_key"`

	OccurrenceID uuid.UUID `gorm:"type:uuid;not null;index:idx_send_intent_occurrence,priority:1" json:"occurrence_id"`
	UserID       uuid.UUID `gorm:"type:uuid;not null" json:"user_id"`
	Kind         string    `gorm:"size:20;not null;default:'protocol'" json:"kind"`

	// RequestHash covers everything that decides WHAT is sent and TO WHOM: the same key with a
	// different hash is refused. It is a hash, so the text of the message is not kept here.
	RequestHash string `gorm:"size:64;not null" json:"-"`
	State       string `gorm:"size:16;not null" json:"state"`
	// MessageID is the id of the message row, assigned before the send so the two can always be matched.
	MessageID uuid.UUID `gorm:"type:uuid;not null" json:"message_id"`

	// HTTPStatus and Response are what the first request answered; a repeated request gets the
	// same answer back. Response holds ids, state and text of the answer, never the customer's text.
	HTTPStatus int   `json:"http_status"`
	Response   JSONB `gorm:"type:jsonb" json:"-"`

	ClaimedAt    time.Time  `gorm:"not null;index:idx_send_intent_pending,where:state = 'processing'" json:"claimed_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ReconciledAt *time.Time `json:"reconciled_at,omitempty"`
	CreatedAt    time.Time  `gorm:"autoCreateTime;index:idx_send_intent_occurrence,priority:2" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (OccurrenceSendIntent) TableName() string { return "occurrence_send_intents" }
