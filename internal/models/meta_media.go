package models

import (
	"time"

	"github.com/google/uuid"
)

// MetaMedia remembers the WhatsApp media ids that THIS server uploaded to Meta after a clean antimalware
// scan. A media id typed or sent by a person (a campaign's header, a template send's "pre-uploaded" media)
// is only accepted when it is in this table for the organization: an id that came from anywhere else points
// at content WhatC never saw, hosted on Meta's side, where it has no control.
type MetaMedia struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_meta_media_org_id,priority:1" json:"organization_id"`
	MediaID        string    `gorm:"size:200;not null;uniqueIndex:idx_meta_media_org_id,priority:2" json:"media_id"`
	Account        string    `gorm:"size:100" json:"account"`
	Source         string    `gorm:"size:40" json:"source"` // which path uploaded it
	SHA256         string    `gorm:"size:64" json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

func (MetaMedia) TableName() string { return "meta_media_uploads" }
