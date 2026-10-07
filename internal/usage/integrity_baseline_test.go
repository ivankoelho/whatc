package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The integrity job never reconstructs consumption from before the measurement
// started. The start is the creation time of the oldest ledger row of all.

// setBaseline makes the measurement start 'ago' in the past: it writes a ledger row of
// ANOTHER organization created then (so this fixture's own rows stay countable). Any
// ledger row (message and Meta event) left by an earlier test is removed: the job looks at every
// organization, so the shared test database must hold only this test data.
func (f *fixture) setBaseline(t *testing.T, ago time.Duration) time.Time {
	t.Helper()
	require.NoError(t, f.db.Exec("DELETE FROM message_usage").Error)
	require.NoError(t, f.db.Exec("DELETE FROM messages WHERE id NOT IN (SELECT message_id FROM message_usage WHERE message_id IS NOT NULL) AND organization_id <> ?", f.org.ID).Error)
	require.NoError(t, f.db.Exec("DELETE FROM message_pricing_events").Error)
	other := testutil.CreateTestOrganization(t, f.db)
	at := time.Now().Add(-ago)
	require.NoError(t, f.db.Create(&models.MessageUsage{OrganizationID: other.ID, WhatsAppAccount: "baseline", Wamid: "baseline-" + uuid.NewString()[:8],
		Direction: "outgoing", ActorType: "agent", Quantity: 1, SentAt: at, BillingState: "pending", LinkState: "linked", CreatedAt: at}).Error)
	return at
}

func (f *fixture) countFor(t *testing.T, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.MessageUsage{}).Where(where, args...).Count(&n).Error)
	return n
}

func TestSweep_EmptyLedgerHasNoBaselineAndDoesNothing(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.messageAt(t, time.Hour, nil)
	_, err := f.rec.RecordStatusEvent(context.Background(), f.event("w-nobase", "delivered", time.Now().Add(-time.Hour), nil))
	require.NoError(t, err)
	f.backdateEvents(t, "w-nobase", time.Hour)
	require.NoError(t, f.db.Exec("DELETE FROM message_usage").Error) // the ledger is empty

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)

	assert.Equal(t, usage.SweepResult{}, res)
	assert.Zero(t, f.countFor(t, "1 = 1"), "neither a missing row nor an unlinked one is created before the measurement starts")
}

func TestSweep_MessagesBeforeTheBaselineAreIgnoredAndLaterOnesCompleted(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 3*time.Hour)
	before := f.messageAt(t, 5*time.Hour, nil) // inside the 10 h window, but before the measurement began
	after := f.messageAt(t, time.Hour, nil)

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Inferred)

	assert.Zero(t, f.countFor(t, "message_id = ?", before.ID), "consumption before the measurement started is never reconstructed")
	assert.NotNil(t, f.rowOf(t, after))
}

func TestSweep_AWamidOfAMessageBeforeTheBaselineNeverBecomesUnlinked(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 3*time.Hour)
	ctx := context.Background()
	old := f.messageAt(t, 5*time.Hour, nil)
	require.NoError(t, f.db.Model(&models.Message{}).Where("id = ?", old.ID).Update("whats_app_message_id", "w-before").Error)
	price := &usage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}
	for _, w := range []string{"w-before", "w-nomsg"} {
		_, err := f.rec.RecordStatusEvent(ctx, f.event(w, "delivered", time.Now().Add(-time.Hour), price))
		require.NoError(t, err)
		f.backdateEvents(t, w, 30*time.Minute)
	}

	res, err := f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Unlinked)

	assert.Zero(t, f.countFor(t, "organization_id = ? AND wamid = ?", f.org.ID, "w-before"), "its message predates the measurement: no row, not even unlinked")
	assert.EqualValues(t, 1, f.countFor(t, "organization_id = ? AND wamid = ?", f.org.ID, "w-nomsg"), "an event with no message at all is still unlinked, as before")
}

func TestSweep_TheBaselineDoesNotMoveForwardWhenTheJobRuns(t *testing.T) {
	f := newFixture(t, integrityCfg())
	started := f.setBaseline(t, 3*time.Hour)
	f.messageAt(t, time.Hour, nil)
	old := f.messageAt(t, 4*time.Hour, nil)

	for i := 0; i < 3; i++ {
		_, err := f.rec.Sweep(context.Background(), time.Now().Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
	}

	var first time.Time
	require.NoError(t, f.db.Raw("SELECT MIN(created_at) FROM message_usage").Scan(&first).Error)
	assert.WithinDuration(t, started, first, time.Second, "the rows the job wrote are newer: the start of the measurement stays")
	assert.Zero(t, f.countFor(t, "message_id = ?", old.ID), "so a message from before it is still ignored on later runs")
}
