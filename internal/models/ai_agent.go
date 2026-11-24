package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// Types of a QualificationField.
const (
	QualFieldText   = "text"   // free text the customer stated
	QualFieldNumber = "number" // a number the customer stated
	QualFieldChoice = "choice" // one of Options
)

// QualificationField is one thing the organization wants the agent to find out from the customer (the area of
// law, the urgency, the city...). An agent with no fields keeps the original product qualification.
type QualificationField struct {
	Key         string   `json:"key"`   // stable slug: a-z, 0-9, _
	Label       string   `json:"label"` // what people read in the CRM
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"` // tells the agent (and the extraction) what to look for
	Options     []string `json:"options,omitempty"`     // only for Type == choice
	Required    bool     `json:"required,omitempty"`    // the agent insists on it before handing over
}

// QualificationFields is the agent's list, stored as jsonb.
type QualificationFields []QualificationField

func (q QualificationFields) Value() (driver.Value, error) {
	if q == nil {
		return nil, nil
	}
	return json.Marshal(q)
}

func (q *QualificationFields) Scan(value any) error {
	if value == nil {
		*q = nil
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}
	return json.Unmarshal(b, q)
}

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

	// QualificationFields is what this agent qualifies the customer on. Empty = the original product
	// qualification (product, category, quantity, area, environment, purchase intent).
	QualificationFields QualificationFields `gorm:"column:qualification_fields;type:jsonb" json:"qualification_fields"`

	CreatedByID *uuid.UUID `gorm:"type:uuid" json:"created_by_id,omitempty"`
	UpdatedByID *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
}

func (AIAgent) TableName() string { return "ai_agents" }
