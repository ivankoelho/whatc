package models

import (
	"time"

	"github.com/google/uuid"
)

// OccurrenceEventType identifies what a timeline entry records.
type OccurrenceEventType string

const (
	OccurrenceEventOpened       OccurrenceEventType = "opened"
	OccurrenceEventNote         OccurrenceEventType = "note"
	OccurrenceEventReply        OccurrenceEventType = "reply"
	OccurrenceEventStageChange  OccurrenceEventType = "stage_change"
	OccurrenceEventAssignment   OccurrenceEventType = "assignment"
	OccurrenceEventProtocolSent OccurrenceEventType = "protocol_sent"
	OccurrenceEventClosed       OccurrenceEventType = "closed"

	// Forwarding and collaboration trail (SAC to team hand-off).
	OccurrenceEventForwarded          OccurrenceEventType = "forwarded"
	OccurrenceEventParticipantAdded   OccurrenceEventType = "participant_added"
	OccurrenceEventParticipantRemoved OccurrenceEventType = "participant_removed"
	OccurrenceEventReopened           OccurrenceEventType = "reopened"
	OccurrenceEventScopeRegularized   OccurrenceEventType = "scope_regularized"

	OccurrenceEventProcessMessageUsed OccurrenceEventType = "process_message_used"
)

// OccurrencePriority ranks how urgent a case is.
type OccurrencePriority string

const (
	OccurrencePriorityLow    OccurrencePriority = "low"
	OccurrencePriorityNormal OccurrencePriority = "normal"
	OccurrencePriorityHigh   OccurrencePriority = "high"
	OccurrencePriorityUrgent OccurrencePriority = "urgent"
)

// OccurrenceStage is one configurable column of the org's pipeline.
type OccurrenceStage struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`

	// Partial unique index, deliberately NOT total: this is config, not a
	// customer-facing number. Deleting a stage must free its name for reuse —
	// unlike ProtocolNumber below, nobody has a stage name written down.
	Name      string `gorm:"size:100;not null" json:"name"`
	Color     string `gorm:"size:20;default:'#6b7280'" json:"color"`
	Position  int    `gorm:"not null;default:0" json:"position"`
	IsInitial bool   `gorm:"default:false" json:"is_initial"`
	IsClosing bool   `gorm:"default:false" json:"is_closing"`

	// TeamID scopes the stage to one team; nil means the organization-wide
	// pipeline every team shares. Name uniqueness is enforced by two partial
	// indexes (see coreIndexes), one per scope.
	TeamID *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"`
	// GeneralState ties the stage to one of the four general states, so the
	// sidebar/board/dashboard stay consistent whatever a team names its steps.
	GeneralState string `gorm:"size:20;default:'';index" json:"general_state"`
	// AllowedFrom, when non-empty, lists the stage IDs a case may come from to
	// enter this one. Empty means any stage of the same pipeline.
	AllowedFrom []string `gorm:"serializer:json;type:jsonb" json:"allowed_from"`
}

func (OccurrenceStage) TableName() string { return "occurrence_stages" }

// Occurrence is one tracked case for a contact. Its ProtocolNumber is the
// human-readable identifier the customer keeps and quotes back.
type Occurrence struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_org_protocol" json:"organization_id"`
	ContactID      uuid.UUID `gorm:"type:uuid;index;not null" json:"contact_id"`

	// Total unique index, deliberately NOT partial on soft delete: a deleted
	// protocol must never be reissued, because the customer still has the
	// number written down.
	ProtocolNumber string `gorm:"size:20;not null;uniqueIndex:idx_occ_org_protocol" json:"protocol_number"`

	Title       string             `gorm:"size:255;not null" json:"title"`
	Description string             `gorm:"type:text" json:"description"`
	StageID     uuid.UUID          `gorm:"type:uuid;index;not null" json:"stage_id"`
	Priority    OccurrencePriority `gorm:"size:20;not null;default:'normal'" json:"priority"`

	AssignedUserID *uuid.UUID `gorm:"type:uuid;index" json:"assigned_user_id,omitempty"`
	TeamID         *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"`
	OpenedByUserID uuid.UUID  `gorm:"type:uuid;not null" json:"opened_by_user_id"`

	OpenedAt time.Time  `gorm:"autoCreateTime" json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`

	// SourceTransferID records which attendance spawned this occurrence. It is
	// traceability only — the lifecycles stay independent, so closing the chat
	// attendance never closes the occurrence.
	SourceTransferID *uuid.UUID `gorm:"type:uuid;index" json:"source_transfer_id,omitempty"`

	// UnitID/DepartmentID default from the originating attendance's Team
	// (AgentTransfer.TeamID, via SourceTransferID) when the occurrence is
	// opened from a conversation; both are editable directly otherwise.
	UnitID       *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	DepartmentID *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`
	CategoryID   *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`

	// WhatHappenedID classifies the root cause, independent from CategoryID
	// (the type of case/action needed) — see OccurrenceWhatHappened's own
	// doc comment for why these are two separate dimensions.
	WhatHappenedID *uuid.UUID `gorm:"type:uuid;index" json:"what_happened_id,omitempty"`

	// ProcessID links the case to the Processo/Motivo that determines its
	// guidance, required fields and SLA parameters. Nil means the case was
	// opened without a resolvable process (e.g. WhatHappenedID left blank, or
	// no OccurrenceProcess configured for that reason yet) — the form and SLA
	// then behave exactly as before this feature existed.
	ProcessID *uuid.UUID `gorm:"type:uuid;index" json:"process_id,omitempty"`

	// Source documents where this case originated. Whatomate has one channel
	// today (WhatsApp), so this is a placeholder for a future channel, not
	// active logic — "manual" when there is no SourceTransferID.
	Source string `gorm:"size:20;not null;default:'whatsapp'" json:"source"`

	// Sale context, filled manually by the agent in this phase (no ERP
	// lookup yet). All optional — the case can be opened without a purchase
	// on file at all.
	SaleChannel        string     `gorm:"size:30" json:"sale_channel,omitempty"`
	InvoiceNumber      string     `gorm:"size:255" json:"invoice_number,omitempty"`
	PurchaseDate       *time.Time `json:"purchase_date,omitempty"`
	ProductDescription string     `gorm:"size:255" json:"product_description,omitempty"`

	// SLA embeds the same struct AgentTransfer already uses for chat SLA
	// (internal/models/chatbot.go:294) — response/resolution deadline, breach
	// flag and timestamp. SLA.FirstResponseAt doubles here as Occurrence's own
	// "first public reply" timestamp (no separate top-level field: that would
	// collide with this embedded one on the same first_response_at column).
	// FirstResponseByID has no equivalent on SLATracking, so it stays a
	// top-level field. Both SLA.FirstResponseAt and FirstResponseByID are
	// Occurrence-only: they are set exclusively by the "reply" event (Task 7),
	// never by an internal note — that distinction is the whole point of
	// first-response SLA.
	SLA               SLATracking `gorm:"embedded"`
	FirstResponseByID *uuid.UUID  `gorm:"type:uuid" json:"first_response_by_id,omitempty"`

	// GeneralState mirrors the current stage's general state (denormalized so
	// counters and boards filter without a join). Kept in sync wherever StageID
	// changes. ReopenedCount counts resolved -> open transitions.
	GeneralState  string `gorm:"size:20;default:'';index" json:"general_state"`
	ReopenedCount int    `gorm:"not null;default:0" json:"reopened_count"`

	// OrderRef is the commercial order the request is about (free text from the
	// requester; never resolved against an ERP here).
	OrderRef string `gorm:"size:60;index" json:"order_ref,omitempty"`

	FirstResponseBy *User `gorm:"foreignKey:FirstResponseByID" json:"first_response_by,omitempty"`

	// Relations
	Contact      *Contact                `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	Stage        *OccurrenceStage        `gorm:"foreignKey:StageID" json:"stage,omitempty"`
	AssignedUser *User                   `gorm:"foreignKey:AssignedUserID" json:"assigned_user,omitempty"`
	Unit         *Unit                   `gorm:"foreignKey:UnitID" json:"unit,omitempty"`
	Department   *Department             `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Category     *OccurrenceCategory     `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	WhatHappened *OccurrenceWhatHappened `gorm:"foreignKey:WhatHappenedID" json:"what_happened,omitempty"`
	Process      *OccurrenceProcess      `gorm:"foreignKey:ProcessID" json:"process,omitempty"`
}

func (Occurrence) TableName() string { return "occurrences" }

// OccurrenceEvent is one entry on an occurrence's timeline. Manual notes and
// automatic events share this table on purpose: two tables drift, and a
// timeline built from two queries has to interleave them anyway.
type OccurrenceEvent struct {
	BaseModel
	OrganizationID uuid.UUID           `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID           `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	Type           OccurrenceEventType `gorm:"size:30;not null" json:"type"`
	Content        string              `gorm:"type:text" json:"content"`
	Metadata       JSONB               `gorm:"type:jsonb;default:'{}'" json:"metadata"`
	CreatedByID    *uuid.UUID          `gorm:"type:uuid" json:"created_by_id,omitempty"` // nil = system

	CreatedBy *User `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
}

func (OccurrenceEvent) TableName() string { return "occurrence_events" }

// OccurrenceCounter holds the per-org, per-year protocol sequence. It does not
// embed BaseModel: the natural key is (organization_id, year), and a uuid PK
// would invite a second row for the same pair.
type OccurrenceCounter struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"organization_id"`
	Year           int       `gorm:"primaryKey" json:"year"`
	LastSeq        int       `gorm:"not null;default:0" json:"last_seq"`
}

func (OccurrenceCounter) TableName() string { return "occurrence_counters" }

// General states: the four categories every pipeline stage maps into.
const (
	GeneralStateNew             = "new"
	GeneralStateInProgress      = "in_progress"
	GeneralStateWaitingCustomer = "waiting_customer"
	GeneralStateResolved        = "resolved"
)

// IsValidGeneralState reports whether s is one of the four general states.
func IsValidGeneralState(s string) bool {
	switch s {
	case GeneralStateNew, GeneralStateInProgress, GeneralStateWaitingCustomer, GeneralStateResolved:
		return true
	}
	return false
}

// OccurrenceParticipant grants one user read access to a case outside their
// own team/unit scope (e.g. SAC following a case forwarded to Logistics).
type OccurrenceParticipant struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_occ_participant" json:"occurrence_id"`
	UserID         uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_occ_participant;index" json:"user_id"`
	AddedByID      *uuid.UUID `gorm:"type:uuid" json:"added_by_id,omitempty"`
	// Capabilities beyond viewing (see OccurrenceCap*). Empty = read only.
	Capabilities []string `gorm:"serializer:json;type:jsonb" json:"capabilities"`
}

func (OccurrenceParticipant) TableName() string { return "occurrence_participants" }

// EffectiveState is the stage's general state, deriving it from the legacy
// flags for rows that predate the column.
func (s OccurrenceStage) EffectiveState() string {
	if IsValidGeneralState(s.GeneralState) {
		return s.GeneralState
	}
	switch {
	case s.IsClosing:
		return GeneralStateResolved
	case s.IsInitial:
		return GeneralStateNew
	}
	return GeneralStateInProgress
}

// Capabilities a participant may be granted on one case. Viewing is implicit in
// being a participant; nothing else is, so each right is granted on its own.
const (
	OccurrenceCapComment      = "comment"       // internal notes
	OccurrenceCapChangeStatus = "change_status" // move between stages (not into resolved)
	OccurrenceCapClaim        = "claim"         // take an unassigned case
	OccurrenceCapReassign     = "reassign"      // hand the case to another agent
	OccurrenceCapResolve      = "resolve"       // conclude or reopen
)

// IsValidOccurrenceCapability reports whether c is a grantable capability.
func IsValidOccurrenceCapability(c string) bool {
	switch c {
	case OccurrenceCapComment, OccurrenceCapChangeStatus, OccurrenceCapClaim, OccurrenceCapReassign, OccurrenceCapResolve:
		return true
	}
	return false
}

// OccurrenceChannelConfig says which WhatsApp account talks to customers about
// a case: the unit's own when configured, otherwise the organization-wide one
// (UnitID nil). The account is referenced by name, like everywhere else.
type OccurrenceChannelConfig struct {
	BaseModel
	OrganizationID  uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	UnitID          *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	Purpose         string     `gorm:"size:30;not null;default:'logistics'" json:"purpose"`
	WhatsAppAccount string     `gorm:"size:100;not null" json:"whatsapp_account"`
}

func (OccurrenceChannelConfig) TableName() string { return "occurrence_channel_configs" }

// OccurrenceNotification records a customer notification PREPARED for a case.
// Nothing sends from it yet: it fixes the account, the eligibility verdict and
// an idempotency key now, so the dispatcher that comes later cannot duplicate
// or guess. Status: "prepared" (account resolved) or "skipped" (with Reason).
type OccurrenceNotification struct {
	BaseModel
	OrganizationID  uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID    uuid.UUID `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	Kind            string    `gorm:"size:30;not null" json:"kind"`
	WhatsAppAccount string    `gorm:"size:100" json:"whatsapp_account"`
	IdempotencyKey  string    `gorm:"size:120;not null;uniqueIndex" json:"idempotency_key"`
	Status          string    `gorm:"size:20;not null" json:"status"`
	Reason          string    `gorm:"size:200" json:"reason"`
	Eligible        bool      `gorm:"not null;default:false" json:"eligible"`
}

func (OccurrenceNotification) TableName() string { return "occurrence_notifications" }
