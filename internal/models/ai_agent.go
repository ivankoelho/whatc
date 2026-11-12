package models

import "github.com/google/uuid"

// AIAgent is a configured AI agent of an organization (MVP: one default agent is used by the
// chatbot; the table already allows several). The free-text fields are what the organization
// tells the agent; the platform's own rules (never state price, stock or deadline as
// confirmed...) are fixed in code and are not editable here.
type AIAgent struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string    `gorm:"size:255;not null" json:"name"`
	// DisplayName is the name the customer sees before the agent's messages ("*Name:*"). Empty means
	// "Assistente Virtual". It names a machine, never a person: the AI is not stored as a user.
	DisplayName  string `gorm:"size:100" json:"display_name"`
	Objective    string `gorm:"type:text" json:"objective"`
	Instructions string `gorm:"type:text" json:"instructions"`
	CompanyInfo  string `gorm:"type:text" json:"company_info"`
	Tone         string `gorm:"type:text" json:"tone"`
	// HandoffRules says when the agent must hand the conversation to a human.
	HandoffRules string `gorm:"type:text" json:"handoff_rules"`
	IsEnabled    bool   `gorm:"default:false" json:"is_enabled"`
	// WaitSeconds is how long this agent waits, after the customer's last message, before answering,
	// so a customer who writes in several short messages gets one answer. 0 (the default) answers at
	// once, exactly as before. Independent per agent.
	WaitSeconds int `gorm:"not null;default:0" json:"wait_seconds"`

	// WhatsAppAccounts are the names of the numbers (WhatsAppAccount.Name) where this agent may
	// act. Fail-closed: empty means nowhere. This, not ChatbotSettings, is what limits a pilot to
	// one number, because ChatbotSettings is one row per organization.
	WhatsAppAccounts StringArray `gorm:"column:whatsapp_accounts;type:jsonb" json:"whatsapp_accounts"`

	CreatedByID *uuid.UUID `gorm:"type:uuid" json:"created_by_id,omitempty"`
	UpdatedByID *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
}

func (AIAgent) TableName() string { return "ai_agents" }
