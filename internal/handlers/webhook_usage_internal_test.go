package handlers

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type statusFixture struct {
	app     *App
	org     *models.Organization
	account *models.WhatsAppAccount
	msg     *models.Message
	wamid   string
	base    time.Time
}

// newStatusFixture builds an outgoing message already sent (status `sent`, with its
// wamid) and its ledger row, plus a BRL price for utility messages to Brazil.
func newStatusFixture(t *testing.T, cfg config.UsageConfig) *statusFixture {
	t.Helper()
	app := webhookTestApp(t)
	app.Usage = usage.New(app.DB, cfg)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511966665555"),
		testutil.WithContactAccount(account.Name))
	wamid := "wamid.st-" + uuid.NewString()[:10]
	msg := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: account.Name,
		ContactID: contact.ID, WhatsAppMessageID: wamid, Direction: models.DirectionOutgoing,
		MessageType: models.MessageTypeText, Content: "x", Status: models.MessageStatusSent,
	}
	require.NoError(t, app.DB.Create(msg).Error)
	require.NoError(t, app.Usage.Record(context.Background(), nil, msg, usage.Origin{ActorType: usage.ActorFlow}))
	require.NoError(t, app.DB.Create(&models.WhatsAppRate{OrganizationID: org.ID, Country: "55", Category: "utility",
		Price: 0.05, Currency: "BRL", ValidFrom: time.Now().UTC().AddDate(0, 0, -3)}).Error)
	return &statusFixture{app: app, org: org, account: account, msg: msg, wamid: wamid, base: time.Now().Add(-time.Minute)}
}

func (f *statusFixture) status(name string, secs int, pricing *struct {
	Billable     bool   `json:"billable"`
	PricingModel string `json:"pricing_model"`
	Category     string `json:"category"`
}) WebhookStatus {
	return WebhookStatus{ID: f.wamid, Status: name, RecipientID: "5511966665555",
		Timestamp: strconv.FormatInt(f.base.Add(time.Duration(secs)*time.Second).Unix(), 10), Pricing: pricing}
}

type pricingBlock = struct {
	Billable     bool   `json:"billable"`
	PricingModel string `json:"pricing_model"`
	Category     string `json:"category"`
}

func utilityPricing() *pricingBlock {
	return &pricingBlock{Billable: true, PricingModel: "PMP", Category: "utility"}
}

func (f *statusFixture) send(s WebhookStatus) { f.app.processStatusUpdate(f.account.PhoneID, s) }

func (f *statusFixture) row(t *testing.T) models.MessageUsage {
	t.Helper()
	var r models.MessageUsage
	require.NoError(t, f.app.DB.Where("organization_id = ? AND wamid = ?", f.org.ID, f.wamid).Take(&r).Error)
	return r
}

func (f *statusFixture) events(t *testing.T) []models.MessagePricingEvent {
	t.Helper()
	var evs []models.MessagePricingEvent
	require.NoError(t, f.app.DB.Where("organization_id = ? AND wamid = ?", f.org.ID, f.wamid).Order("event_at").Find(&evs).Error)
	return evs
}

func (f *statusFixture) messageStatus(t *testing.T) models.MessageStatus {
	t.Helper()
	var m models.Message
	require.NoError(t, f.app.DB.First(&m, f.msg.ID).Error)
	return m.Status
}

func TestStatusUsage_DeliveredWithPricingIsPriced(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("sent", 1, nil))
	f.send(f.status("delivered", 2, utilityPricing()))

	r := f.row(t)
	assert.Equal(t, "priced", r.BillingState)
	require.NotNil(t, r.Billable)
	assert.True(t, *r.Billable)
	assert.Equal(t, "utility", r.BillingCategory)
	require.NotNil(t, r.EstimatedCost)
	assert.InDelta(t, 0.05, *r.EstimatedCost, 1e-9)
	assert.Equal(t, "BRL", r.EstimatedCurrency)
	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t))
	assert.Len(t, f.events(t), 2)
}

func TestStatusUsage_AnyArrivalOrderGivesTheSameLedgerAndMessageStatus(t *testing.T) {
	orders := [][]string{{"sent", "delivered", "read"}, {"sent", "read", "delivered"}, {"delivered", "sent", "read"},
		{"delivered", "read", "sent"}, {"read", "sent", "delivered"}, {"read", "delivered", "sent"}}
	secs := map[string]int{"sent": 1, "delivered": 2, "read": 3}
	for _, order := range orders {
		t.Run(order[0]+"-"+order[1]+"-"+order[2], func(t *testing.T) {
			f := newStatusFixture(t, config.UsageConfig{})
			require.NoError(t, f.app.DB.Model(f.msg).Update("status", models.MessageStatusPending).Error)
			for _, s := range order {
				f.send(f.status(s, secs[s], utilityPricing()))
			}
			r := f.row(t)
			assert.Equal(t, "priced", r.BillingState)
			require.NotNil(t, r.EstimatedCost)
			assert.InDelta(t, 0.05, *r.EstimatedCost, 1e-9)
			assert.Nil(t, r.RepricedAt)
			assert.Empty(t, r.MetaPricingHistory)
			assert.Equal(t, models.MessageStatusRead, f.messageStatus(t), "the message status never regresses")
			assert.Len(t, f.events(t), 3)
		})
	}
}

func TestStatusUsage_DuplicateEventChangesNothing(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("delivered", 2, utilityPricing()))
	first := f.row(t)

	f.send(f.status("delivered", 2, utilityPricing()))

	assert.Len(t, f.events(t), 1, "the repeated event is not stored twice")
	second := f.row(t)
	assert.Equal(t, first.SettledAt, second.SettledAt, "and nothing is recalculated")
	assert.Equal(t, first.EstimatedCost, second.EstimatedCost)
}

func TestStatusUsage_EventWithoutPricingIsRecordedAndProvesNothing(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("sent", 1, nil))

	evs := f.events(t)
	require.Len(t, evs, 1)
	assert.False(t, evs[0].HasPricing)
	assert.Nil(t, evs[0].Pricing)
	r := f.row(t)
	assert.Equal(t, "pending", r.BillingState)
	assert.Nil(t, r.Billable, "unknown, not false")
}

func TestStatusUsage_DeliveredWithoutPricingAwaitsItAndLatePricingFixesIt(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("sent", 1, nil))
	f.send(f.status("delivered", 2, nil))
	r := f.row(t)
	assert.Equal(t, "awaiting_pricing", r.BillingState)
	assert.Nil(t, r.Billable)
	assert.Nil(t, r.EstimatedCost)

	// a late `sent` (not a progression for the message) still brings the pricing
	f.send(f.status("sent", 5, utilityPricing()))
	r = f.row(t)
	assert.Equal(t, "priced", r.BillingState)
	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t), "the late sent did not move the message status back")
}

func TestStatusUsage_NotBillableCostsZero(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("delivered", 2, &pricingBlock{Billable: false, PricingModel: "PMP", Category: "service"}))
	r := f.row(t)
	assert.Equal(t, "not_billable", r.BillingState)
	require.NotNil(t, r.EstimatedCost)
	assert.Zero(t, *r.EstimatedCost)
	require.NotNil(t, r.Billable)
	assert.False(t, *r.Billable)
}

func TestStatusUsage_DifferentLaterPricingRecalculatesAndKeepsTheHistory(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.send(f.status("delivered", 2, utilityPricing()))
	f.send(f.status("read", 9, &pricingBlock{Billable: true, PricingModel: "PMP", Category: "marketing"}))

	r := f.row(t)
	assert.Equal(t, "marketing", r.BillingCategory)
	assert.Equal(t, "no_rate", r.BillingState, "no marketing price was registered")
	assert.Len(t, r.MetaPricingHistory, 1)
	assert.NotNil(t, r.RepricedAt)
}

func TestStatusUsage_FailedWithAnError(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	st := f.status("failed", 2, nil)
	st.Errors = []WebhookStatusError{{Code: 131026, Title: "Message undeliverable"}}
	f.send(st)

	r := f.row(t)
	assert.Equal(t, "failed", r.BillingState)
	require.NotNil(t, r.Billable)
	assert.False(t, *r.Billable)
	evs := f.events(t)
	require.Len(t, evs, 1)
	assert.Equal(t, 131026, evs[0].ErrorCode)
	assert.Equal(t, models.MessageStatusFailed, f.messageStatus(t))
}

func TestStatusUsage_ConversationIsKept(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	st := f.status("delivered", 2, utilityPricing())
	st.Conversation = &struct {
		ID     string `json:"id"`
		Origin struct {
			Type string `json:"type"`
		} `json:"origin"`
	}{ID: "conv-42"}
	f.send(st)
	assert.Equal(t, "conv-42", f.row(t).ConversationID)
}

func TestStatusUsage_EventBeforeTheRowIsSettledWhenTheRowAppears(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	// a second message whose webhook arrives before any ledger row exists
	wamid := "wamid.early-" + uuid.NewString()[:8]
	st := f.status("delivered", 2, utilityPricing())
	st.ID = wamid
	f.app.processStatusUpdate(f.account.PhoneID, st) // no message, no row: nothing to settle, no error

	msg := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, WhatsAppAccount: f.account.Name,
		ContactID: f.msg.ContactID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "y", Status: models.MessageStatusPending}
	require.NoError(t, f.app.DB.Create(msg).Error)
	require.NoError(t, f.app.Usage.Record(context.Background(), nil, msg, usage.Origin{ActorType: usage.ActorFlow}))
	require.NoError(t, f.app.Usage.AttachWamid(context.Background(), f.org.ID, msg.ID, f.account.Name, wamid))

	var r models.MessageUsage
	require.NoError(t, f.app.DB.Where("wamid = ?", wamid).Take(&r).Error)
	assert.Equal(t, "priced", r.BillingState)
}

func TestStatusUsage_OtherOrganizationsAreNotTouched(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	otherOrg := testutil.CreateTestOrganization(t, f.app.DB)
	require.NoError(t, f.app.DB.Create(&models.MessageUsage{OrganizationID: otherOrg.ID, WhatsAppAccount: f.account.Name,
		Wamid: f.wamid, Direction: "outgoing", ActorType: "system", Quantity: 1, SentAt: time.Now(),
		BillingState: "pending", LinkState: "linked"}).Error)

	f.send(f.status("delivered", 2, utilityPricing()))

	var other models.MessageUsage
	require.NoError(t, f.app.DB.Where("organization_id = ? AND wamid = ?", otherOrg.ID, f.wamid).Take(&other).Error)
	assert.Equal(t, "pending", other.BillingState)
}

func TestStatusUsage_DisabledLeavesTheWebhookExactlyAsBefore(t *testing.T) {
	off := false
	f := newStatusFixture(t, config.UsageConfig{RecordEnabled: &off})
	f.app.Usage = usage.New(f.app.DB, config.UsageConfig{RecordEnabled: &off})
	require.NoError(t, f.app.DB.Model(f.msg).Update("status", models.MessageStatusSent).Error)

	f.send(f.status("delivered", 2, utilityPricing()))

	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t), "the message status was updated as always")
	assert.Empty(t, f.events(t))
}

func TestStatusUsage_WithoutARecorderBehavesAsBefore(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	f.app.Usage = nil
	f.send(f.status("delivered", 2, utilityPricing()))
	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t))
}

// F1 on the webhook: a recorder whose database is gone must not change what the
// webhook does for the message.
func TestStatusUsage_BrokenRecorderIsFailOpen(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	dsn := os.Getenv("TEST_DATABASE_URL")
	broken, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := broken.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	f.app.Usage = usage.New(broken, config.UsageConfig{})

	require.NotPanics(t, func() { f.send(f.status("delivered", 2, utilityPricing())) })

	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t), "the message status still progressed")
}

// A status for an account we do not know is ignored by the ledger without error.
func TestStatusUsage_UnknownPhoneNumberIDIsIgnored(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	require.NotPanics(t, func() { f.app.processStatusUpdate("phone-does-not-exist", f.status("delivered", 2, utilityPricing())) })
	assert.Empty(t, f.events(t))
}

func TestStatusUsage_PanelSwitchOffRecordsNoEventAndTheMessageStillProgresses(t *testing.T) {
	f := newStatusFixture(t, config.UsageConfig{})
	_, err := f.app.Usage.SetRecording(context.Background(), f.org.ID, false, time.Now())
	require.NoError(t, err)

	f.send(f.status("delivered", 2, utilityPricing()))

	assert.Equal(t, models.MessageStatusDelivered, f.messageStatus(t), "the webhook does what it always did")
	assert.Empty(t, f.events(t), "but no Meta event is kept while the measurement is off")
	assert.Equal(t, "pending", f.row(t).BillingState, "and the existing row is frozen")
}
