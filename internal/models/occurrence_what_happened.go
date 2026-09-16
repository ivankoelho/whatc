package models

import "github.com/google/uuid"

// OccurrenceWhatHappened classifies the root cause of an occurrence — e.g.
// "Atraso na Entrega", "Produto com Avaria" — independent from Category
// (which classifies the type of case/action needed, e.g. "Troca de
// Produto"). Two orthogonal dimensions on purpose: a case can be a "Troca de
// Produto" whose cause was "Produto com Avaria", or the same cause under a
// "Devolução com Estorno". Kept as its own flat table (no subcategory
// nesting, unlike OccurrenceCategory) since none was asked for and a second
// self-referencing tree would duplicate resolveCategoryParent's rules for a
// dimension that doesn't need them.
type OccurrenceWhatHappened struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string    `gorm:"size:100;not null" json:"name"`
	Position       int       `gorm:"not null;default:0" json:"position"`
	IsActive       bool      `gorm:"default:true" json:"is_active"`
}

func (OccurrenceWhatHappened) TableName() string { return "occurrence_what_happened" }
