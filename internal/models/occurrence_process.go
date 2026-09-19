package models

import "github.com/google/uuid"

// OccurrenceProcessMessageStage identifies which point of the SAC lifecycle a
// suggested message belongs to — the five stages the product spec asked for.
type OccurrenceProcessMessageStage string

const (
	OccurrenceProcessMessageRegistration OccurrenceProcessMessageStage = "registration"
	OccurrenceProcessMessageDocuments    OccurrenceProcessMessageStage = "documents"
	OccurrenceProcessMessageFollowUp     OccurrenceProcessMessageStage = "follow_up"
	OccurrenceProcessMessageForwarding   OccurrenceProcessMessageStage = "forwarding"
	OccurrenceProcessMessageClosing      OccurrenceProcessMessageStage = "closing"
)

// OccurrenceProcess is the "Processo/Motivo" a WhatHappened reason maps to:
// it does not replace OccurrenceCategory/OccurrenceWhatHappened (both stay
// independent flat dimensions, per their own doc comments) — it sits beside
// them and tells the agent/system how a case with that reason should be
// handled: internal guidance, restrictions, which of Occurrence's existing
// optional fields are required, SLA parameters, and routing.
//
// Exactly one ACTIVE process may exist per (OrganizationID, WhatHappenedID) —
// see the unique index below and the validation in CreateOccurrenceProcess/
// UpdateOccurrenceProcess (Task 4). ResolveOccurrenceProcess (Task 5) resolves
// a reason to a process with a plain .First() and depends on this invariant
// being real, not just documented.
//
// ResponseMinutes/ResolutionMinutes are deliberately *pointers*: nil means
// "no process-specific override, fall back to the existing priority-based
// OccurrenceSLAPolicy" — this is not a second SLA mechanism, just an optional
// second source for the same two integers getSLAPolicy already produces.
// Both use the same calendar-minutes semantics OccurrenceSLAPolicy's own
// four default rows already use — no business-hours calendar in this phase.
//
// RequiredFields lists keys from Occurrence's existing optional fields
// ("invoice_number", "product_description", "purchase_date", "sale_channel")
// that this process needs filled — not a dynamic custom-field system. Every
// field the five real seeded processes need already exists as a plain
// Occurrence column.
type OccurrenceProcess struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_process_what_happened,where:deleted_at IS NULL AND is_active = true" json:"organization_id"`

	Name        string `gorm:"size:150;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`

	CategoryID     *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`
	WhatHappenedID *uuid.UUID `gorm:"type:uuid;index;uniqueIndex:idx_occ_process_what_happened,where:deleted_at IS NULL AND is_active = true" json:"what_happened_id,omitempty"`

	// Guidance is for the agent only — never sent to the customer. Restrictions
	// is the "what NOT to say" list (§6 of the spec), same rule.
	Guidance     string `gorm:"type:text" json:"guidance"`
	Restrictions string `gorm:"type:text" json:"restrictions"`

	// EvidenceChecklist is a read-only informational list ("documentos a
	// solicitar ao cliente") — not real attachment/upload fields. A
	// document/evidence system is explicitly out of scope for this phase.
	EvidenceChecklist JSONBArray `gorm:"type:jsonb;default:'[]'" json:"evidence_checklist"`

	RequiredFields JSONBArray `gorm:"type:jsonb;default:'[]'" json:"required_fields"`

	ResponseMinutes   *int `json:"response_minutes,omitempty"`
	ResolutionMinutes *int `json:"resolution_minutes,omitempty"`

	DepartmentID *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`

	// No DB default: a `default:true` tag makes GORM overwrite an explicit false
	// with true on Create. Every creator sets IsActive explicitly.
	IsActive bool `gorm:"not null" json:"is_active"`
	Position int  `gorm:"not null;default:0" json:"position"`

	Category     *OccurrenceCategory     `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	WhatHappened *OccurrenceWhatHappened `gorm:"foreignKey:WhatHappenedID" json:"what_happened,omitempty"`
	Department   *Department             `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
}

func (OccurrenceProcess) TableName() string { return "occurrence_processes" }

// OccurrenceProcessMessage is one stage's suggested message template for a
// process. Content uses the same `[Nome]`/`[Protocolo]`/... bracket syntax as
// the real, already-validated process map this feature is seeded from.
type OccurrenceProcessMessage struct {
	BaseModel
	OrganizationID uuid.UUID                     `gorm:"type:uuid;index;not null" json:"organization_id"`
	ProcessID      uuid.UUID                     `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_process_msg_stage,where:deleted_at IS NULL" json:"process_id"`
	Stage          OccurrenceProcessMessageStage `gorm:"size:20;not null;uniqueIndex:idx_occ_process_msg_stage,where:deleted_at IS NULL" json:"stage"`
	Content        string                        `gorm:"type:text;not null" json:"content"`
	IsActive       bool                          `gorm:"not null" json:"is_active"`
}

func (OccurrenceProcessMessage) TableName() string { return "occurrence_process_messages" }
