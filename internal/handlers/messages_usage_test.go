package handlers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	wausage "github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type usageSendFixture struct {
	app     *handlers.App
	mock    *mockWhatsAppServer
	org     *models.Organization
	account *models.WhatsAppAccount
	contact *models.Contact
}

func newUsageSendFixture(t *testing.T, cfg config.UsageConfig) *usageSendFixture {
	t.Helper()
	mock := newMockWhatsAppServer()
	t.Cleanup(mock.close)
	app := newMsgTestApp(t, mock)
	app.Usage = wausage.New(app.DB, cfg)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name),
		testutil.WithPhoneNumber("5511988887777"))
	return &usageSendFixture{app: app, mock: mock, org: org, account: account, contact: contact}
}

func (f *usageSendFixture) send(t *testing.T, ctx context.Context, opts handlers.MessageSendOptions, mutate func(*handlers.OutgoingMessageRequest)) *models.Message {
	t.Helper()
	req := handlers.OutgoingMessageRequest{Account: f.account, Contact: f.contact, Type: models.MessageTypeText, Content: "olá"}
	if mutate != nil {
		mutate(&req)
	}
	msg, err := f.app.SendOutgoingMessage(ctx, req, opts)
	require.NoError(t, err)
	f.app.WaitForBackgroundTasks()
	return msg
}

func (f *usageSendFixture) usageRows(t *testing.T) []models.MessageUsage {
	t.Helper()
	var rows []models.MessageUsage
	require.NoError(t, f.app.DB.Where("organization_id = ?", f.org.ID).Find(&rows).Error)
	return rows
}

func TestSendUsage_AgentSendIsRecordedWithTheWamid(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	agentID := makeAgentUser(t, f.app, f.org.ID, "Milena Souza")
	opts := handlers.DefaultSendOptions()
	opts.SentByUserID = &agentID

	msg := f.send(t, testutil.TestContext(t), opts, nil)

	rows := f.usageRows(t)
	require.Len(t, rows, 1, "one message, one row")
	r := rows[0]
	assert.Equal(t, &msg.ID, r.MessageID)
	assert.Equal(t, f.mock.nextMessageID, r.Wamid, "the accepted send's wamid is attached")
	assert.Equal(t, "agent", r.ActorType)
	assert.Equal(t, &agentID, r.ActorUserID)
	assert.Equal(t, "outgoing", r.Direction)
	assert.Equal(t, "55", r.RecipientCountry)
	assert.Equal(t, "pending", r.BillingState)
	assert.Equal(t, "linked", r.LinkState)
	assert.Nil(t, r.Billable)
}

func TestSendUsage_ExplicitOriginWinsAndCarriesTheOccurrence(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	agentID := makeAgentUser(t, f.app, f.org.ID, "Ana")
	occ := uuid.New()
	opts := handlers.DefaultSendOptions()
	opts.SentByUserID = &agentID

	f.send(t, testutil.TestContext(t), opts, func(r *handlers.OutgoingMessageRequest) {
		r.Origin = wausage.Origin{ActorType: wausage.ActorAgent, OccurrenceID: &occ, Detail: "occurrence_reply"}
	})

	r := f.usageRows(t)[0]
	assert.Equal(t, &occ, r.OccurrenceID)
	assert.Equal(t, "occurrence_reply", r.OriginDetail)
	assert.Equal(t, &agentID, r.ActorUserID, "the sending user completes an agent origin")
}

func TestSendUsage_OriginFromTheContext(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	flow := uuid.New()
	ctx := wausage.WithOrigin(testutil.TestContext(t), wausage.Origin{ActorType: wausage.ActorFlow, FlowID: &flow, FlowNodeID: "n1"})

	f.send(t, ctx, handlers.ChatbotSendOptions(), nil)

	r := f.usageRows(t)[0]
	assert.Equal(t, "flow", r.ActorType)
	assert.Equal(t, &flow, r.FlowID)
	assert.Equal(t, "n1", r.FlowNodeID)
	assert.False(t, r.OriginInferred)
}

func TestSendUsage_NoOriginIsUnclassified(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	f.send(t, testutil.TestContext(t), handlers.ChatbotSendOptions(), nil)
	r := f.usageRows(t)[0]
	assert.Equal(t, "system", r.ActorType)
	assert.Equal(t, "unclassified", r.OriginDetail)
	assert.True(t, r.OriginInferred)
}

func TestSendUsage_RefusedByTheAPIIsSendFailed(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	f.mock.returnError = true
	req := handlers.OutgoingMessageRequest{Account: f.account, Contact: f.contact, Type: models.MessageTypeText, Content: "x"}
	_, _ = f.app.SendOutgoingMessage(testutil.TestContext(t), req, handlers.ChatbotSendOptions())
	f.app.WaitForBackgroundTasks()

	rows := f.usageRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "send_failed", rows[0].BillingState)
	assert.Empty(t, rows[0].Wamid)
	require.NotNil(t, rows[0].EstimatedCost)
	assert.Zero(t, *rows[0].EstimatedCost)
}

func TestSendUsage_SettlesWhenTheEventArrivedBeforeTheWamidWasAttached(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	require.NoError(t, f.app.DB.Create(&models.WhatsAppRate{OrganizationID: f.org.ID, Country: "55", Category: "utility",
		Price: 0.07, Currency: "BRL", ValidFrom: time.Now().UTC().AddDate(0, 0, -2)}).Error)
	// the webhook of the message that is about to be sent was already recorded
	_, err := f.app.Usage.RecordStatusEvent(context.Background(), wausage.StatusEvent{
		OrganizationID: f.org.ID, WhatsAppAccount: f.account.Name, Wamid: f.mock.nextMessageID, Status: "delivered",
		EventAt: time.Now(), Pricing: &wausage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}})
	require.NoError(t, err)

	f.send(t, testutil.TestContext(t), handlers.ChatbotSendOptions(), nil)

	r := f.usageRows(t)[0]
	assert.Equal(t, "priced", r.BillingState)
	require.NotNil(t, r.EstimatedCost)
	assert.InDelta(t, 0.07, *r.EstimatedCost, 1e-9)
}

// With recording off the send behaves exactly as before and nothing is written.
func TestSendUsage_DisabledChangesNothingAboutTheSend(t *testing.T) {
	off := false
	f := newUsageSendFixture(t, config.UsageConfig{RecordEnabled: &off})
	msg := f.send(t, testutil.TestContext(t), handlers.ChatbotSendOptions(), nil)

	assert.Empty(t, f.usageRows(t))
	require.Len(t, f.mock.sentMessages, 1)
	var stored models.Message
	require.NoError(t, f.app.DB.First(&stored, msg.ID).Error)
	assert.Equal(t, models.MessageStatusSent, stored.Status)
	assert.Equal(t, f.mock.nextMessageID, stored.WhatsAppMessageID)
}

func TestSendUsage_WithoutARecorderBehavesAsBefore(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	f.app.Usage = nil
	msg := f.send(t, testutil.TestContext(t), handlers.ChatbotSendOptions(), nil)
	var stored models.Message
	require.NoError(t, f.app.DB.First(&stored, msg.ID).Error)
	assert.Equal(t, models.MessageStatusSent, stored.Status)
	assert.Empty(t, f.usageRows(t))
}

// F1: a recorder whose database is gone must not stop, delay-fail or change the send.
func TestSendUsage_BrokenRecorderIsFailOpen(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	require.NotEmpty(t, dsn)
	broken, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := broken.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close()) // every query on it now fails

	f := newUsageSendFixture(t, config.UsageConfig{})
	f.app.Usage = wausage.New(broken, config.UsageConfig{})

	agentID := makeAgentUser(t, f.app, f.org.ID, "Lia")
	opts := handlers.DefaultSendOptions()
	opts.SentByUserID = &agentID
	msg := f.send(t, testutil.TestContext(t), opts, nil)

	var stored models.Message
	require.NoError(t, f.app.DB.First(&stored, msg.ID).Error)
	assert.Equal(t, models.MessageStatusSent, stored.Status, "the message was sent and finalized")
	assert.Equal(t, f.mock.nextMessageID, stored.WhatsAppMessageID)
	require.Len(t, f.mock.sentMessages, 1, "and it reached WhatsApp")
	var c models.Contact
	require.NoError(t, f.app.DB.First(&c, f.contact.ID).Error)
	assert.Equal(t, models.ContactStatusInProgress, c.ContactStatus, "the status transition still happened")
	assert.Empty(t, f.usageRows(t))
}
