package models

import (
	"time"

	"github.com/google/uuid"
)

// MessageUsage is the per-message WhatsApp consumption ledger: one row per
// message (incoming and outgoing), written when the message is created and
// changed only by the settlement (usage.Settle) as Meta's status events arrive.
// The messages table is not touched. It never stores message content or the
// full phone number — only the destination country code.
//
// Design: docs/superpowers/specs/2026-10-06-consumo-whatsapp-medicao-design.md §4.
type MessageUsage struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID `gorm:"type:uuid;not null;index:idx_message_usage_org_sent,priority:1;index:idx_message_usage_org_state,priority:1;uniqueIndex:idx_message_usage_org_wamid,priority:1,where:wamid <> ''" json:"organization_id"`

	// Identity. MessageID stays NULL until the internal message is found; Wamid stays
	// empty until the send was accepted. Both are unique where present, so one
	// message is at most one row — enforced by the database, not only by the code.
	WhatsAppAccount string     `gorm:"column:whatsapp_account;size:100;not null;uniqueIndex:idx_message_usage_org_wamid,priority:2,where:wamid <> ''" json:"whatsapp_account"`
	MessageID       *uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_message_usage_message,where:message_id IS NOT NULL" json:"message_id,omitempty"`
	Wamid           string     `gorm:"size:255;not null;default:'';uniqueIndex:idx_message_usage_org_wamid,priority:3,where:wamid <> ''" json:"wamid,omitempty"`
	Direction       string     `gorm:"size:10;not null" json:"direction"` // "incoming" | "outgoing"

	// Context
	ContactID        *uuid.UUID `gorm:"type:uuid" json:"contact_id,omitempty"`
	ConversationID   string     `gorm:"size:255" json:"conversation_id,omitempty"` // Meta's, when informed
	MessageType      string     `gorm:"size:30" json:"message_type,omitempty"`
	RecipientCountry string     `gorm:"size:5" json:"recipient_country,omitempty"` // E.164 country code, e.g. "55"

	// Template (empty for free text)
	TemplateName     string `gorm:"size:255" json:"template_name,omitempty"`
	TemplateID       string `gorm:"size:255" json:"template_id,omitempty"`
	DeclaredCategory string `gorm:"size:30" json:"declared_category,omitempty"` // MARKETING | UTILITY | AUTHENTICATION

	// Origin
	ActorType      string     `gorm:"size:20;not null" json:"actor_type"`
	ActorUserID    *uuid.UUID `gorm:"type:uuid" json:"actor_user_id,omitempty"`
	TeamID         *uuid.UUID `gorm:"type:uuid" json:"team_id,omitempty"`
	UnitID         *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	UnitSource     string     `gorm:"size:10;not null;default:none" json:"unit_source"` // agent | team | none
	FlowID         *uuid.UUID `gorm:"type:uuid" json:"flow_id,omitempty"`
	FlowNodeID     string     `gorm:"size:100" json:"flow_node_id,omitempty"`
	CampaignID     *uuid.UUID `gorm:"type:uuid" json:"campaign_id,omitempty"`
	OccurrenceID   *uuid.UUID `gorm:"type:uuid" json:"occurrence_id,omitempty"`
	AIUsageLogID   *uuid.UUID `gorm:"type:uuid" json:"ai_usage_log_id,omitempty"`
	OriginDetail   string     `gorm:"size:50" json:"origin_detail,omitempty"`
	OriginInferred bool       `gorm:"not null;default:false" json:"origin_inferred"`

	// What Meta reported. Billable is NULL while unknown — that is NOT false.
	BillingCategory    string     `gorm:"size:40" json:"billing_category,omitempty"`
	Billable           *bool      `json:"billable"`
	PricingModel       string     `gorm:"size:30" json:"pricing_model,omitempty"`
	MetaPricing        JSONB      `gorm:"type:jsonb" json:"meta_pricing,omitempty"`         // snapshot behind the current value
	MetaPricingHistory JSONBArray `gorm:"type:jsonb" json:"meta_pricing_history,omitempty"` // up to 5 earlier snapshots
	CategoryDiverged   bool       `gorm:"not null;default:false" json:"category_diverged"`
	MetaCost           *float64   `gorm:"type:numeric(18,6)" json:"meta_cost,omitempty"` // reserved for reconciliation; NULL for now
	MetaCostCurrency   string     `gorm:"size:3" json:"meta_cost_currency,omitempty"`

	// What Whatc computed. EstimatedCost is never overwritten by MetaCost.
	Quantity          int        `gorm:"not null;default:1" json:"quantity"`
	EstimatedCost     *float64   `gorm:"type:numeric(18,6)" json:"estimated_cost"`
	EstimatedCurrency string     `gorm:"size:3" json:"estimated_currency,omitempty"`
	RateID            *uuid.UUID `gorm:"type:uuid;index" json:"rate_id,omitempty"`
	RepricedAt        *time.Time `json:"repriced_at,omitempty"`

	// State. link_state is independent of billing_state.
	BillingState string     `gorm:"size:20;not null;default:pending;index:idx_message_usage_org_state,priority:2" json:"billing_state"`
	LinkState    string     `gorm:"size:10;not null;default:linked;index:idx_message_usage_unlinked,priority:1,where:link_state = 'unlinked'" json:"link_state"`
	SentAt       time.Time  `gorm:"not null;index:idx_message_usage_org_sent,priority:2" json:"sent_at"`
	SettledAt    *time.Time `json:"settled_at,omitempty"`

	CreatedAt time.Time `gorm:"autoCreateTime;index:idx_message_usage_unlinked,priority:2,where:link_state = 'unlinked'" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

func (MessageUsage) TableName() string { return "message_usage" }

// MessagePricingEvent is one status event received from Meta (append-only). A
// status event and a pricing snapshot are different things: an event does not
// always carry pricing, and nothing here assumes it does. Only the relevant
// fields are kept — never the whole webhook, never message content.
type MessagePricingEvent struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_message_pricing_event,priority:1" json:"organization_id"`
	WhatsAppAccount string    `gorm:"column:whatsapp_account;size:100;not null;uniqueIndex:idx_message_pricing_event,priority:2" json:"whatsapp_account"`
	Wamid           string    `gorm:"size:255;not null;uniqueIndex:idx_message_pricing_event,priority:3;index:idx_message_pricing_event_wamid" json:"wamid"`
	Status          string    `gorm:"size:20;not null;uniqueIndex:idx_message_pricing_event,priority:4" json:"status"` // sent|delivered|read|failed
	EventAt         time.Time `gorm:"not null;uniqueIndex:idx_message_pricing_event,priority:5" json:"event_at"`
	ReceivedAt      time.Time `gorm:"not null;autoCreateTime" json:"received_at"`

	HasPricing       bool   `gorm:"not null;default:false" json:"has_pricing"`
	Pricing          JSONB  `gorm:"type:jsonb" json:"pricing,omitempty"`      // {billable, pricing_model, category}; NULL when absent
	Conversation     JSONB  `gorm:"type:jsonb" json:"conversation,omitempty"` // {id, origin}
	RecipientCountry string `gorm:"size:5" json:"recipient_country,omitempty"`
	ErrorCode        int    `json:"error_code,omitempty"`
	ErrorTitle       string `gorm:"size:255" json:"error_title,omitempty"`
}

func (MessagePricingEvent) TableName() string { return "message_pricing_events" }

// WhatsAppRate is the versioned internal price table. There are no prices in
// the code. A row already referenced by a message_usage is immutable: changing a
// price means creating a new validity period.
type WhatsAppRate struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_whatsapp_rates_key,priority:1" json:"organization_id"`
	Country        string     `gorm:"size:5;not null;uniqueIndex:idx_whatsapp_rates_key,priority:2" json:"country"` // E.164 country code or "*"
	Category       string     `gorm:"size:40;not null;uniqueIndex:idx_whatsapp_rates_key,priority:3" json:"category"`
	Price          float64    `gorm:"type:numeric(18,6);not null" json:"price"`
	Currency       string     `gorm:"size:3;not null" json:"currency"` // ISO 4217
	ValidFrom      time.Time  `gorm:"type:date;not null;uniqueIndex:idx_whatsapp_rates_key,priority:4" json:"valid_from"`
	ValidTo        *time.Time `gorm:"type:date" json:"valid_to,omitempty"` // NULL = open
	Notes          string     `gorm:"type:text" json:"notes,omitempty"`
	CreatedBy      *uuid.UUID `gorm:"type:uuid" json:"created_by,omitempty"`
	CreatedAt      time.Time  `gorm:"autoCreateTime" json:"created_at"`
}

func (WhatsAppRate) TableName() string { return "whatsapp_rates" }

// ContactStatusEvent records every contact status transition (append-only),
// written in the same transaction as the change. It is the base for delimiting
// service periods later; a "service" is derived from these events, not stored.
type ContactStatusEvent struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	ContactID      uuid.UUID  `gorm:"type:uuid;not null;index:idx_contact_status_events_contact,priority:1" json:"contact_id"`
	FromStatus     string     `gorm:"size:20;not null" json:"from_status"`
	ToStatus       string     `gorm:"size:20;not null" json:"to_status"`
	ActorType      string     `gorm:"size:20;not null" json:"actor_type"`
	ActorUserID    *uuid.UUID `gorm:"type:uuid" json:"actor_user_id,omitempty"`
	Reason         string     `gorm:"size:30;not null" json:"reason"`
	OccurredAt     time.Time  `gorm:"not null;index:idx_contact_status_events_contact,priority:2" json:"occurred_at"`
}

func (ContactStatusEvent) TableName() string { return "contact_status_events" }
