package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentTransferAssignment is one change in who is responsible for an attendance.
// The table is append-only: AgentTransfer holds only the CURRENT owner, so this is
// what keeps "who had it before, who took it, when and why" once ownership moves.
//
// Each row is an event, not an interval. The interval an agent held an attendance
// is derived (from one event to the next for the same transfer), which avoids
// closing an open row and the races that come with it. FromAgentID/ToAgentID nil
// means "the queue": a transfer waiting for an agent has no owner.
//
// Coverage starts when this table does; earlier changes live only in audit_logs.
type AgentTransferAssignment struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	TransferID     uuid.UUID  `gorm:"type:uuid;not null" json:"transfer_id"`
	TeamID         *uuid.UUID `gorm:"type:uuid" json:"team_id,omitempty"`
	FromAgentID    *uuid.UUID `gorm:"type:uuid" json:"from_agent_id,omitempty"`
	ToAgentID      *uuid.UUID `gorm:"type:uuid" json:"to_agent_id,omitempty"`
	// Reason is the distribution event that caused it (claimed, assigned, released,
	// returned_away, returned_offline, returned_away_timeout, reassigned_away_timeout,
	// reassigned_customer_waiting, auto_assigned, queued) or "closed".
	Reason string `gorm:"size:40;not null" json:"reason"`
	// ActorUserID is who did it; nil is the system (strategy, reaper, sweep).
	ActorUserID *uuid.UUID `gorm:"type:uuid" json:"actor_user_id,omitempty"`
	OccurredAt  time.Time  `gorm:"not null" json:"occurred_at"`
	// The SLA state of the attendance at that moment.
	EscalationLevel int  `gorm:"not null;default:0" json:"escalation_level"`
	SLABreached     bool `gorm:"column:sla_breached;not null;default:false" json:"sla_breached"`
}

func (AgentTransferAssignment) TableName() string { return "agent_transfer_assignments" }
