package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) turnOff(t *testing.T, at time.Time) {
	t.Helper()
	_, err := f.rec.SetRecording(context.Background(), f.org.ID, false, at)
	require.NoError(t, err)
}

func (f *fixture) turnOn(t *testing.T, at time.Time) {
	t.Helper()
	_, err := f.rec.SetRecording(context.Background(), f.org.ID, true, at)
	require.NoError(t, err)
}

func (f *fixture) eventCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.MessagePricingEvent{}).Where("organization_id = ?", f.org.ID).Count(&n).Error)
	return n
}

func TestRecordingSwitch_DefaultsToOnAndRemembersWhenItWasFlipped(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()

	st, err := f.rec.RecordingState(ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, st.Enabled, "an organization that never touched it records")
	assert.Nil(t, st.ChangedAt)
	assert.True(t, f.rec.EnabledFor(ctx, f.org.ID))

	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	f.turnOff(t, at)
	st, err = f.rec.RecordingState(ctx, f.org.ID)
	require.NoError(t, err)
	assert.False(t, st.Enabled)
	require.NotNil(t, st.ChangedAt)
	assert.WithinDuration(t, at, *st.ChangedAt, time.Millisecond)
	assert.False(t, f.rec.EnabledFor(ctx, f.org.ID))

	// the other settings of the organization are untouched by the switch
	var org models.Organization
	require.NoError(t, f.db.First(&org, "id = ?", f.org.ID).Error)
	assert.NotNil(t, org.Settings[usage.RecordingSettingsKey])
}

func TestRecordingSwitch_OffStopsEveryKindOfRecording(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	msg := f.message(t, "w-off", models.DirectionOutgoing)
	f.turnOff(t, time.Now())

	require.NoError(t, f.rec.Record(ctx, nil, msg, usage.Origin{ActorType: usage.ActorAgent}))
	ins, err := f.rec.RecordStatusEvent(ctx, f.event("w-off", "delivered", time.Now(), nil))
	require.NoError(t, err)
	assert.False(t, ins)
	require.NoError(t, f.rec.AttachWamid(ctx, f.org.ID, msg.ID, f.account, "w-off"))
	require.NoError(t, f.rec.MarkSendFailed(ctx, nil, f.org.ID, msg.ID))
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w-off"))
	require.NoError(t, f.rec.RecordContactStatus(ctx, nil, models.ContactStatusEvent{OrganizationID: f.org.ID, ContactID: f.contact.ID,
		FromStatus: "new", ToStatus: "in_progress", ActorType: "agent", Reason: "api"}))

	assert.Empty(t, f.rows(t))
	assert.Zero(t, f.eventCount(t))
	var n int64
	f.db.Model(&models.ContactStatusEvent{}).Where("organization_id = ?", f.org.ID).Count(&n)
	assert.Zero(t, n, "not even the status history is written")
}

func TestRecordingSwitch_OnAgainResumesAndNeverRewritesWhatWasMissed(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	f.turnOff(t, time.Now().Add(-2*time.Hour))
	missed := f.message(t, "w-missed", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, missed, usage.Origin{ActorType: usage.ActorAgent}))
	assert.Empty(t, f.rows(t))

	f.turnOn(t, time.Now())
	later := f.message(t, "w-later", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, later, usage.Origin{ActorType: usage.ActorAgent}))
	rows := f.rows(t)
	require.Len(t, rows, 1, "recording resumed")
	assert.Equal(t, &later.ID, rows[0].MessageID)
}

func TestRecordingSwitch_OffFreezesExistingRowsUntilItIsTurnedOnAgain(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	msg := f.message(t, "w-frozen", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, msg, usage.Origin{ActorType: usage.ActorAgent}))
	_, err := f.rec.RecordStatusEvent(ctx, f.event("w-frozen", "delivered", time.Now(), nil))
	require.NoError(t, err)

	f.turnOff(t, time.Now())
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w-frozen"))
	assert.Equal(t, "pending", f.rows(t)[0].BillingState, "frozen while it is off")

	f.turnOn(t, time.Now())
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w-frozen"))
	assert.Equal(t, "awaiting_pricing", f.rows(t)[0].BillingState)
}

func TestRecordingSwitch_TakesEffectAtOnceHereAndOnAFreshInstance(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	assert.True(t, f.rec.EnabledFor(ctx, f.org.ID)) // cached as on
	f.turnOff(t, time.Now())
	assert.False(t, f.rec.EnabledFor(ctx, f.org.ID), "the instance that flips it does not wait for the cache")

	other := usage.New(f.db, config.UsageConfig{}) // another server instance: its own cache
	assert.False(t, other.EnabledFor(ctx, f.org.ID))
}

func TestRecordingSwitch_IsPerOrganization(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	other := testutil.CreateTestOrganization(t, f.db)
	f.turnOff(t, time.Now())
	assert.True(t, f.rec.EnabledFor(context.Background(), other.ID))
	assert.False(t, f.rec.EnabledFor(context.Background(), f.org.ID))
}

func TestRecordingSwitch_TheServerSwitchWinsAndThePanelCannotOverrideIt(t *testing.T) {
	off := false
	f := newFixture(t, config.UsageConfig{RecordEnabled: &off})
	ctx := context.Background()
	assert.False(t, f.rec.EnabledFor(ctx, f.org.ID))
	_, err := f.rec.SetRecording(ctx, f.org.ID, true, time.Now())
	require.NoError(t, err)
	assert.False(t, f.rec.EnabledFor(ctx, f.org.ID), "the panel on does not beat usage.record_enabled = false")
}

func TestRecordingSwitch_AnUnknownOrganizationOrAFailingReadKeepsRecordingOn(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	assert.True(t, f.rec.EnabledFor(context.Background(), uuid.New()), "reading the switch must never break a send: the default is on")
	var nilRec *usage.Recorder
	assert.False(t, nilRec.EnabledFor(context.Background(), f.org.ID))
	st, err := nilRec.RecordingState(context.Background(), f.org.ID)
	require.NoError(t, err)
	assert.True(t, st.Enabled)
}

// The integrity job honours the panel switch.
func TestSweep_AnOrganizationSwitchedOffIsLeftAlone(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	other := newFixtureFor(t, f, integrityCfg())
	f.turnOff(t, time.Now().Add(-6*time.Hour))
	f.messageAt(t, time.Hour, nil)
	other.messageAt(t, time.Hour, nil)

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)

	assert.Equal(t, 1, res.Inferred, "only the organization that is on")
	assert.Empty(t, f.rows(t))
	assert.Len(t, other.rows(t), 1)
}

func TestSweep_OnlyWhatWasCreatedSinceItWasTurnedOnAgainIsCompleted(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	f.turnOff(t, time.Now().Add(-8*time.Hour))
	f.turnOn(t, time.Now().Add(-3*time.Hour))
	during := f.messageAt(t, 5*time.Hour, nil) // created while it was off
	after := f.messageAt(t, time.Hour, nil)    // created after it was turned on again

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Inferred)

	var n int64
	f.db.Model(&models.MessageUsage{}).Where("message_id = ?", during.ID).Count(&n)
	assert.Zero(t, n, "the period it was off is never reconstructed")
	assert.NotNil(t, f.rowOf(t, after))
}

func TestSweep_UnlinkedAndPendingAlsoRespectTheSwitch(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	ctx := context.Background()
	price := &usage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}
	for _, w := range []string{"w-u"} {
		_, err := f.rec.RecordStatusEvent(ctx, f.event(w, "sent", time.Now().Add(-90*time.Minute), nil))
		require.NoError(t, err)
		_, err = f.rec.RecordStatusEvent(ctx, f.event(w, "delivered", time.Now().Add(-time.Hour), price))
		require.NoError(t, err)
		f.backdateEvents(t, w, 30*time.Minute)
	}
	// a pending row past the undelivered deadline
	require.NoError(t, f.db.Create(&models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: f.account, Wamid: "w-p", Direction: "outgoing",
		ActorType: "agent", Quantity: 1, SentAt: time.Now().Add(-4 * time.Hour), BillingState: "pending", LinkState: "linked"}).Error)

	f.turnOff(t, time.Now())
	res, err := f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Zero(t, res.Unlinked, "no unlinked row for an organization that is off")
	var p models.MessageUsage
	require.NoError(t, f.db.Where("wamid = ?", "w-p").Take(&p).Error)
	assert.Equal(t, "pending", p.BillingState, "its pending rows stay as they are, even past the undelivered deadline")
}

// newFixtureFor is a second organization on the same database (its own contact and account).
func newFixtureFor(t *testing.T, f *fixture, cfg config.UsageConfig) *fixture {
	t.Helper()
	org := testutil.CreateTestOrganization(t, f.db)
	contact := testutil.CreateTestContactWith(t, f.db, org.ID, testutil.WithPhoneNumber("5511999990001"))
	return &fixture{db: f.db, org: org, account: "acc-" + uuid.NewString()[:6], contact: contact, rec: usage.New(f.db, cfg)}
}
