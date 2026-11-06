package models

import (
	"time"

	"github.com/google/uuid"
)

// Retention of FILES (not of records): what may be taken off disk once it is old enough, per organization
// and per category. The history of a protocol, its personal-data-minimised record and the audit trail are
// NOT governed by these rules; a file can leave while the record that says it existed stays.

// Retention categories that can be configured. Each maps to one kind of stored file.
const (
	RetentionOccurrenceFiles    = "occurrence_files"    // documents of a protocol (incl. copies imported from Nextcloud)
	RetentionOccurrenceInvoices = "occurrence_invoices" // invoice/coupon attachments of a protocol
	RetentionChatAttachments    = "chat_attachments"    // Chat Interno attachments (incl. case discussions)
	RetentionWhatsAppMedia      = "whatsapp_media"      // media of WhatsApp messages stored by WhatC
	RetentionCampaignMedia      = "campaign_media"      // local preview copy of a campaign header
)

// RetentionCategories in display order.
var RetentionCategories = []string{
	RetentionOccurrenceFiles, RetentionOccurrenceInvoices, RetentionChatAttachments, RetentionWhatsAppMedia, RetentionCampaignMedia,
}

// Scopes a legal hold can be applied to.
const (
	RetentionScopeOccurrence = "occurrence" // the protocol: its documents, invoices and case discussion
	RetentionScopeContact    = "contact"    // the contact: WhatsApp media and every protocol of that contact
)

// RetentionPolicy is one organization's rule for one category. There is no default term: a category with
// no row, or with Enabled false, never has anything eligible. Terms are chosen by the organization (and
// validated by whoever advises it legally); nothing here encodes a legal period.
type RetentionPolicy struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_retention_policy,priority:1" json:"organization_id"`
	Category       string     `gorm:"size:40;not null;uniqueIndex:idx_retention_policy,priority:2" json:"category"`
	KeepMonths     int        `gorm:"not null;default:0" json:"keep_months"`
	Enabled        bool       `gorm:"not null;default:false" json:"enabled"`
	UpdatedByID    *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (RetentionPolicy) TableName() string { return "retention_policies" }

// RetentionHold suspends expurgation (a "legal hold") for everything tied to a protocol or a contact. A
// hold is never deleted: it is revoked, with who and why, and its history is kept.
type RetentionHold struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;index:idx_retention_hold_scope,priority:1" json:"organization_id"`
	ScopeType      string     `gorm:"size:20;not null;index:idx_retention_hold_scope,priority:2" json:"scope_type"`
	ScopeID        uuid.UUID  `gorm:"type:uuid;not null;index:idx_retention_hold_scope,priority:3" json:"scope_id"`
	ScopeLabel     string     `gorm:"size:200" json:"scope_label"` // protocol number / contact name, as it was when the hold was placed
	Reason         string     `gorm:"type:text;not null" json:"reason"`
	CreatedByID    uuid.UUID  `gorm:"type:uuid;not null" json:"created_by_id"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	RevokedByID    *uuid.UUID `gorm:"type:uuid" json:"revoked_by_id,omitempty"`
	RevokeReason   string     `gorm:"type:text" json:"revoke_reason,omitempty"`
}

func (RetentionHold) TableName() string { return "retention_holds" }

// RetentionHoldEvent is the append-only history of a hold (placed, revoked).
type RetentionHoldEvent struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index" json:"organization_id"`
	HoldID         uuid.UUID `gorm:"type:uuid;not null;index" json:"hold_id"`
	Action         string    `gorm:"size:20;not null" json:"action"` // placed | revoked
	ActorID        uuid.UUID `gorm:"type:uuid;not null" json:"actor_id"`
	Detail         string    `gorm:"type:text" json:"detail"`
	CreatedAt      time.Time `json:"created_at"`
}

func (RetentionHoldEvent) TableName() string { return "retention_hold_events" }

// Purge engine ledger. The engine is prepared but switched off in this release: these rows only ever
// record planning. They exist so that execution, when it is enabled, is idempotent and resumable.
const (
	RunPlanned  = "planned"
	RunRunning  = "running"
	RunPaused   = "paused"
	RunFinished = "finished"
	RunFailed   = "failed"

	ItemPlanned    = "planned"
	ItemDeleting   = "deleting"
	ItemRecordGone = "record_deleted" // the database record is gone, the stored file may remain: retried
	ItemDone       = "done"
	ItemSkipped    = "skipped" // became ineligible (hold, shared reference, policy off, no longer due)
	ItemFailed     = "failed"
)

// RetentionRun is one pass of the engine over one category of one organization.
type RetentionRun struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;index" json:"organization_id"`
	Category       string     `gorm:"size:40;not null" json:"category"`
	Mode           string     `gorm:"size:10;not null" json:"mode"` // plan | execute
	Status         string     `gorm:"size:12;not null" json:"status"`
	Counts         JSONB      `gorm:"type:jsonb" json:"counts"`
	LastError      string     `gorm:"size:300" json:"last_error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func (RetentionRun) TableName() string { return "retention_runs" }

// RetentionItem is one file the engine has planned to remove. (organization, source, resource) is unique,
// so planning twice never queues a file twice.
type RetentionItem struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_retention_item,priority:1" json:"organization_id"`
	Category       string     `gorm:"size:40;not null;uniqueIndex:idx_retention_item,priority:2" json:"category"`
	ResourceID     uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_retention_item,priority:3" json:"resource_id"`
	RunID          uuid.UUID  `gorm:"type:uuid;not null;index" json:"run_id"`
	StoragePath    string     `gorm:"size:500" json:"-"`
	Status         string     `gorm:"size:16;not null;index:idx_retention_item_queue,priority:1" json:"status"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt  *time.Time `gorm:"index:idx_retention_item_queue,priority:2" json:"next_attempt_at,omitempty"`
	LastError      string     `gorm:"size:300" json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (RetentionItem) TableName() string { return "retention_items" }
