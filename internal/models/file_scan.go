package models

import (
	"time"

	"github.com/google/uuid"
)

// Antimalware verdicts. Only ScanClean releases a file to a person: every other state, and the
// absence of a row, keeps it closed (fail-closed).
const (
	ScanPending  = "pending"  // waiting for (or in) a scan
	ScanClean    = "clean"    // the engine found nothing
	ScanInfected = "infected" // detected: quarantined, never served
	ScanError    = "error"    // engine unreachable, timed out or refused: retried with back-off
	ScanSkipped  = "skipped"  // deliberately not scanned; not released either
)

// What a scan row points at.
const (
	ScanSourceOccurrenceFile     = "occurrence_file"
	ScanSourceChatAttachment     = "chat_attachment"
	ScanSourceOccurrenceDocument = "occurrence_document"
	ScanSourceMessageMedia       = "message_media"
	ScanSourceCampaignMedia      = "campaign_media" // header media of a bulk campaign (local preview copy)
	ScanSourceBranding           = "branding"       // system logo / login background (public, system-owned: org uuid.Nil)
	ScanSourceIVRAudio           = "ivr_audio"      // IVR / hold-music audio (transcoded by us from a scanned upload)
)

// FileScan is the antimalware state of one stored file. There is exactly one row per
// (source, resource); the delivery paths ask FileScan, never the file's own table, whether a file may
// be served. SHA256 and ScannerVersion say what was scanned and with which signatures, so a result is
// only ever reused for identical content (same organization, same hash) and a fresh enough database.
type FileScan struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	Source         string     `gorm:"size:24;not null;uniqueIndex:idx_file_scan_resource,priority:1" json:"source"`
	ResourceID     uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_file_scan_resource,priority:2" json:"resource_id"`
	StoragePath    string     `gorm:"size:500;not null" json:"-"`
	SHA256         string     `gorm:"size:64;index" json:"-"`
	SizeBytes      int64      `json:"size_bytes"`
	Status         string     `gorm:"size:12;not null;index:idx_file_scan_queue,priority:1" json:"status"`
	Signature      string     `gorm:"size:200" json:"signature,omitempty"`
	ScannerVersion string     `gorm:"size:200" json:"scanner_version,omitempty"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt  *time.Time `gorm:"index:idx_file_scan_queue,priority:2" json:"next_attempt_at,omitempty"`
	LastError      string     `gorm:"size:300" json:"last_error,omitempty"`
	ScannedAt      *time.Time `json:"scanned_at,omitempty"`
	QuarantinedAt  *time.Time `json:"quarantined_at,omitempty"`
	AlertedAt      *time.Time `json:"alerted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (FileScan) TableName() string { return "file_scans" }
