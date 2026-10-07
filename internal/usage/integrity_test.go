package usage_test

import (
	"context"
	"sync"
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

// Non-default parameters on purpose: the job must take every window and deadline
// from the injected configuration.
func integrityCfg() config.UsageConfig {
	return config.UsageConfig{
		UnlinkedAfterMinutes: intp(7), UnlinkedAttentionHours: intp(2), IntegrityIntervalMinutes: intp(2),
		IntegrityWindowHours: intp(10), UndeliveredAfterHours: intp(3),
	}
}

func (f *fixture) messageAt(t *testing.T, age time.Duration, mutate func(*models.Message)) *models.Message {
	t.Helper()
	m := f.message(t, "", models.DirectionOutgoing)
	if mutate != nil {
		mutate(m)
		require.NoError(t, f.db.Save(m).Error)
	}
	require.NoError(t, f.db.Model(&models.Message{}).Where("id = ?", m.ID).Update("created_at", time.Now().Add(-age)).Error)
	m.CreatedAt = time.Now().Add(-age)
	return m
}

func (f *fixture) rowOf(t *testing.T, m *models.Message) models.MessageUsage {
	t.Helper()
	var r models.MessageUsage
	require.NoError(t, f.db.Where("message_id = ?", m.ID).Take(&r).Error)
	return r
}

func TestSweep_MissingRowsGetAnInferredOrigin(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	user := testutil.CreateTestUser(t, f.db, f.org.ID).ID
	camp := uuid.New()
	agentMsg := f.messageAt(t, time.Hour, func(m *models.Message) { m.SentByUserID = &user })
	campMsg := f.messageAt(t, time.Hour, func(m *models.Message) { m.Metadata = models.JSONB{"campaign_id": camp.String()} })
	plain := f.messageAt(t, time.Hour, nil)
	incoming := f.messageAt(t, time.Hour, func(m *models.Message) { m.Direction = models.DirectionIncoming })
	failed := f.messageAt(t, time.Hour, func(m *models.Message) { m.Status = models.MessageStatusFailed })

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 5, res.Inferred)

	a := f.rowOf(t, agentMsg)
	assert.Equal(t, "agent", a.ActorType)
	assert.Equal(t, &user, a.ActorUserID)
	assert.True(t, a.OriginInferred)
	c := f.rowOf(t, campMsg)
	assert.Equal(t, "campaign", c.ActorType)
	assert.Equal(t, &camp, c.CampaignID)
	p := f.rowOf(t, plain)
	assert.Equal(t, "system", p.ActorType)
	assert.Equal(t, "unclassified", p.OriginDetail)
	assert.True(t, p.OriginInferred)
	assert.Equal(t, "contact", f.rowOf(t, incoming).ActorType)
	assert.Equal(t, "send_failed", f.rowOf(t, failed).BillingState)
}

func TestSweep_WindowAndGraceComeFromTheConfiguration(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	old := f.messageAt(t, 11*time.Hour, nil)  // before the 10 h window
	fresh := f.messageAt(t, time.Minute, nil) // younger than the 2 min interval: the normal path owns it
	inside := f.messageAt(t, 5*time.Minute, nil)

	_, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)

	var n int64
	f.db.Model(&models.MessageUsage{}).Where("message_id IN ?", []uuid.UUID{old.ID, fresh.ID}).Count(&n)
	assert.Zero(t, n)
	assert.NotNil(t, f.rowOf(t, inside))

	// with a wider configuration the same data is picked up
	wide := usage.New(f.db, config.UsageConfig{IntegrityWindowHours: intp(48), IntegrityIntervalMinutes: intp(1)})
	_, err = wide.Sweep(context.Background(), time.Now().Add(5*time.Minute))
	require.NoError(t, err)
	assert.NotNil(t, f.rowOf(t, old))
	assert.NotNil(t, f.rowOf(t, fresh))
}

func TestSweep_IsIdempotent(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	f.messageAt(t, time.Hour, nil)
	first, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, first.Inferred)
	second, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Zero(t, second.Inferred)
	assert.Zero(t, second.Errors)
	assert.Len(t, f.rows(t), 1)
}

// More messages than one batch: every page is processed, none is skipped or doubled.
func TestSweep_PagesThroughMoreThanOneBatch(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	const total = 1100
	at := time.Now().Add(-time.Hour)
	msgs := make([]models.Message, total)
	for i := range msgs {
		msgs[i] = models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: at.Add(time.Duration(i) * time.Millisecond)},
			OrganizationID: f.org.ID, WhatsAppAccount: f.account, ContactID: f.contact.ID,
			Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Status: models.MessageStatusSent}
	}
	require.NoError(t, f.db.CreateInBatches(&msgs, 200).Error)

	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, total, res.Inferred)
	assert.Len(t, f.rows(t), total)
}

func TestSweep_ConcurrentPassesNeverDuplicate(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	for i := 0; i < 30; i++ {
		f.messageAt(t, time.Hour, nil)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.rec.Sweep(context.Background(), time.Now())
		}()
	}
	wg.Wait()
	assert.Len(t, f.rows(t), 30, "one message, one row, however many passes raced")
}

func (f *fixture) backdateEvents(t *testing.T, wamid string, age time.Duration) {
	t.Helper()
	require.NoError(t, f.db.Model(&models.MessagePricingEvent{}).Where("organization_id = ? AND wamid = ?", f.org.ID, wamid).
		Update("received_at", time.Now().Add(-age)).Error)
}

func TestSweep_UnlinkedAfterTheConfiguredWait(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	ctx := context.Background()
	price := &usage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}
	f.rate(t, "55", "utility", 0.09, time.Now().UTC().AddDate(0, 0, -3), nil, "BRL")
	for _, w := range []string{"w-young", "w-old"} {
		ev := f.event(w, "delivered", time.Now().Add(-time.Hour), price)
		ev.RecipientCountry = "55"
		_, err := f.rec.RecordStatusEvent(ctx, ev)
		require.NoError(t, err)
	}
	f.backdateEvents(t, "w-young", 3*time.Minute) // less than the 7 min wait
	f.backdateEvents(t, "w-old", 30*time.Minute)

	res, err := f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Unlinked)

	var old models.MessageUsage
	require.NoError(t, f.db.Where("organization_id = ? AND wamid = ?", f.org.ID, "w-old").Take(&old).Error)
	assert.Equal(t, "unlinked", old.LinkState)
	assert.Nil(t, old.MessageID)
	assert.Equal(t, "priced", old.BillingState, "the cost known by wamid is kept")
	require.NotNil(t, old.EstimatedCost)
	assert.InDelta(t, 0.09, *old.EstimatedCost, 1e-9)
	var n int64
	f.db.Model(&models.MessageUsage{}).Where("organization_id = ? AND wamid = ?", f.org.ID, "w-young").Count(&n)
	assert.Zero(t, n, "too early: it may just be the race with the send")

	// the second pass changes nothing
	res, err = f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Zero(t, res.Unlinked)
}

func TestSweep_PendingBecomesUnconfirmedOnlyAfterTheConfiguredDeadline(t *testing.T) {
	f := newFixture(t, integrityCfg())
	ctx := context.Background()
	mk := func(wamid string, age time.Duration) models.MessageUsage {
		row := models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: f.account, Wamid: wamid, Direction: "outgoing",
			ActorType: "agent", Quantity: 1, SentAt: time.Now().Add(-age), BillingState: "pending", LinkState: "linked", RecipientCountry: "55"}
		require.NoError(t, f.db.Create(&row).Error)
		return row
	}
	young := mk("p-young", 2*time.Hour) // < 3 h
	old := mk("p-old", 4*time.Hour)     // > 3 h

	res, err := f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Resettled)

	var y, o models.MessageUsage
	require.NoError(t, f.db.First(&y, "id = ?", young.ID).Error)
	require.NoError(t, f.db.First(&o, "id = ?", old.ID).Error)
	assert.Equal(t, "pending", y.BillingState)
	assert.Equal(t, "unconfirmed", o.BillingState)
	require.NotNil(t, o.EstimatedCost)
	assert.Zero(t, *o.EstimatedCost)

	// the delivery arrives late: it reclassifies
	_, err = f.rec.RecordStatusEvent(ctx, f.event("p-old", "delivered", time.Now(), &usage.Pricing{Billable: false, PricingModel: "PMP", Category: "service"}))
	require.NoError(t, err)
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "p-old"))
	require.NoError(t, f.db.First(&o, "id = ?", old.ID).Error)
	assert.Equal(t, "not_billable", o.BillingState)
}

func TestSweep_SettlesAPendingRowWhoseDeliveryEventWasNeverSettled(t *testing.T) {
	f := newFixture(t, integrityCfg())
	ctx := context.Background()
	row := models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: f.account, Wamid: "missed", Direction: "outgoing",
		ActorType: "agent", Quantity: 1, SentAt: time.Now(), BillingState: "pending", LinkState: "linked"}
	require.NoError(t, f.db.Create(&row).Error)
	// the event is stored but its settlement was lost (e.g. the webhook's settle failed)
	_, err := f.rec.RecordStatusEvent(ctx, f.event("missed", "delivered", time.Now(), nil))
	require.NoError(t, err)

	res, err := f.rec.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Resettled)
	var got models.MessageUsage
	require.NoError(t, f.db.First(&got, "id = ?", row.ID).Error)
	assert.Equal(t, "awaiting_pricing", got.BillingState)
}

func TestSweep_DisabledDoesNothing(t *testing.T) {
	off := false
	cfg := integrityCfg()
	cfg.RecordEnabled = &off
	f := newFixture(t, cfg)
	f.messageAt(t, time.Hour, nil)
	res, err := f.rec.Sweep(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, usage.SweepResult{}, res)
	assert.Empty(t, f.rows(t))
}

func TestSweep_StopsWhenTheContextIsCancelled(t *testing.T) {
	f := newFixture(t, integrityCfg())
	f.setBaseline(t, 30*time.Hour)
	f.messageAt(t, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.rec.Sweep(ctx, time.Now())
	assert.Error(t, err)
}

type nopLog struct{}

func (nopLog) Info(string, ...any)  {}
func (nopLog) Error(string, ...any) {}

func TestRunIntegrity_StopsWithTheContextAndIsOffWhenDisabled(t *testing.T) {
	f := newFixture(t, integrityCfg())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.rec.RunIntegrity(ctx, nopLog{}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not stop with its context")
	}

	off := false
	disabled := usage.New(f.db, config.UsageConfig{RecordEnabled: &off})
	finished := make(chan struct{})
	go func() { disabled.RunIntegrity(context.Background(), nopLog{}); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a disabled job must return immediately")
	}
}
