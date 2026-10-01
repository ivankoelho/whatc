package models

import (
	"time"

	"github.com/google/uuid"
)

// AIUsageLog is one call to an AI provider: what was asked of whom, how long it
// took, what it cost in tokens and how it ended. It is the base for validating
// integrations now and for cost/performance reporting later.
//
// It never stores prompts, answers or credentials: only identifiers of the
// context the call was made for (contact, session, user) and sanitized error text.
type AIUsageLog struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_ai_usage_org_created,priority:1" json:"organization_id"`
	Provider       string    `gorm:"size:20;not null;index" json:"provider"`
	Model          string    `gorm:"size:100" json:"model"`
	// Feature says which part of Whatc made the call ("chatbot_reply",
	// "chatbot_flow_node", "model_list", ...). Later: assistant, classification...
	Feature string `gorm:"size:50;not null" json:"feature"`

	UserID          *uuid.UUID `gorm:"type:uuid" json:"user_id,omitempty"`
	ContactID       *uuid.UUID `gorm:"type:uuid;index" json:"contact_id,omitempty"`
	SessionID       *uuid.UUID `gorm:"type:uuid" json:"session_id,omitempty"`
	WhatsAppAccount string     `gorm:"size:100" json:"whatsapp_account,omitempty"`

	Success      bool   `gorm:"not null;default:false" json:"success"`
	ErrorKind    string `gorm:"size:30" json:"error_kind,omitempty"` // ai.ErrorKind, or "credentials"
	HTTPStatus   int    `json:"http_status,omitempty"`
	ErrorMessage string `gorm:"type:text" json:"error_message,omitempty"` // sanitized, truncated

	LatencyMs    int `json:"latency_ms"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	CreatedAt time.Time `gorm:"autoCreateTime;index:idx_ai_usage_org_created,priority:2,sort:desc" json:"created_at"`
}

func (AIUsageLog) TableName() string { return "ai_usage_logs" }
