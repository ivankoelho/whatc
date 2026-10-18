package models

import (
	"time"

	"github.com/google/uuid"
)

// AgentState is what an agent is right now. It is derived, never stored on the
// user: see assignment.AgentStateOf for the single definition.
type AgentState string

const (
	AgentStateOnline  AgentState = "online"  // connected and available: can receive new attendances
	AgentStateAway    AgentState = "away"    // connected but marked away
	AgentStateOffline AgentState = "offline" // no live connection
)

// Why a presence interval ended.
const (
	PresenceClosedTransition = "transition" // the agent's state changed
	PresenceClosedRestart    = "restart"    // the process restarted; the real end is unknown
)

// AgentPresenceLog records every state an agent has been in, per organization,
// as intervals. The one open interval (EndedAt nil) is the current state and
// StartedAt is the instant it really began, so "away for 08:32" is a difference
// of two timestamps, not a counter.
//
// Coverage starts when this table does: offline/online time before that was
// never recorded and cannot be reconstructed. An interval closed with reason
// "restart" ends at the restart, not at the true moment the connection dropped.
type AgentPresenceLog struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null" json:"user_id"`
	State          AgentState `gorm:"size:10;not null" json:"state"`
	StartedAt      time.Time  `gorm:"not null" json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ClosedReason   string     `gorm:"size:20" json:"closed_reason,omitempty"`
}

func (AgentPresenceLog) TableName() string { return "agent_presence_log" }
