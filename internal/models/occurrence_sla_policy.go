package models

import "github.com/google/uuid"

// OccurrenceSLAPolicy is one response/resolution target for a priority level.
// DepartmentID/UnitID/CategoryID exist so a future phase can scope SLA more
// narrowly than "by priority, organisation-wide" without a schema change —
// this phase only ever creates and matches rows where all three are nil.
type OccurrenceSLAPolicy struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_sla_org_priority_scope,where:department_id IS NULL AND unit_id IS NULL AND category_id IS NULL AND deleted_at IS NULL" json:"organization_id"`
	DepartmentID   *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`
	UnitID         *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	CategoryID     *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`

	Priority          OccurrencePriority `gorm:"size:20;not null;uniqueIndex:idx_occ_sla_org_priority_scope,where:department_id IS NULL AND unit_id IS NULL AND category_id IS NULL AND deleted_at IS NULL" json:"priority"`
	ResponseMinutes   int                `gorm:"not null" json:"response_minutes"`
	ResolutionMinutes int                `gorm:"not null" json:"resolution_minutes"`
}

func (OccurrenceSLAPolicy) TableName() string { return "occurrence_sla_policies" }
