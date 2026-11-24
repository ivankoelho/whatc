package models

import "github.com/google/uuid"

// ContactAdReferral is where a customer's conversation came from when it began with a click on a Meta
// ad ("click to WhatsApp"). Meta delivers it in the first message's "referral" object. A contact that
// clicks again later gets one more row; the first one is the origin the CRM shows (first touch), and every
// row stays as the history of what brought the customer back.
//
// Only what Meta sends about the ad is kept, never the customer's text. MessageID makes recording
// idempotent: Meta retries webhooks, and one message is one row.
type ContactAdReferral struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:uq_ad_referral_message" json:"organization_id"`
	ContactID      uuid.UUID `gorm:"type:uuid;index;not null" json:"contact_id"`
	// MessageID is the WhatsApp id of the message that carried the referral.
	MessageID string `gorm:"size:255;not null;uniqueIndex:uq_ad_referral_message" json:"message_id"`

	SourceID   string `gorm:"size:100" json:"source_id"`   // the ad (or post) id
	SourceType string `gorm:"size:20" json:"source_type"`  // "ad" or "post"
	SourceURL  string `gorm:"size:1000" json:"source_url"` // the ad's URL
	Headline   string `gorm:"size:500" json:"headline"`
	Body       string `gorm:"type:text" json:"body"`
	MediaType  string `gorm:"size:20" json:"media_type"`
	// CtwaClid is Meta's click id for this conversation: what a later conversion report to Meta needs.
	CtwaClid string `gorm:"size:500" json:"ctwa_clid"`
}

func (ContactAdReferral) TableName() string { return "contact_ad_referrals" }
