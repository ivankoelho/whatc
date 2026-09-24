package models

import "github.com/google/uuid"

// Unit is a physical location (store, branch, headquarters) an organisation
// operates. The schema is wider than the Fase 3 UI exposes on purpose —
// Address/Phone/BusinessHoursID have no screen yet, so a later request for
// them costs a UI change, not a second migration.
type Unit struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL" json:"organization_id"`
	Name           string    `gorm:"size:255;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL" json:"name"`
	// Code is the legacy store code, kept in the table but no longer edited:
	// CNPJ replaced it as the unit's identifier for integrations.
	Code            string     `gorm:"size:50" json:"code,omitempty"`
	CNPJ            string     `gorm:"column:cnpj;size:14" json:"cnpj,omitempty"` // digits only
	Type            string     `gorm:"size:50" json:"type,omitempty"`
	Active          bool       `gorm:"default:true" json:"active"`
	Address         string     `gorm:"size:500" json:"address,omitempty"`
	Phone           string     `gorm:"size:50" json:"phone,omitempty"`
	BusinessHoursID *uuid.UUID `gorm:"type:uuid" json:"business_hours_id,omitempty"`
	Metadata        JSONB      `gorm:"type:jsonb;default:'{}'" json:"metadata"`
}

func (Unit) TableName() string { return "units" }

// Department is a functional sector (Logística, ADM, TI...) shared across
// every Unit — one row per sector, not one per (unit, sector) combination.
type Department struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_departments_org_name,where:deleted_at IS NULL" json:"organization_id"`
	Name           string    `gorm:"size:255;not null;uniqueIndex:idx_departments_org_name,where:deleted_at IS NULL" json:"name"`
	Active         bool      `gorm:"default:true" json:"active"`
}

func (Department) TableName() string { return "departments" }
