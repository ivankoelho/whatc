package usage_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	t0   = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now0 = t0.Add(time.Hour)
)

func intp(v int) *int { return &v }

// cfg uses NON-default parameters on purpose: the settlement must read them from
// the injected configuration, never from a constant.
func testCfg() config.UsageConfig {
	return config.UsageConfig{UndeliveredAfterHours: intp(10)}
}

func baseRow() models.MessageUsage {
	return models.MessageUsage{
		Direction: "outgoing", Quantity: 1, SentAt: t0, RecipientCountry: "55",
		DeclaredCategory: "UTILITY", BillingState: string(usage.StatePending),
	}
}

func ev(status string, secs int, p *usage.Pricing) models.MessagePricingEvent {
	e := models.MessagePricingEvent{Status: status, EventAt: t0.Add(time.Duration(secs) * time.Second), ReceivedAt: t0.Add(time.Duration(secs) * time.Second)}
	if p != nil {
		e.HasPricing = true
		e.Pricing = models.JSONB{"billable": p.Billable, "pricing_model": p.PricingModel, "category": p.Category}
	}
	return e
}

func pr(billable bool, cat string) *usage.Pricing {
	return &usage.Pricing{Billable: billable, PricingModel: "PMP", Category: cat}
}

var rateID = uuid.New()

func rates(price float64) usage.RateFunc {
	return func(country, category string, at time.Time) *models.WhatsAppRate {
		if country == "55" && category == "utility" {
			return &models.WhatsAppRate{ID: rateID, Price: price, Currency: "BRL"}
		}
		return nil
	}
}

func apply(row models.MessageUsage, s usage.Settlement) models.MessageUsage {
	row.BillingState, row.Billable, row.BillingCategory = string(s.BillingState), s.Billable, s.BillingCategory
	row.PricingModel, row.ConversationID, row.MetaPricing = s.PricingModel, s.ConversationID, s.MetaPricing
	row.MetaPricingHistory, row.CategoryDiverged = s.MetaPricingHist, s.CategoryDiverged
	row.EstimatedCost, row.EstimatedCurrency, row.RateID, row.RepricedAt = s.EstimatedCost, s.EstimatedCurrency, s.RateID, s.RepricedAt
	return row
}

func TestCompute_AnyOrderOfTheSameEventsGivesTheSameResult(t *testing.T) {
	p := pr(true, "utility")
	events := []models.MessagePricingEvent{ev("sent", 1, p), ev("delivered", 2, p), ev("read", 3, p)}
	perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var want usage.Settlement
	for i, perm := range perms {
		in := []models.MessagePricingEvent{events[perm[0]], events[perm[1]], events[perm[2]]}
		got := usage.Compute(baseRow(), in, rates(0.0068), now0, testCfg())
		if i == 0 {
			want = got
			assert.Equal(t, usage.StatePriced, got.BillingState)
			require.NotNil(t, got.EstimatedCost)
			assert.InDelta(t, 0.0068, *got.EstimatedCost, 1e-9)
			assert.Equal(t, "BRL", got.EstimatedCurrency)
			continue
		}
		assert.Equal(t, want, got, "permutation %v", perm)
	}
}

func TestCompute_Duplicates(t *testing.T) {
	p := pr(true, "utility")
	once := usage.Compute(baseRow(), []models.MessagePricingEvent{ev("delivered", 2, p)}, rates(1), now0, testCfg())
	twice := usage.Compute(baseRow(), []models.MessagePricingEvent{ev("delivered", 2, p), ev("delivered", 2, p)}, rates(1), now0, testCfg())
	assert.Equal(t, once, twice)
}

func TestCompute_States(t *testing.T) {
	tests := []struct {
		name      string
		row       func() models.MessageUsage
		events    []models.MessagePricingEvent
		rates     usage.RateFunc
		now       time.Time
		state     usage.BillingState
		billable  *bool
		wantCost  *float64
		wantCurr  string
		wantCateg string
	}{
		{name: "just sent: pending", events: []models.MessagePricingEvent{ev("sent", 1, nil)},
			state: usage.StatePending},
		{name: "no event at all within the deadline: pending", state: usage.StatePending},
		{name: "event without pricing is recorded but proves nothing: billable stays unknown (nil, not false)",
			events: []models.MessagePricingEvent{ev("sent", 1, nil)}, state: usage.StatePending, billable: nil},
		{name: "delivered without pricing: awaiting_pricing, cost unknown",
			events: []models.MessagePricingEvent{ev("sent", 1, nil), ev("delivered", 2, nil)},
			state:  usage.StateAwaitingPricing},
		{name: "not billable costs zero",
			events: []models.MessagePricingEvent{ev("delivered", 2, pr(false, "service"))},
			state:  usage.StateNotBillable, billable: boolp(false), wantCost: floatp(0), wantCateg: "service"},
		{name: "billable and priced",
			events: []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))}, rates: rates(0.25),
			state: usage.StatePriced, billable: boolp(true), wantCost: floatp(0.25), wantCurr: "BRL", wantCateg: "utility"},
		{name: "billable without a price: no_rate (cost unknown, never silent zero)",
			events: []models.MessagePricingEvent{ev("delivered", 2, pr(true, "marketing"))}, rates: rates(0.25),
			state: usage.StateNoRate, billable: boolp(true), wantCateg: "marketing"},
		{name: "failed without pricing: not charged",
			events: []models.MessagePricingEvent{ev("sent", 1, nil), ev("failed", 3, nil)},
			state:  usage.StateFailed, billable: boolp(false), wantCost: floatp(0)},
		{name: "failed wins over delivered",
			events: []models.MessagePricingEvent{ev("delivered", 2, nil), ev("failed", 3, nil)},
			state:  usage.StateFailed, billable: boolp(false), wantCost: floatp(0)},
		{name: "failed but Meta says billable: charged",
			events: []models.MessagePricingEvent{ev("failed", 3, pr(true, "utility"))}, rates: rates(0.5),
			state: usage.StatePriced, billable: boolp(true), wantCost: floatp(0.5), wantCurr: "BRL", wantCateg: "utility"},
		{name: "late sent carrying pricing after a delivered without it still counts",
			events: []models.MessagePricingEvent{ev("delivered", 2, nil), ev("sent", 1, pr(true, "utility"))}, rates: rates(0.1),
			state: usage.StatePriced, billable: boolp(true), wantCost: floatp(0.1), wantCurr: "BRL", wantCateg: "utility"},
		{name: "never delivered before the deadline: still pending", now: t0.Add(9 * time.Hour),
			events: []models.MessagePricingEvent{ev("sent", 1, nil)}, state: usage.StatePending},
		{name: "never delivered past the configured deadline: unconfirmed, cost zero", now: t0.Add(10 * time.Hour),
			events: []models.MessagePricingEvent{ev("sent", 1, nil)}, state: usage.StateUnconfirmed, wantCost: floatp(0)},
		{name: "unconfirmed reclassifies when the delivery arrives late", now: t0.Add(30 * time.Hour),
			events: []models.MessagePricingEvent{ev("sent", 1, nil), ev("delivered", 2, nil)}, state: usage.StateAwaitingPricing},
		{name: "unconfirmed then priced when pricing arrives", now: t0.Add(30 * time.Hour), rates: rates(2),
			events: []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))},
			state:  usage.StatePriced, billable: boolp(true), wantCost: floatp(2), wantCurr: "BRL", wantCateg: "utility"},
		{name: "incoming is never billable",
			row:   func() models.MessageUsage { r := baseRow(); r.Direction = "incoming"; return r },
			state: usage.StateNotBillable, billable: boolp(false), wantCost: floatp(0)},
		{name: "send_failed is terminal",
			row:   func() models.MessageUsage { r := baseRow(); r.BillingState = string(usage.StateSendFailed); return r },
			state: usage.StateSendFailed},
		{name: "quantity multiplies the price", rates: rates(0.5),
			row:    func() models.MessageUsage { r := baseRow(); r.Quantity = 3; return r },
			events: []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))},
			state:  usage.StatePriced, billable: boolp(true), wantCost: floatp(1.5), wantCurr: "BRL", wantCateg: "utility"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := baseRow()
			if tc.row != nil {
				row = tc.row()
			}
			at := tc.now
			if at.IsZero() {
				at = now0
			}
			got := usage.Compute(row, tc.events, tc.rates, at, testCfg())
			assert.Equal(t, tc.state, got.BillingState)
			assert.Equal(t, tc.billable, got.Billable)
			assert.Equal(t, tc.wantCost, got.EstimatedCost)
			assert.Equal(t, tc.wantCurr, got.EstimatedCurrency)
			assert.Equal(t, tc.wantCateg, got.BillingCategory)
		})
	}
}

func TestCompute_UndeliveredDeadlineComesFromTheConfiguration(t *testing.T) {
	events := []models.MessagePricingEvent{ev("sent", 1, nil)}
	at := t0.Add(2 * time.Hour)
	short := config.UsageConfig{UndeliveredAfterHours: intp(1)}
	long := config.UsageConfig{UndeliveredAfterHours: intp(5)}
	assert.Equal(t, usage.StateUnconfirmed, usage.Compute(baseRow(), events, nil, at, short).BillingState)
	assert.Equal(t, usage.StatePending, usage.Compute(baseRow(), events, nil, at, long).BillingState)
}

func TestCompute_LatePricingSnapshot(t *testing.T) {
	row := baseRow()
	first := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))}, rates(1), now0, testCfg())
	row = apply(row, first)
	require.Nil(t, row.RepricedAt)

	t.Run("the same pricing again changes nothing", func(t *testing.T) {
		again := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility")), ev("read", 3, pr(true, "utility"))}, rates(1), now0.Add(time.Hour), testCfg())
		assert.False(t, again.DiffersFrom(row))
		assert.Nil(t, again.RepricedAt)
		assert.Empty(t, again.MetaPricingHist)
	})

	t.Run("a different pricing recalculates and keeps the previous one", func(t *testing.T) {
		later := now0.Add(2 * time.Hour)
		next := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility")), ev("read", 9, pr(true, "marketing"))}, rates(1), later, testCfg())
		assert.Equal(t, "marketing", next.BillingCategory)
		assert.Equal(t, usage.StateNoRate, next.BillingState)
		require.Len(t, next.MetaPricingHist, 1)
		require.NotNil(t, next.RepricedAt)
		assert.True(t, next.RepricedAt.Equal(later))
	})

	t.Run("the history keeps at most 5 snapshots", func(t *testing.T) {
		r := row
		for i := 0; i < 8; i++ {
			cat := []string{"utility", "marketing"}[i%2]
			s := usage.Compute(r, []models.MessagePricingEvent{ev("read", 10+i, pr(true, cat))}, rates(1), now0, testCfg())
			r = apply(r, s)
		}
		assert.LessOrEqual(t, len(r.MetaPricingHistory), 5)
	})
}

func TestCompute_MetaPrevailsOnCategoryAndIsFlagged(t *testing.T) {
	row := baseRow() // declared UTILITY
	got := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "marketing"))}, rates(1), now0, testCfg())
	assert.Equal(t, "marketing", got.BillingCategory)
	assert.True(t, got.CategoryDiverged)

	same := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))}, rates(1), now0, testCfg())
	assert.False(t, same.CategoryDiverged)
}

func TestCompute_MetaCostIsNeverUsed(t *testing.T) {
	row := baseRow()
	cost := 99.0
	row.MetaCost = &cost
	got := usage.Compute(row, []models.MessagePricingEvent{ev("delivered", 2, pr(true, "utility"))}, rates(0.5), now0, testCfg())
	require.NotNil(t, got.EstimatedCost)
	assert.InDelta(t, 0.5, *got.EstimatedCost, 1e-9, "the estimate comes from the internal table only")
}

func TestCompute_ConversationIDFromTheEvents(t *testing.T) {
	e := ev("delivered", 2, pr(true, "utility"))
	e.Conversation = models.JSONB{"id": "conv-1"}
	got := usage.Compute(baseRow(), []models.MessagePricingEvent{ev("sent", 1, nil), e}, rates(1), now0, testCfg())
	assert.Equal(t, "conv-1", got.ConversationID)
}

func TestCompute_AppliedSettlementIsStable(t *testing.T) {
	row := baseRow()
	events := []models.MessagePricingEvent{ev("sent", 1, nil), ev("delivered", 2, pr(true, "utility"))}
	s := usage.Compute(row, events, rates(0.3), now0, testCfg())
	require.True(t, s.DiffersFrom(row))
	row = apply(row, s)
	assert.False(t, usage.Compute(row, events, rates(0.3), now0.Add(time.Hour), testCfg()).DiffersFrom(row))
}

func boolp(b bool) *bool        { return &b }
func floatp(f float64) *float64 { return &f }
