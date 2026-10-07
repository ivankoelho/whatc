package usage

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
)

// maxPricingHistory is how many earlier Meta pricing snapshots a row keeps.
// A technical bound on the row size, not an operational parameter.
const maxPricingHistory = 5

// RateFunc finds the price in force for a destination country, billing
// category and instant, or nil. It is injected so Compute does no I/O.
type RateFunc func(country, category string, at time.Time) *models.WhatsAppRate

// Settlement is the part of a message_usage row that the settlement owns.
type Settlement struct {
	BillingState      BillingState
	Billable          *bool
	BillingCategory   string
	PricingModel      string
	ConversationID    string
	MetaPricing       models.JSONB
	MetaPricingHist   models.JSONBArray
	CategoryDiverged  bool
	EstimatedCost     *float64
	EstimatedCurrency string
	RateID            *uuid.UUID
	RepricedAt        *time.Time
}

// Compute is the settlement: a pure function of the row, the full SET of
// Meta's status events for its wamid, the price table and the clock. Because it
// reads the whole set, duplicates, out-of-order delivery and late pricing all
// lead to the same result, and it never regresses.
//
// Rules (design §6.2 and §7): Meta prevails on category, billable and model; the
// internal table prevails on the amount; billable stays nil (unknown) until Meta
// says otherwise; estimated cost is never taken from Meta.
func Compute(row models.MessageUsage, events []models.MessagePricingEvent, rates RateFunc, now time.Time, cfg config.UsageConfig) Settlement {
	s := Settlement{
		BillingState: BillingState(row.BillingState), Billable: row.Billable, BillingCategory: row.BillingCategory,
		PricingModel: row.PricingModel, ConversationID: row.ConversationID, MetaPricing: row.MetaPricing,
		MetaPricingHist: row.MetaPricingHistory, CategoryDiverged: row.CategoryDiverged,
		EstimatedCost: row.EstimatedCost, EstimatedCurrency: row.EstimatedCurrency, RateID: row.RateID,
		RepricedAt: row.RepricedAt,
	}

	// The API refused the send: there is no wamid and nothing to settle.
	if BillingState(row.BillingState) == StateSendFailed {
		return s
	}
	// Anything received is never charged.
	if row.Direction == string(models.DirectionIncoming) {
		s.BillingState, s.Billable = StateNotBillable, boolPtr(false)
		s.EstimatedCost, s.EstimatedCurrency, s.RateID = floatPtr(0), "", nil
		return s
	}

	ordered := append([]models.MessagePricingEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].EventAt.Equal(ordered[j].EventAt) {
			return ordered[i].EventAt.Before(ordered[j].EventAt)
		}
		return ordered[i].ReceivedAt.Before(ordered[j].ReceivedAt)
	})

	var (
		failed, delivered bool
		latest            *Pricing // pricing of the most recent event that carried any
		conv              string
	)
	for _, ev := range ordered {
		switch ev.Status {
		case "failed":
			failed = true
		case "delivered", "read":
			delivered = true
		}
		if p, ok := pricingOf(ev); ok {
			latest = &p
		}
		if id, _ := ev.Conversation["id"].(string); id != "" {
			conv = id
		}
	}
	if conv != "" {
		s.ConversationID = conv
	}

	// Pricing snapshot: a different one replaces the current and pushes it to the history.
	if latest != nil {
		if old, ok := pricingFromJSON(row.MetaPricing); ok && old != *latest {
			s.MetaPricingHist = append(append(models.JSONBArray(nil), row.MetaPricingHistory...), row.MetaPricing)
			if n := len(s.MetaPricingHist); n > maxPricingHistory {
				s.MetaPricingHist = s.MetaPricingHist[n-maxPricingHistory:]
			}
			t := now
			s.RepricedAt = &t
		}
		s.MetaPricing = models.JSONB{"billable": latest.Billable, "pricing_model": latest.PricingModel, "category": latest.Category}
		s.PricingModel = latest.PricingModel
		s.BillingCategory = strings.ToLower(latest.Category)
		s.CategoryDiverged = row.DeclaredCategory != "" && s.BillingCategory != "" &&
			!strings.EqualFold(row.DeclaredCategory, s.BillingCategory)
	}

	// Billable: what Meta said; a failure without pricing is not charged; otherwise unknown.
	s.Billable = nil
	switch {
	case latest != nil:
		s.Billable = boolPtr(latest.Billable)
	case failed:
		s.Billable = boolPtr(false)
	}

	s.EstimatedCost, s.EstimatedCurrency, s.RateID = nil, "", nil
	switch {
	case failed && (s.Billable == nil || !*s.Billable):
		s.BillingState, s.EstimatedCost = StateFailed, floatPtr(0)
	case s.Billable != nil && !*s.Billable:
		s.BillingState, s.EstimatedCost = StateNotBillable, floatPtr(0)
	case s.Billable != nil: // billable
		var rate *models.WhatsAppRate
		if rates != nil {
			rate = rates(row.RecipientCountry, s.BillingCategory, row.SentAt)
		}
		if rate == nil {
			s.BillingState = StateNoRate
		} else {
			s.BillingState = StatePriced
			s.EstimatedCost = floatPtr(float64(maxInt(row.Quantity, 1)) * rate.Price)
			s.EstimatedCurrency, s.RateID = rate.Currency, &rate.ID
		}
	case delivered:
		s.BillingState = StateAwaitingPricing
	case !row.SentAt.IsZero() && now.Sub(row.SentAt) >= cfg.UndeliveredAfter():
		s.BillingState, s.EstimatedCost = StateUnconfirmed, floatPtr(0)
	default:
		s.BillingState = StatePending
	}

	// A different rate on a row that already had one is a repricing.
	if row.RateID != nil && s.RateID != nil && *row.RateID != *s.RateID && s.RepricedAt == row.RepricedAt {
		t := now
		s.RepricedAt = &t
	}
	return s
}

// DiffersFrom reports whether applying s would change row.
func (s Settlement) DiffersFrom(row models.MessageUsage) bool {
	return string(s.BillingState) != row.BillingState || !boolEq(s.Billable, row.Billable) ||
		s.BillingCategory != row.BillingCategory || s.PricingModel != row.PricingModel ||
		s.ConversationID != row.ConversationID || s.CategoryDiverged != row.CategoryDiverged ||
		!floatEq(s.EstimatedCost, row.EstimatedCost) || s.EstimatedCurrency != row.EstimatedCurrency ||
		!uuidEq(s.RateID, row.RateID) || len(s.MetaPricingHist) != len(row.MetaPricingHistory) ||
		!pricingEq(s.MetaPricing, row.MetaPricing)
}

func pricingOf(ev models.MessagePricingEvent) (Pricing, bool) {
	if !ev.HasPricing {
		return Pricing{}, false
	}
	return pricingFromJSON(ev.Pricing)
}

func pricingFromJSON(j models.JSONB) (Pricing, bool) {
	if len(j) == 0 {
		return Pricing{}, false
	}
	b, _ := j["billable"].(bool)
	m, _ := j["pricing_model"].(string)
	c, _ := j["category"].(string)
	return Pricing{Billable: b, PricingModel: m, Category: c}, true
}

func pricingEq(a, b models.JSONB) bool {
	pa, oka := pricingFromJSON(a)
	pb, okb := pricingFromJSON(b)
	return oka == okb && pa == pb
}

func boolPtr(b bool) *bool        { return &b }
func floatPtr(f float64) *float64 { return &f }
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func boolEq(a, b *bool) bool     { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func floatEq(a, b *float64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func uuidEq(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
