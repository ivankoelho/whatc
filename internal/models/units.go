package models

import "github.com/google/uuid"

// Unit is a physical location (store, branch, headquarters) an organisation
// operates. The schema is wider than the Fase 3 UI exposes on purpose —
// Address/Phone/BusinessHoursID have no screen yet, so a later request for
// them costs a UI change, not a second migration.
type Unit struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL;uniqueIndex:idx_units_org_cnpj,where:cnpj <> '' AND deleted_at IS NULL;uniqueIndex:idx_units_org_xprocess_loja,where:xprocess_cod_empresa IS NOT NULL AND deleted_at IS NULL" json:"organization_id"`
	Name           string    `gorm:"size:255;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL" json:"name"`
	// Code is the legacy store code, kept in the table but no longer edited:
	// CNPJ replaced it as the unit's identifier for integrations.
	Code            string     `gorm:"size:50" json:"code,omitempty"`
	CNPJ            string     `gorm:"column:cnpj;size:14;uniqueIndex:idx_units_org_cnpj,where:cnpj <> '' AND deleted_at IS NULL" json:"cnpj,omitempty"` // digits only
	Type            string     `gorm:"size:50" json:"type,omitempty"`
	Active          bool       `gorm:"default:true" json:"active"`
	Address         string     `gorm:"size:500" json:"address,omitempty"`
	Phone           string     `gorm:"size:50" json:"phone,omitempty"`
	BusinessHoursID *uuid.UUID `gorm:"type:uuid" json:"business_hours_id,omitempty"`
	Metadata        JSONB      `gorm:"type:jsonb;default:'{}'" json:"metadata"`

	// XProcessCodEmpresa is the X2 store (cod_empresa) this unit stands for. It is set
	// ONLY by an administrator choosing the store: linking a unit (PUT
	// /api/units/{id}/xprocess-loja) or importing the store as a new unit (POST
	// /api/units/xprocess-import). Never inferred from names or CNPJ, and it is unique
	// per organization. column: is
	// explicit because GORM would split "XProcess" into x_process_cod_empresa.
	XProcessCodEmpresa *string `gorm:"column:xprocess_cod_empresa;size:20;uniqueIndex:idx_units_org_xprocess_loja,where:xprocess_cod_empresa IS NOT NULL AND deleted_at IS NULL" json:"xprocess_cod_empresa,omitempty"`
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
