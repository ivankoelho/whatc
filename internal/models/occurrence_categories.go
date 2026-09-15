package models

import "github.com/google/uuid"

// OccurrenceCategory is a category or subcategory an occurrence is classified
// under. ParentID nil means a top-level category; set means a subcategory —
// one level of nesting only in this phase (a subcategory of a subcategory is
// rejected at the handler, not the schema). This is what makes an automation
// rule engine possible later (unit+department+category → team/priority/SLA)
// without a further migration.
type OccurrenceCategory struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string     `gorm:"size:100;not null" json:"name"`
	ParentID       *uuid.UUID `gorm:"type:uuid;index" json:"parent_id,omitempty"`
	Position       int        `gorm:"not null;default:0" json:"position"`
	IsActive       bool       `gorm:"default:true" json:"is_active"`

	Parent *OccurrenceCategory `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
}

func (OccurrenceCategory) TableName() string { return "occurrence_categories" }
