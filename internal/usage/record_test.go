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
	"gorm.io/gorm"
)

type fixture struct {
	db      *gorm.DB
	org     *models.Organization
	account string
	contact *models.Contact
	rec     *usage.Recorder
}

func newFixture(t *testing.T, cfg config.UsageConfig) *fixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("5511999990000"))
	return &fixture{db: db, org: org, account: "acc-" + uuid.NewString()[:6], contact: contact, rec: usage.New(db, cfg)}
}

func (f *fixture) message(t *testing.T, wamid string, dir models.Direction) *models.Message {
	t.Helper()
	m := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, WhatsAppAccount: f.account,
		ContactID: f.contact.ID, WhatsAppMessageID: wamid, Direction: dir, MessageType: models.MessageTypeText,
		Content: "secret content", Status: models.MessageStatusPending,
	}
	require.NoError(t, f.db.Create(m).Error)
	return m
}

func (f *fixture) rows(t *testing.T) []models.MessageUsage {
	t.Helper()
	var rows []models.MessageUsage
	require.NoError(t, f.db.Where("organization_id = ?", f.org.ID).Find(&rows).Error)
	return rows
}

func (f *fixture) event(wamid, status string, at time.Time, p *usage.Pricing) usage.StatusEvent {
	return usage.StatusEvent{OrganizationID: f.org.ID, WhatsAppAccount: f.account, Wamid: wamid, Status: status, EventAt: at, Pricing: p}
}

func (f *fixture) rate(t *testing.T, country, category string, price float64, from time.Time, to *time.Time, currency string) {
	t.Helper()
	require.NoError(t, f.db.Create(&models.WhatsAppRate{OrganizationID: f.org.ID, Country: country, Category: category,
		Price: price, Currency: currency, ValidFrom: from, ValidTo: to}).Error)
}

var agentOrigin = func(u *uuid.UUID) usage.Origin { return usage.Origin{ActorType: usage.ActorAgent, ActorUserID: u} }

func TestRecord_IsIdempotent(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	msg := f.message(t, "", models.DirectionOutgoing)
	ctx := context.Background()

	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	assert.Len(t, f.rows(t), 1)

	t.Run("concurrently", func(t *testing.T) {
		m := f.message(t, "", models.DirectionOutgoing)
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = f.rec.Record(ctx, nil, m, agentOrigin(nil))
			}()
		}
		wg.Wait()
		var n int64
		f.db.Model(&models.MessageUsage{}).Where("message_id = ?", m.ID).Count(&n)
		assert.EqualValues(t, 1, n)
	})
}

func TestRecord_StoresIdentifiersOnly(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	msg := f.message(t, "", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(context.Background(), nil, msg, agentOrigin(nil)))

	row := f.rows(t)[0]
	assert.Equal(t, "55", row.RecipientCountry)
	assert.Equal(t, usage.StatePending, usage.BillingState(row.BillingState))
	assert.Equal(t, usage.LinkLinked, usage.LinkState(row.LinkState))
	assert.Nil(t, row.Billable, "unknown, not false")
	assert.Nil(t, row.EstimatedCost)
	assert.Equal(t, "outgoing", row.Direction)

	// nothing of the content or the phone number is in the ledger row
	var dump string
	require.NoError(t, f.db.Raw("SELECT row_to_json(u)::text FROM message_usage u WHERE id = ?", row.ID).Scan(&dump).Error)
	assert.NotContains(t, dump, "secret content")
	assert.NotContains(t, dump, "5511999990000")
}

func TestRecord_IncomingIsNotBillable(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	msg := f.message(t, "wamid-in", models.DirectionIncoming)
	require.NoError(t, f.rec.Record(context.Background(), nil, msg, usage.Origin{ActorType: usage.ActorContact}))
	row := f.rows(t)[0]
	assert.Equal(t, string(usage.StateNotBillable), row.BillingState)
	require.NotNil(t, row.Billable)
	assert.False(t, *row.Billable)
	require.NotNil(t, row.EstimatedCost)
	assert.Zero(t, *row.EstimatedCost)
}

func TestRecord_WithoutOriginIsInferredAsUnclassified(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	require.NoError(t, f.rec.Record(context.Background(), nil, f.message(t, "", models.DirectionOutgoing), usage.Origin{}))
	row := f.rows(t)[0]
	assert.Equal(t, "system", row.ActorType)
	assert.Equal(t, "unclassified", row.OriginDetail)
	assert.True(t, row.OriginInferred)
}

func TestRecord_UnitAttribution_AgentThenTeamThenNone(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	unitAgent := &models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "U-agent", Active: true}
	unitTeam := &models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "U-team", Active: true}
	require.NoError(t, f.db.Create(unitAgent).Error)
	require.NoError(t, f.db.Create(unitTeam).Error)
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "T", UnitID: &unitTeam.ID}
	require.NoError(t, f.db.Create(team).Error)
	userWithUnit := testutil.CreateTestUser(t, f.db, f.org.ID)
	require.NoError(t, f.db.Model(userWithUnit).Update("unit_id", unitAgent.ID).Error)
	userNoUnit := testutil.CreateTestUser(t, f.db, f.org.ID)
	ctx := context.Background()

	cases := []struct {
		name   string
		origin usage.Origin
		unit   *uuid.UUID
		source usage.UnitSource
	}{
		{"agent's unit wins", usage.Origin{ActorType: usage.ActorAgent, ActorUserID: &userWithUnit.ID, TeamID: &team.ID}, &unitAgent.ID, usage.UnitFromAgent},
		{"agent without unit falls to the team", usage.Origin{ActorType: usage.ActorAgent, ActorUserID: &userNoUnit.ID, TeamID: &team.ID}, &unitTeam.ID, usage.UnitFromTeam},
		{"bot with a team", usage.Origin{ActorType: usage.ActorFlow, TeamID: &team.ID}, &unitTeam.ID, usage.UnitFromTeam},
		{"nobody", usage.Origin{ActorType: usage.ActorSystem}, nil, usage.UnitNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := f.message(t, "", models.DirectionOutgoing)
			require.NoError(t, f.rec.Record(ctx, nil, msg, tc.origin))
			var row models.MessageUsage
			require.NoError(t, f.db.Where("message_id = ?", msg.ID).Take(&row).Error)
			assert.Equal(t, tc.unit, row.UnitID)
			assert.Equal(t, string(tc.source), row.UnitSource)
		})
	}
}

func TestRecord_OriginIsStored(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	flow, camp, occ := uuid.New(), uuid.New(), uuid.New()
	msg := f.message(t, "", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(context.Background(), nil, msg, usage.Origin{
		ActorType: usage.ActorFlow, FlowID: &flow, FlowNodeID: "node-3", CampaignID: &camp, OccurrenceID: &occ, Detail: "keyword"}))
	row := f.rows(t)[0]
	assert.Equal(t, "flow", row.ActorType)
	assert.Equal(t, &flow, row.FlowID)
	assert.Equal(t, "node-3", row.FlowNodeID)
	assert.Equal(t, &camp, row.CampaignID)
	assert.Equal(t, &occ, row.OccurrenceID)
	assert.Equal(t, "keyword", row.OriginDetail)
	assert.False(t, row.OriginInferred)
}

func TestRecord_TemplateKeepsNameIDAndDeclaredCategory(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	require.NoError(t, f.db.Create(&models.Template{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID,
		WhatsAppAccount: f.account, Name: "promo", Language: "pt_BR", Category: "MARKETING", MetaTemplateID: "meta-9"}).Error)
	msg := f.message(t, "", models.DirectionOutgoing)
	msg.TemplateName = "promo"
	msg.MessageType = models.MessageTypeTemplate
	require.NoError(t, f.rec.Record(context.Background(), nil, msg, agentOrigin(nil)))
	row := f.rows(t)[0]
	assert.Equal(t, "promo", row.TemplateName)
	assert.Equal(t, "meta-9", row.TemplateID)
	assert.Equal(t, "MARKETING", row.DeclaredCategory)
}

func TestRecord_DisabledOrNilDoesNothingAndNeverFails(t *testing.T) {
	off := false
	f := newFixture(t, config.UsageConfig{RecordEnabled: &off})
	msg := f.message(t, "", models.DirectionOutgoing)
	ctx := context.Background()

	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	_, err := f.rec.RecordStatusEvent(ctx, f.event("w", "sent", time.Now(), nil))
	require.NoError(t, err)
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w"))
	require.NoError(t, f.rec.MarkSendFailed(ctx, nil, msg.ID))
	require.NoError(t, f.rec.AttachWamid(ctx, f.org.ID, msg.ID, f.account, "w"))
	assert.Empty(t, f.rows(t))
	var n int64
	f.db.Model(&models.MessagePricingEvent{}).Where("organization_id = ?", f.org.ID).Count(&n)
	assert.Zero(t, n)

	var nilRec *usage.Recorder
	require.NoError(t, nilRec.Record(ctx, nil, msg, usage.Origin{}))
	require.NoError(t, nilRec.Settle(ctx, f.org.ID, f.account, "w"))
}

func TestRecordStatusEvent_DuplicateIsRefused(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	at := time.Now().UTC().Truncate(time.Second)
	ctx := context.Background()
	ok, err := f.rec.RecordStatusEvent(ctx, f.event("w1", "delivered", at, nil))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.rec.RecordStatusEvent(ctx, f.event("w1", "delivered", at, nil))
	require.NoError(t, err)
	assert.False(t, ok, "the same event twice is a duplicate")

	var ev models.MessagePricingEvent
	require.NoError(t, f.db.Where("wamid = ? AND organization_id = ?", "w1", f.org.ID).Take(&ev).Error)
	assert.False(t, ev.HasPricing, "an event without pricing is recorded as such")
	assert.Nil(t, ev.Pricing)
}

func TestSettle_EventBeforeTheRowIsSettledWhenTheWamidIsAttached(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	day := time.Now().UTC().AddDate(0, 0, -1)
	f.rate(t, "55", "utility", 0.0068, day, nil, "BRL")

	// the webhook arrives first: no row exists yet
	_, err := f.rec.RecordStatusEvent(ctx, f.event("early", "delivered", time.Now(), &usage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}))
	require.NoError(t, err)
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "early"), "nothing to settle yet is not an error")

	msg := f.message(t, "", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	require.NoError(t, f.rec.AttachWamid(ctx, f.org.ID, msg.ID, f.account, "early"))

	row := f.rows(t)[0]
	assert.Equal(t, "early", row.Wamid)
	assert.Equal(t, string(usage.StatePriced), row.BillingState)
	require.NotNil(t, row.EstimatedCost)
	assert.InDelta(t, 0.0068, *row.EstimatedCost, 1e-9)
	assert.Equal(t, "BRL", row.EstimatedCurrency)
	assert.NotNil(t, row.SettledAt)
	assert.Nil(t, row.MetaCost, "the Meta cost field stays untouched")
}

func TestSettle_IsIdempotentAndFollowsLateEvents(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	f.rate(t, "55", "utility", 1, time.Now().UTC().AddDate(0, 0, -1), nil, "BRL")
	msg := f.message(t, "w2", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))

	now := time.Now().UTC()
	_, _ = f.rec.RecordStatusEvent(ctx, f.event("w2", "delivered", now, nil))
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w2"))
	assert.Equal(t, string(usage.StateAwaitingPricing), f.rows(t)[0].BillingState)
	assert.Nil(t, f.rows(t)[0].Billable)

	_, _ = f.rec.RecordStatusEvent(ctx, f.event("w2", "sent", now.Add(-time.Second), &usage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}))
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w2"))
	first := f.rows(t)[0]
	assert.Equal(t, string(usage.StatePriced), first.BillingState)

	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w2"))
	second := f.rows(t)[0]
	assert.Equal(t, first.EstimatedCost, second.EstimatedCost)
	assert.Equal(t, first.SettledAt, second.SettledAt, "an unchanged settlement writes nothing")
	assert.Nil(t, second.RepricedAt)
}

func TestSettle_RowWithoutRateBecomesNoRateAndRepricesWhenTheRateAppears(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	ctx := context.Background()
	msg := f.message(t, "w3", models.DirectionOutgoing)
	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	_, _ = f.rec.RecordStatusEvent(ctx, f.event("w3", "delivered", time.Now(), &usage.Pricing{Billable: true, Category: "marketing"}))
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w3"))
	row := f.rows(t)[0]
	assert.Equal(t, string(usage.StateNoRate), row.BillingState)
	assert.Nil(t, row.EstimatedCost, "unknown, never a silent zero")

	f.rate(t, "*", "marketing", 0.5, time.Now().UTC().AddDate(0, 0, -1), nil, "USD")
	require.NoError(t, f.rec.Settle(ctx, f.org.ID, f.account, "w3"))
	row = f.rows(t)[0]
	assert.Equal(t, string(usage.StatePriced), row.BillingState)
	assert.Equal(t, "USD", row.EstimatedCurrency)
}

func TestMarkSendFailed(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	msg := f.message(t, "", models.DirectionOutgoing)
	ctx := context.Background()
	require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
	require.NoError(t, f.rec.MarkSendFailed(ctx, nil, msg.ID))
	row := f.rows(t)[0]
	assert.Equal(t, string(usage.StateSendFailed), row.BillingState)
	require.NotNil(t, row.EstimatedCost)
	assert.Zero(t, *row.EstimatedCost)
}

func TestUnlinkedRow_IsLinkedByRecordAndMergedByAttachWamid(t *testing.T) {
	ctx := context.Background()
	mkUnlinked := func(f *fixture, wamid string) {
		require.NoError(t, f.db.Create(&models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: f.account, Wamid: wamid,
			Direction: "outgoing", ActorType: "system", Quantity: 1, SentAt: time.Now(), BillingState: "pending", LinkState: "unlinked"}).Error)
	}

	t.Run("Record completes the unlinked row of the same wamid", func(t *testing.T) {
		f := newFixture(t, config.UsageConfig{})
		mkUnlinked(f, "late-1")
		msg := f.message(t, "late-1", models.DirectionOutgoing)
		require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
		rows := f.rows(t)
		require.Len(t, rows, 1, "linked, not duplicated")
		assert.Equal(t, string(usage.LinkLinked), rows[0].LinkState)
		assert.Equal(t, &msg.ID, rows[0].MessageID)
		assert.Equal(t, "agent", rows[0].ActorType)
	})

	t.Run("AttachWamid merges an unlinked row into the message's own row", func(t *testing.T) {
		f := newFixture(t, config.UsageConfig{})
		mkUnlinked(f, "late-2")
		msg := f.message(t, "", models.DirectionOutgoing)
		require.NoError(t, f.rec.Record(ctx, nil, msg, agentOrigin(nil)))
		require.NoError(t, f.rec.AttachWamid(ctx, f.org.ID, msg.ID, f.account, "late-2"))
		rows := f.rows(t)
		require.Len(t, rows, 1)
		assert.Equal(t, "late-2", rows[0].Wamid)
		assert.Equal(t, string(usage.LinkLinked), rows[0].LinkState)
	})

	t.Run("AttachWamid adopts the unlinked row when the message has none", func(t *testing.T) {
		f := newFixture(t, config.UsageConfig{})
		mkUnlinked(f, "late-3")
		msg := f.message(t, "", models.DirectionOutgoing)
		require.NoError(t, f.rec.AttachWamid(ctx, f.org.ID, msg.ID, f.account, "late-3"))
		rows := f.rows(t)
		require.Len(t, rows, 1)
		assert.Equal(t, &msg.ID, rows[0].MessageID)
		assert.Equal(t, string(usage.LinkLinked), rows[0].LinkState)
	})
}

func TestLookupRate(t *testing.T) {
	f := newFixture(t, config.UsageConfig{})
	d := func(m, day int) time.Time { return time.Date(2026, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	end := d(3, 31)
	f.rate(t, "55", "utility", 0.10, d(1, 1), &end, "BRL")
	f.rate(t, "55", "utility", 0.20, d(4, 1), nil, "BRL")
	f.rate(t, "*", "utility", 9.99, d(1, 1), nil, "USD")
	f.rate(t, "1", "marketing", 0.03, d(1, 1), nil, "USD")

	look := func(country, cat string, at time.Time) *models.WhatsAppRate {
		r, err := usage.LookupRate(f.db, f.org.ID, country, cat, at)
		require.NoError(t, err)
		return r
	}
	assert.InDelta(t, 0.10, look("55", "utility", d(1, 1)).Price, 1e-9, "valid_from is inclusive")
	assert.InDelta(t, 0.10, look("55", "utility", time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC)).Price, 1e-9, "valid_to is inclusive")
	assert.InDelta(t, 0.20, look("55", "utility", d(4, 1)).Price, 1e-9, "the next period takes over")
	assert.InDelta(t, 9.99, look("44", "utility", d(2, 1)).Price, 1e-9, "unknown country falls to *")
	assert.Equal(t, "USD", look("44", "utility", d(2, 1)).Currency)
	assert.Nil(t, look("44", "marketing", d(2, 1)), "no price for that category")
	assert.InDelta(t, 0.20, look("55", "utility", d(12, 1)).Price, 1e-9, "an open period has no end")
	assert.Nil(t, look("55", "utility", time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)), "before any validity")
}
