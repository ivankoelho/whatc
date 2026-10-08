// Package usage is the WhatsApp consumption ledger: one row per message
// (message_usage), the append-only log of Meta's status events
// (message_pricing_events) and the settlement that turns the events into a
// billing state and an estimated cost.
//
// It depends only on the database and the configuration, never on the HTTP
// handlers, so the campaign worker and the integrity job use it too. Recording
// is instrumentation: callers log a returned error and carry on — it must never
// stop a message from being sent or processed.
//
// Design: docs/superpowers/specs/2026-10-06-consumo-whatsapp-medicao-design.md.
package usage

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ActorType says who originated a message.
type ActorType string

const (
	ActorAgent       ActorType = "agent"
	ActorAI          ActorType = "ai"
	ActorFlow        ActorType = "flow"
	ActorCampaign    ActorType = "campaign"
	ActorSystem      ActorType = "system"
	ActorExternalApp ActorType = "external_app"
	ActorContact     ActorType = "contact"
)

// BillingState is what is known about the charge for a message.
type BillingState string

const (
	StatePending         BillingState = "pending"
	StateAwaitingPricing BillingState = "awaiting_pricing"
	StatePriced          BillingState = "priced"
	StateNotBillable     BillingState = "not_billable"
	StateNoRate          BillingState = "no_rate"
	StateUnconfirmed     BillingState = "unconfirmed"
	StateSendFailed      BillingState = "send_failed"
	StateFailed          BillingState = "failed"
)

// LinkState says whether the internal message was found. It is independent of
// BillingState: "unlinked" is a reconciliation state, not a loss.
type LinkState string

const (
	LinkLinked   LinkState = "linked"
	LinkUnlinked LinkState = "unlinked"
)

// UnitSource says where a row's unit came from.
type UnitSource string

const (
	UnitFromAgent UnitSource = "agent"
	UnitFromTeam  UnitSource = "team"
	UnitNone      UnitSource = "none"
)

// Origin says who and what produced a message. Zero fields mean "not known".
type Origin struct {
	ActorType    ActorType
	ActorUserID  *uuid.UUID
	TeamID       *uuid.UUID
	FlowID       *uuid.UUID
	FlowNodeID   string
	CampaignID   *uuid.UUID
	OccurrenceID *uuid.UUID
	AIUsageLogID *uuid.UUID
	Detail       string
	// Inferred marks an origin guessed after the fact (integrity job).
	Inferred bool
}

type originKey struct{}

// WithOrigin carries the origin of a send down to the code that records it.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the origin carried by ctx, if any.
func OriginFrom(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originKey{}).(Origin)
	return o, ok
}

// Pricing is the pricing block of a Meta status event.
type Pricing struct {
	Billable     bool
	PricingModel string
	Category     string
}

// StatusEvent is one status webhook entry, reduced to what the ledger keeps.
type StatusEvent struct {
	OrganizationID     uuid.UUID
	WhatsAppAccount    string
	Wamid              string
	Status             string // sent | delivered | read | failed
	EventAt            time.Time
	Pricing            *Pricing // nil when this event carried no pricing
	ConversationID     string
	ConversationOrigin string
	RecipientCountry   string
	ErrorCode          int
	ErrorTitle         string
}
