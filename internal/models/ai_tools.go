package models

import (
	"time"

	"github.com/google/uuid"
)

// AIToolSetting says whether one catalog tool is enabled for one organization (Fase 9B).
// Opt-in: no row means disabled. The tool itself lives in code (internal/aitools catalog),
// never in the database; this table only records an administrator's decision.
type AIToolSetting struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_ai_tool_settings_org_tool,priority:1" json:"organization_id"`
	ToolName       string     `gorm:"size:64;not null;uniqueIndex:idx_ai_tool_settings_org_tool,priority:2" json:"tool_name"`
	Enabled        bool       `gorm:"not null;default:false" json:"enabled"`
	UpdatedByID    *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"` // the human who last changed it
	CreatedAt      time.Time  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}

func (AIToolSetting) TableName() string { return "ai_tool_settings" }

// AI tool call status. "requested" is written BEFORE the tool runs; a row that stays
// "requested" means "attempted, outcome unknown".
const (
	AIToolCallRequested = "requested"
	AIToolCallDenied    = "denied"
	AIToolCallExecuted  = "executed"
	AIToolCallFailed    = "failed"
)

// Why a tool call was denied. Closed list. The model is never told which one applies.
const (
	AIToolDenyUnknownTool             = "unknown_tool"
	AIToolDenyNotEnabled              = "not_enabled"
	AIToolDenyGlobalOff               = "global_off"
	AIToolDenyConfirmationUnavailable = "confirmation_unavailable"
	AIToolDenyLoopLimit               = "loop_limit"
	AIToolDenyAuditUnavailable        = "audit_unavailable"
	AIToolDenyArgsTooLarge            = "args_too_large"
)

// Risk class of a tool and who acted.
const (
	AIToolRiskRead  = "read"
	AIToolRiskWrite = "write"

	AIActorKindAI = "ai"
)

// AIToolCall is the audit record of one tool call the AI attempted (Fase 9B), one row per
// ToolCall. The actor is the AI; the customer is only the subject. It never holds argument
// values, result content, ToolCall.Opaque or internal error text: just sizes, the names of the
// top-level argument keys and a keyed HMAC of the canonical arguments.
type AIToolCall struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_ai_tool_calls_org_requested,priority:1;index:idx_ai_tool_calls_org_tool,priority:1" json:"organization_id"`
	RunID          uuid.UUID `gorm:"type:uuid;not null;index" json:"run_id"`
	// Step is the ORDER of the call within its run_id (1, 2, 3...), not the number of the
	// RunToolLoop step: the Tool port does not expose round boundaries.
	Step     int    `json:"step"`
	CallID   string `gorm:"size:128" json:"call_id"` // the provider's id; unique only within the run
	ToolName string `gorm:"size:64;index:idx_ai_tool_calls_org_tool,priority:2" json:"tool_name"`
	Risk     string `gorm:"size:10" json:"risk,omitempty"`

	ActorKind        string     `gorm:"size:10;not null" json:"actor_kind"`
	ActorRef         string     `gorm:"size:50" json:"actor_ref"` // the feature that asked, e.g. chatbot_reply
	SubjectContactID *uuid.UUID `gorm:"type:uuid" json:"subject_contact_id,omitempty"`
	SessionID        *uuid.UUID `gorm:"type:uuid" json:"session_id,omitempty"`
	WhatsAppAccount  string     `gorm:"size:100" json:"whatsapp_account,omitempty"`

	Status       string `gorm:"size:12;not null" json:"status"`
	DenialReason string `gorm:"size:30" json:"denial_reason,omitempty"`

	ArgsKeys   JSONBArray `gorm:"type:jsonb" json:"args_keys,omitempty"` // top-level key names only
	ArgsBytes  int        `json:"args_bytes"`
	ArgsHMAC   string     `gorm:"column:args_hmac_sha256;size:64" json:"args_hmac_sha256,omitempty"` // hex; empty without a server secret
	ResultSize int        `gorm:"column:result_bytes" json:"result_bytes"`
	Truncated  bool       `gorm:"column:result_truncated" json:"result_truncated"`
	ResultErr  bool       `gorm:"column:result_is_error" json:"result_is_error"`
	ErrorKind  string     `gorm:"size:20" json:"error_kind,omitempty"`

	RequestedAt time.Time  `gorm:"not null;index:idx_ai_tool_calls_org_requested,priority:2,sort:desc" json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	DurationMs  int        `json:"duration_ms"`
}

func (AIToolCall) TableName() string { return "ai_tool_calls" }
