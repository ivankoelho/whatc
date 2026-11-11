package models

import (
	"time"

	"github.com/google/uuid"
)

// MaxPinnedConversations is how many conversations one user can pin.
const MaxPinnedConversations = 3

// ConversationPin is a user's personal "fixed at the top" mark on a
// conversation, like WhatsApp's pin. It is per user: pinning never changes
// what anyone else sees. Composite primary key = at most one pin per
// (user, contact); rows are hard-deleted on unpin.
type ConversationPin struct {
	UserID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	ContactID      uuid.UUID `gorm:"type:uuid;primaryKey;index" json:"contact_id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	PinnedAt       time.Time `gorm:"not null;default:now()" json:"pinned_at"`
}

func (ConversationPin) TableName() string { return "conversation_pins" }
