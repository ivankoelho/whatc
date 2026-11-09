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
	OccurrenceEventFileAdded          OccurrenceEventType = "file_added"
	OccurrenceEventFileRemoved        OccurrenceEventType = "file_removed"
	// A customer communication milestone: Content is the notification status.
	OccurrenceEventCustomerNotice OccurrenceEventType = "customer_notification"

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
	// Role says why the person is on the case: "support" (apoio: asked to help) or "follower" (acompanha).
	// Neither is the requester or the main assignee, and neither grants anything by itself: access is the
	// capabilities above, and never the WhatsApp conversation or restricted files.
	Role string `gorm:"size:12;not null;default:'follower'" json:"role"`
	// DepartmentID is the department the person was picked from (informational; the link is the user).
	DepartmentID *uuid.UUID `gorm:"type:uuid" json:"department_id,omitempty"`
	// RemovedByID: who ended the link. The row is soft-deleted (deleted_at is when) and kept as history.
	RemovedByID *uuid.UUID `gorm:"type:uuid" json:"removed_by_id,omitempty"`
}

// Participant roles.
const (
	ParticipantRoleSupport  = "support"
	ParticipantRoleFollower = "follower"
)

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

	// Automation. A configuration applies to the most specific scope that has
	// one: team, then unit, then the organization (see resolveAutomation). A
	// more specific row prevails even when it is inactive, so a unit can switch
	// the automation off without falling back to the organization's.
	TeamID *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"`
	// Event is the business event that triggers it ("forwarded").
	Event string `gorm:"size:30;not null;default:'forwarded'" json:"event"`
	// TemplateName/Language is the approved template sent for the event;
	// FallbackTemplateName is used instead when the case has no order number.
	TemplateName         string `gorm:"size:255" json:"template_name"`
	TemplateLanguage     string `gorm:"size:10" json:"template_language"`
	FallbackTemplateName string `gorm:"size:255" json:"fallback_template_name"`
	IsActive             bool   `gorm:"not null;default:true" json:"is_active"`
	// Retry policy: attempts include the first one; backoff doubles each time.
	MaxAttempts      int `gorm:"not null;default:3" json:"max_attempts"`
	RetryBackoffSecs int `gorm:"not null;default:60" json:"retry_backoff_secs"`
	// OnFailure: "manual_retry" keeps a failed send visible for an operator to
	// reprocess; "cancel" closes it as cancelled once attempts are exhausted.
	OnFailure string `gorm:"size:20;not null;default:'manual_retry'" json:"on_failure"`
}

// Notification (customer communication) states. Acceptance by the provider is
// NOT delivery: accepted -> delivered -> read only advance on provider
// evidence (status webhooks).
const (
	NotificationPrepared   = "prepared"   // intent recorded, automation not active (yet)
	NotificationPending    = "pending"    // ready; waiting for a worker
	NotificationProcessing = "processing" // claimed by a worker, request possibly in flight
	NotificationAccepted   = "accepted"   // the provider accepted the request (has a message id)
	NotificationDelivered  = "delivered"
	NotificationRead       = "read"
	NotificationFailed     = "failed"    // definitively not sent
	NotificationUncertain  = "uncertain" // may or may not have been accepted: never resent blindly
	NotificationCancelled  = "cancelled"
	NotificationSkipped    = "skipped" // not eligible; Reason says why
)

func (OccurrenceChannelConfig) TableName() string { return "occurrence_channel_configs" }

// OccurrenceNotification is one customer communication triggered by a case
// event. The row is the durable INTENT: it is written in the same transaction
// as the case, survives a dead worker, and carries a stable idempotency key
// ("occ:<id>:<kind>") so a repeated event cannot create a second one.
//
// Account, template and retry policy are SNAPSHOT at creation, so editing the
// configuration while a send is in flight cannot change what that send does.
// ProviderMessageID is the id the provider returned; it links the row to the
// webhooks that later say delivered/read.
type OccurrenceNotification struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID  `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	ConfigID       *uuid.UUID `gorm:"type:uuid" json:"config_id,omitempty"`
	Kind           string     `gorm:"size:30;not null" json:"kind"`
	IdempotencyKey string     `gorm:"size:120;not null;uniqueIndex" json:"idempotency_key"`

	Status   string `gorm:"size:20;not null;index" json:"status"`
	Reason   string `gorm:"size:200" json:"reason"`
	Eligible bool   `gorm:"not null;default:false" json:"eligible"`

	WhatsAppAccount      string `gorm:"size:100" json:"whatsapp_account"`
	TemplateName         string `gorm:"size:255" json:"template_name"`
	TemplateLanguage     string `gorm:"size:10" json:"template_language"`
	FallbackTemplateName string `gorm:"size:255" json:"fallback_template_name,omitempty"`
	MaxAttempts          int    `gorm:"not null;default:3" json:"max_attempts"`
	RetryBackoffSecs     int    `gorm:"not null;default:60" json:"retry_backoff_secs"`
	OnFailure            string `gorm:"size:20;not null;default:'manual_retry'" json:"on_failure"`

	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	LockedAt      *time.Time `json:"locked_at,omitempty"`
	LockedBy      string     `gorm:"size:80" json:"-"`
	// RetryRequestedByID is set when a person asked for the next attempt; the
	// attempt records it and clears it.
	RetryRequestedByID *uuid.UUID `gorm:"type:uuid" json:"-"`

	ProviderMessageID string     `gorm:"size:200;index" json:"provider_message_id,omitempty"`
	MessageID         *uuid.UUID `gorm:"type:uuid" json:"message_id,omitempty"`
	TemplateCategory  string     `gorm:"size:50" json:"template_category,omitempty"`
	LastError         string     `gorm:"size:500" json:"last_error,omitempty"`
	// Why this customer may be contacted, recorded at send time: Purpose is the
	// reason for the communication; ConsentBasis how the relationship exists
	// (customer_initiated = the customer wrote to us); ConsentEvidence is the id of
	// the inbound message that shows it.
	Purpose         string     `gorm:"size:40;not null;default:'order_service_update'" json:"purpose"`
	ConsentBasis    string     `gorm:"size:40" json:"consent_basis,omitempty"`
	ConsentEvidence string     `gorm:"size:100" json:"consent_evidence,omitempty"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
}

func (OccurrenceNotification) TableName() string { return "occurrence_notifications" }

// OccurrenceNotificationAttempt is one try at sending, kept for the history
// shown on the case and for reconciling an ambiguous result.
type OccurrenceNotificationAttempt struct {
	BaseModel
	OrganizationID    uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	NotificationID    uuid.UUID  `gorm:"type:uuid;index;not null" json:"notification_id"`
	Number            int        `gorm:"not null" json:"number"`
	WhatsAppAccount   string     `gorm:"size:100" json:"whatsapp_account"`
	TemplateName      string     `gorm:"size:255" json:"template_name"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	Outcome           string     `gorm:"size:20" json:"outcome"` // accepted | failed | uncertain | skipped
	ProviderMessageID string     `gorm:"size:200" json:"provider_message_id,omitempty"`
	Error             string     `gorm:"size:500" json:"error,omitempty"`
	TriggeredByID     *uuid.UUID `gorm:"type:uuid" json:"triggered_by_id,omitempty"` // set for manual reprocessing
}

func (OccurrenceNotificationAttempt) TableName() string { return "occurrence_notification_attempts" }

// OccurrenceFile is a general document of a case (Word, Excel, PDF, image, NF-e XML...), kept
// apart from the invoice/coupon records. Removing it is a soft delete (the row stays for the
// audit trail; the stored file is erased). Restricted files can be downloaded only by those who
// work the case and by whoever sent them; the rest of the people who can open the case see them listed.
type OccurrenceFile struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	UploadedByID   uuid.UUID `gorm:"type:uuid;not null" json:"uploaded_by_id"`
	Name           string    `gorm:"size:255;not null" json:"name"`
	Mime           string    `gorm:"size:100;not null" json:"mime"`
	Kind           string    `gorm:"size:10;not null" json:"kind"`
	SizeBytes      int64     `gorm:"not null" json:"size_bytes"`
	SHA256         string    `gorm:"size:64;index" json:"-"`
	StoragePath    string    `gorm:"size:400;not null" json:"-"`
	Restricted     bool      `gorm:"not null" json:"restricted"` // set explicitly on every insert (a gorm default would swallow false)
}

func (OccurrenceFile) TableName() string { return "occurrence_files" }

// OccurrenceTransfer is one move of the main responsibility for a case between teams (departments). The case
// keeps its protocol, files, discussion and history: only who is responsible changes. A nil FromTeamID means the
// case came from its requester (opened with a team), and a nil ToTeamID means it went back to the requester.
type OccurrenceTransfer struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	Kind           string    `gorm:"size:10;not null" json:"kind"` // forward | return
	FromTeamID     *uuid.UUID `gorm:"type:uuid" json:"from_team_id,omitempty"`
	ToTeamID       *uuid.UUID `gorm:"type:uuid" json:"to_team_id,omitempty"`
	FromUserID     *uuid.UUID `gorm:"type:uuid" json:"from_user_id,omitempty"` // who held the case before
	ToUserID       *uuid.UUID `gorm:"type:uuid" json:"to_user_id,omitempty"`   // who holds it after (may be nobody yet)
	ByUserID       uuid.UUID `gorm:"type:uuid;not null" json:"by_user_id"`
	Reason         string    `gorm:"type:text" json:"reason"`
}

func (OccurrenceTransfer) TableName() string { return "occurrence_transfers" }

const (
	TransferKindForward = "forward"
	TransferKindReturn  = "return"

	OccurrenceEventReturned OccurrenceEventType = "returned"
)
