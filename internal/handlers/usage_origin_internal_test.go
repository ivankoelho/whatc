package handlers

import (
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

type originFixture struct {
	app     *App
	org     *models.Organization
	account *models.WhatsAppAccount
	contact *models.Contact
}

func newOriginFixture(t *testing.T) *originFixture {
	t.Helper()
	app := newProcessorTestApp(t)
	app.Usage = usage.New(app.DB, config.UsageConfig{})
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511977776666"),
		testutil.WithContactAccount(account.Name))
	return &originFixture{app: app, org: org, account: account, contact: contact}
}

func (f *originFixture) rows(t *testing.T) []models.MessageUsage {
	t.Helper()
	var rows []models.MessageUsage
	require.NoError(t, f.app.DB.Where("organization_id = ?", f.org.ID).Order("created_at").Find(&rows).Error)
	return rows
}

func (f *originFixture) only(t *testing.T) models.MessageUsage {
	t.Helper()
	rows := f.rows(t)
	require.Len(t, rows, 1)
	return rows[0]
}

func TestUsageOrigin_BotHelpers(t *testing.T) {
	t.Run("keyword reply", func(t *testing.T) {
		f := newOriginFixture(t)
		require.NoError(t, f.app.sendAndSaveTextMessage(botCtx("keyword"), f.account, f.contact, "oi"))
		r := f.only(t)
		assert.Equal(t, "flow", r.ActorType)
		assert.Equal(t, "keyword", r.OriginDetail)
		assert.Equal(t, "text", r.MessageType)
		assert.False(t, r.OriginInferred)
	})
	t.Run("AI reply", func(t *testing.T) {
		f := newOriginFixture(t)
		require.NoError(t, f.app.sendAndSaveTextMessage(aiCtx("chatbot_reply"), f.account, f.contact, "oi"))
		r := f.only(t)
		assert.Equal(t, "ai", r.ActorType)
		assert.Equal(t, "chatbot_reply", r.OriginDetail)
	})
	t.Run("reply buttons keep the origin", func(t *testing.T) {
		f := newOriginFixture(t)
		btns := []map[string]any{{"id": "a", "title": "A"}, {"id": "b", "title": "B"}}
		require.NoError(t, f.app.sendAndSaveInteractiveButtons(botCtx("default_response"), f.account, f.contact, "escolha", btns))
		r := f.only(t)
		assert.Equal(t, "flow", r.ActorType)
		assert.Equal(t, "default_response", r.OriginDetail)
		assert.Equal(t, "interactive", r.MessageType)
	})
	t.Run("CTA buttons sent through the buttons helper keep the origin", func(t *testing.T) {
		f := newOriginFixture(t)
		btns := []map[string]any{{"type": "url", "title": "Site", "url": "https://example.com"}}
		require.NoError(t, f.app.sendAndSaveInteractiveButtons(botCtx("keyword"), f.account, f.contact, "veja", btns))
		assert.Equal(t, "keyword", f.only(t).OriginDetail)
	})
	t.Run("WhatsApp Flow message", func(t *testing.T) {
		f := newOriginFixture(t)
		require.NoError(t, f.app.sendAndSaveFlowMessage(botCtx("keyword"), f.account, f.contact, "flow-1", "h", "corpo", "cta", "tok", "SCREEN"))
		assert.Equal(t, "flow", f.only(t).MessageType)
	})
}

func TestUsageOrigin_GraphNodeCarriesFlowAndNode(t *testing.T) {
	f := newOriginFixture(t)
	flowID := uuid.New()
	nctx := &chatNodeCtx{account: f.account, contact: f.contact, session: &models.ChatbotSession{CurrentFlowID: &flowID}}

	require.NoError(t, f.app.sendAndSaveTextMessage(f.app.nodeCtx(nctx, &ChatNode{ID: "node-7"}, usage.ActorFlow, ""), f.account, f.contact, "msg"))
	require.NoError(t, f.app.sendAndSaveTextMessage(f.app.nodeCtx(nctx, &ChatNode{ID: "node-9"}, usage.ActorAI, "chatbot_flow_node"), f.account, f.contact, "ia"))

	rows := f.rows(t)
	require.Len(t, rows, 2)
	byNode := map[string]models.MessageUsage{}
	for _, r := range rows {
		byNode[r.FlowNodeID] = r
	}
	assert.Equal(t, "flow", byNode["node-7"].ActorType)
	assert.Equal(t, &flowID, byNode["node-7"].FlowID)
	assert.Equal(t, "ai", byNode["node-9"].ActorType)
	assert.Equal(t, "chatbot_flow_node", byNode["node-9"].OriginDetail)
}

func TestUsageOrigin_SLANoticeIsSystem(t *testing.T) {
	f := newOriginFixture(t)
	proc := NewSLAProcessor(f.app, time.Minute)
	proc.sendSLATextToCustomer(models.AgentTransfer{
		OrganizationID: f.org.ID, WhatsAppAccount: f.account.Name, ContactID: f.contact.ID, PhoneNumber: f.contact.PhoneNumber,
	}, "SLA warning", "aviso")
	r := f.only(t)
	assert.Equal(t, "system", r.ActorType)
	assert.Equal(t, "sla", r.OriginDetail)
}

func TestUsageOrigin_IncomingIsContactAndNotBillable(t *testing.T) {
	f := newOriginFixture(t)
	f.app.saveIncomingMessage(f.account, f.contact, "wamid.in-"+uuid.NewString()[:8], "text", "oi", nil, "")
	r := f.only(t)
	assert.Equal(t, "incoming", r.Direction)
	assert.Equal(t, "contact", r.ActorType)
	assert.Equal(t, "not_billable", r.BillingState)
	require.NotNil(t, r.Billable)
	assert.False(t, *r.Billable)
}

func TestUsageOrigin_BusinessAppEchoIsExternalApp(t *testing.T) {
	f := newOriginFixture(t)
	var msg IncomingTextMessage
	msg.ID = "wamid.echo-" + uuid.NewString()[:8]
	msg.To = f.contact.PhoneNumber
	msg.From = f.account.PhoneID
	msg.Type = "text"
	msg.Text = &struct {
		Body string `json:"body"`
	}{Body: "mandei pelo celular"}

	f.app.processMessageEcho(f.account.PhoneID, msg)

	r := f.only(t)
	assert.Equal(t, "outgoing", r.Direction)
	assert.Equal(t, "external_app", r.ActorType)
	assert.Equal(t, msg.ID, r.Wamid, "the echo already carries its wamid")
	assert.Equal(t, "linked", r.LinkState)
}

// F1/F2 on the receive path: nothing about the incoming message changes when recording is off.
func TestUsageOrigin_IncomingWithRecordingOffStillSavesTheMessage(t *testing.T) {
	f := newOriginFixture(t)
	off := false
	f.app.Usage = usage.New(f.app.DB, config.UsageConfig{RecordEnabled: &off})
	wamid := "wamid.in-" + uuid.NewString()[:8]
	f.app.saveIncomingMessage(f.account, f.contact, wamid, "text", "oi", nil, "")

	assert.Empty(t, f.rows(t))
	var m models.Message
	require.NoError(t, f.app.DB.Where("whats_app_message_id = ?", wamid).First(&m).Error)
	assert.Equal(t, models.MessageStatusReceived, m.Status)
}
