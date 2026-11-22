package models

import (
	"time"

	"github.com/google/uuid"
)

// NextcloudCalendar is the calendar an organization's AI agent books meetings in. The server address is
// the organization's Nextcloud (NextcloudSettings); this is the SERVICE account the agent acts as (a
// dedicated user, for instance "triagem", with its own app password) and the booking rules. The password
// is stored encrypted and never returned.
type NextcloudCalendar struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"organization_id"`
	Enabled        bool      `gorm:"not null;default:false" json:"enabled"`

	// CalendarPath is the calendar collection on the server, e.g. /remote.php/dav/calendars/triagem/personal/
	CalendarPath string `gorm:"size:300;not null" json:"calendar_path"`
	Username     string `gorm:"size:200;not null" json:"username"`
	AppPassword  string `gorm:"type:text;not null" json:"-"`

	TimeZone     string `gorm:"size:60;not null;default:'America/Sao_Paulo'" json:"time_zone"`
	SlotMinutes  int    `gorm:"not null;default:30" json:"slot_minutes"`
	BufferMin    int    `gorm:"not null;default:0" json:"buffer_minutes"`
	MinNoticeHrs int    `gorm:"not null;default:2" json:"min_notice_hours"`
	LookaheadDay int    `gorm:"not null;default:14" json:"lookahead_days"`
	// Hours maps a weekday (0 = Sunday ... 6 = Saturday) to "HH:MM-HH:MM". A missing weekday is closed.
	Hours JSONB `gorm:"type:jsonb;default:'{}'" json:"hours"`
	// Location is written on the event (an address, or "videochamada").
	Location string `gorm:"size:200" json:"location"`
	// EmbedURL is the calendar's public "embed" page on the same Nextcloud, shown inside the CRM (a tab) so people
	// see the agenda without leaving it. Empty = no calendar tab content.
	EmbedURL string `gorm:"size:500" json:"embed_url"`

	UpdatedByID *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (NextcloudCalendar) TableName() string { return "nextcloud_calendars" }

// Status of an AIAppointmentOffer.
const (
	AIAppointmentOffered = "offered" // slots were sent; the customer has not chosen
	AIAppointmentBooked  = "booked"  // the customer chose and the event exists
	AIAppointmentExpired = "expired" // replaced by a newer offer, or too old
)

// AIAppointmentOffer is a set of meeting times the agent offered a customer. Its Token is the only thing
// a button carries (random, never the time itself); a tap is honoured once, and only while the offer is
// still "offered" and unexpired. The row holds no message text.
type AIAppointmentOffer struct {
	BaseModel
	OrganizationID  uuid.UUID  `gorm:"type:uuid;index;not null;uniqueIndex:idx_ai_offer_booked_slot,priority:1,where:status = 'booked' AND booked_start IS NOT NULL" json:"organization_id"`
	ContactID       uuid.UUID  `gorm:"type:uuid;index;not null" json:"contact_id"`
	SessionID       uuid.UUID  `gorm:"type:uuid;not null" json:"session_id"`
	WhatsAppAccount string     `gorm:"size:100;not null" json:"whatsapp_account"`
	OpportunityID   *uuid.UUID `gorm:"type:uuid" json:"opportunity_id,omitempty"`
	Token           string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	// Slots are the offered starts, RFC 3339 in UTC, in the order of the buttons.
	Slots     StringArray `gorm:"type:jsonb" json:"slots"`
	Status    string      `gorm:"size:20;not null;default:'offered';index" json:"status"`
	ExpiresAt time.Time   `gorm:"not null" json:"expires_at"`
	// Attempts counts how many times booking was tried on a slot the calendar had taken meanwhile.
	Attempts    int        `gorm:"not null;default:0" json:"attempts"`
	// BookedStart is set when the offer is claimed. The partial unique index above makes it the lock on the time: two
	// customers tapping the same time cannot both hold it (a released offer, not "booked" any more, frees it).
	BookedStart *time.Time `gorm:"uniqueIndex:idx_ai_offer_booked_slot,priority:2" json:"booked_start,omitempty"`
	EventUID    string     `gorm:"size:100" json:"event_uid"`
}

func (AIAppointmentOffer) TableName() string { return "ai_appointment_offers" }
