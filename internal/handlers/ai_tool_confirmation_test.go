package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// confirmEnv runs the real chatbot AI path with a simulated provider and a simulated WhatsApp.
type confirmEnv struct {
	app      *handlers.App
	tr       *seqTransport
	org      *models.Organization
	account  *models.WhatsAppAccount
	contact  *models.Contact
	session  *models.ChatbotSession
	settings *models.ChatbotSettings
	waMu     sync.Mutex
	waBodies []string
}

func callWith(id, name, argsJSON string) seqReply {
	esc, _ := json.Marshal(argsJSON)
	return seqReply{200, `{"choices":[{"message":{"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + string(esc) + `}}]},"finish_reason":"tool_calls"}],` + usage + `}`}
}

func newConfirmEnv(t *testing.T, replies ...seqReply) *confirmEnv {
	t.Helper()
	e := &confirmEnv{tr: &seqTransport{replies: replies}}
	wa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.waMu.Lock()
		e.waBodies = append(e.waBodies, string(b))
		e.waMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.mock_" + uuid.NewString()[:8]}}})
	}))
	t.Cleanup(wa.Close)

	e.app = newTestApp(t,
		withHTTPClient(&http.Client{Transport: e.tr, Timeout: 5 * time.Second}),
		withWhatsApp(whatsapp.NewWithBaseURL(testutil.NopLogger(), wa.URL)))
	require.NoError(t, e.app.DB.Exec("DELETE FROM ai_tool_confirmations").Error)
	t.Cleanup(func() { e.app.DB.Exec("DELETE FROM ai_tool_confirmations") })

	e.org = testutil.CreateTestOrganization(t, e.app.DB)
	e.account = testutil.CreateTestWhatsAppAccount(t, e.app.DB, e.org.ID)
	e.settings = encryptedSettings(t, e.app, e.org.ID, models.AIProviderOpenAI, "model-1", "sk-o")
	require.NoError(t, e.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", e.settings.ID).Update("is_enabled", true).Error)
	e.app.InvalidateChatbotSettingsCache(e.org.ID)
	e.settings, _ = e.app.GetChatbotSettingsCachedForTest(e.org.ID, "")

	e.contact = testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	e.session = &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: e.org.ID, ContactID: e.contact.ID,
		WhatsAppAccount: e.account.Name, PhoneNumber: e.contact.PhoneNumber, Status: models.SessionStatusActive, LastActivityAt: time.Now()}
	require.NoError(t, e.app.DB.Create(e.session).Error)

	// everything on, and the tool opted in
	e.app.Config.AITools.Enabled = true
	e.app.Config.AITools.WriteEnabled = true
	e.app.Config.AITools.Providers = []string{"openai"}
	require.NoError(t, e.app.DB.Create(&models.AIToolSetting{OrganizationID: e.org.ID, ToolName: "request_agent_transfer", Enabled: true}).Error)
	return e
}

func (e *confirmEnv) transfers(t *testing.T) []models.AgentTransfer {
	t.Helper()
	var rows []models.AgentTransfer
	require.NoError(t, e.app.DB.Where("contact_id = ?", e.contact.ID).Find(&rows).Error)
	return rows
}

func (e *confirmEnv) confirmations(t *testing.T) []models.AIToolConfirmation {
	t.Helper()
	var rows []models.AIToolConfirmation
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Order("proposed_at ASC").Find(&rows).Error)
	return rows
}

func (e *confirmEnv) sentToCustomer(t *testing.T) []string {
	t.Helper()
	var msgs []models.Message
	require.NoError(t, e.app.DB.Where("contact_id = ? AND direction = ?", e.contact.ID, models.DirectionOutgoing).Order("created_at ASC").Find(&msgs).Error)
	var out []string
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func offeredTools(body map[string]any) []string {
	var names []string
	if tools, ok := body["tools"].([]any); ok {
		for _, x := range tools {
			names = append(names, x.(map[string]any)["function"].(map[string]any)["name"].(string))
		}
	}
	return names
}

func buttonID(buttons []map[string]any, prefix string) string {
	for _, b := range buttons {
		if id, _ := b["id"].(string); strings.HasPrefix(id, prefix) {
			return id
		}
	}
	return ""
}

func TestAIConfirmation_ALyingModelNeverReachesTheCustomerAndTheTapDoesTheTransfer(t *testing.T) {
	e := newConfirmEnv(t,
		callWith("call_1", "request_agent_transfer", `{"reason":"pedido atrasado"}`),
		textReply("Pronto, já transferi você para um atendente!")) // the model claims it happened

	text, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero falar com uma pessoa")
	require.NoError(t, err)

	// what the customer would be sent is the SERVER's message, with the server's buttons
	assert.Contains(t, text, "Posso transferir seu atendimento para um atendente humano?")
	assert.NotContains(t, text, "já transferi", "the model's words are discarded")
	require.Len(t, buttons, 2)
	confirmID, declineID := buttonID(buttons, "aitc:"), buttonID(buttons, "aitd:")
	require.NotEmpty(t, confirmID)
	require.NotEmpty(t, declineID)
	assert.Equal(t, "Sim, transferir", buttons[0]["title"])

	// nothing was transferred; one proposal is waiting
	assert.Empty(t, e.transfers(t))
	confs := e.confirmations(t)
	require.Len(t, confs, 1)
	assert.Equal(t, models.AIConfirmationPending, confs[0].Status)
	assert.Equal(t, models.JSONB{"reason": "pedido atrasado"}, confs[0].Args)

	// the tool result the model saw says the transfer was NOT done
	last := e.tr.bodies[1]["messages"].([]any)
	toolMsg := last[len(last)-1].(map[string]any)
	assert.Contains(t, toolMsg["content"], `"transfer_performed":false`)
	assert.Contains(t, toolMsg["content"], "awaiting_customer_confirmation")

	// the customer taps "Sim, transferir"
	e.app.HandleAIToolTapForTest(e.account, e.contact, confirmID, "wamid.tap1")
	rows := e.transfers(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.TransferSourceAIConfirmed, rows[0].Source)
	assert.Nil(t, rows[0].TransferredByUserID)
	assert.Nil(t, rows[0].AgentID, "to the queue")
	assert.Contains(t, rows[0].Notes, "confirmado pelo cliente")
	assert.Contains(t, rows[0].Notes, "Motivo informado pela IA (não verificado): pedido atrasado")
	assert.Contains(t, e.sentToCustomer(t), "Pronto! Você entrou na fila de atendimento. Um atendente vai responder em breve.")

	conf := e.confirmations(t)[0]
	assert.Equal(t, models.AIConfirmationExecuted, conf.Status)
	require.NotNil(t, conf.TransferID)
	assert.Equal(t, rows[0].ID, *conf.TransferID)
	assert.Equal(t, e.contact.ID, *conf.ConfirmedByContactID)

	// a repeated tap or webhook redelivery: no second transfer, no second message
	before := len(e.sentToCustomer(t))
	e.app.HandleAIToolTapForTest(e.account, e.contact, confirmID, "wamid.tap1")
	e.app.HandleAIToolTapForTest(e.account, e.contact, declineID, "wamid.tap2")
	assert.Len(t, e.transfers(t), 1)
	assert.Len(t, e.sentToCustomer(t), before)
}

func TestAIConfirmation_DeclineAndForeignTap(t *testing.T) {
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	_, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero uma pessoa")
	require.NoError(t, err)
	confirmID, declineID := buttonID(buttons, "aitc:"), buttonID(buttons, "aitd:")

	// another customer taps this customer's button: nothing happens
	other := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	e.app.HandleAIToolTapForTest(e.account, other, confirmID, "wamid.x")
	assert.Empty(t, e.confirmations(t)[0].ConfirmWAMID)
	assert.Equal(t, models.AIConfirmationPending, e.confirmations(t)[0].Status)
	assert.Empty(t, e.transfers(t))

	e.app.HandleAIToolTapForTest(e.account, e.contact, declineID, "wamid.no")
	assert.Equal(t, models.AIConfirmationDeclined, e.confirmations(t)[0].Status)
	assert.Contains(t, e.sentToCustomer(t), "Tudo bem, seguimos por aqui.")
	assert.Empty(t, e.transfers(t))
	e.app.HandleAIToolTapForTest(e.account, e.contact, confirmID, "wamid.late")
	assert.Empty(t, e.transfers(t), "a declined request cannot be confirmed afterwards")
}

func TestAIConfirmation_TheToolIsOnlyOfferedWithTheWholeChain(t *testing.T) {
	cases := map[string]func(e *confirmEnv){
		"write_enabled off":     func(e *confirmEnv) { e.app.Config.AITools.WriteEnabled = false },
		"global off":            func(e *confirmEnv) { e.app.Config.AITools.Enabled = false },
		"no validated provider": func(e *confirmEnv) { e.app.Config.AITools.Providers = nil },
		"no opt-in": func(e *confirmEnv) {
			require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Delete(&models.AIToolSetting{}).Error)
		},
	}
	for name, off := range cases {
		e := newConfirmEnv(t, textReply("plain answer"))
		off(e)
		text, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero uma pessoa")
		require.NoError(t, err, name)
		assert.Equal(t, "plain answer", text, name)
		assert.Empty(t, buttons, name)
		assert.NotContains(t, offeredTools(e.tr.bodies[0]), "request_agent_transfer", name)
	}

	// and with everything on, it is offered
	e := newConfirmEnv(t, textReply("plain answer"))
	_, _, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "oi")
	require.NoError(t, err)
	assert.Contains(t, offeredTools(e.tr.bodies[0]), "request_agent_transfer")
}

func TestAIConfirmation_TheFlowNodeNeverGetsTheWriteTool(t *testing.T) {
	e := newConfirmEnv(t, textReply("plain answer"))
	out, err := e.app.GenerateAIResponseNodeForTest(e.settings, e.session, "quero uma pessoa")
	require.NoError(t, err)
	assert.Equal(t, "plain answer", out)
	assert.NotContains(t, offeredTools(e.tr.bodies[0]), "request_agent_transfer")
}

func TestAIConfirmation_ForcedCallInTheFlowNodeIsDeniedAndCreatesNothing(t *testing.T) {
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	// a read tool is enabled, so the node does run the loop; the write tool is not offered there, and the
	// model forces its name anyway
	require.NoError(t, e.app.DB.Create(&models.AIToolSetting{OrganizationID: e.org.ID, ToolName: "get_business_hours", Enabled: true}).Error)
	_, err := e.app.GenerateAIResponseNodeForTest(e.settings, e.session, "quero uma pessoa")
	require.NoError(t, err)
	assert.Empty(t, e.confirmations(t))
	assert.Empty(t, e.transfers(t))
	var rows []models.AIToolCall
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AIToolCallDenied, rows[0].Status)
	assert.Equal(t, "feature_not_allowed", rows[0].DenialReason)
}

func TestAIConfirmation_ReauthorizationAtTapTimeStopsAnythingThatChanged(t *testing.T) {
	cases := map[string]struct {
		change func(e *confirmEnv)
		reason string
	}{
		"write_enabled turned off": {func(e *confirmEnv) { e.app.Config.AITools.WriteEnabled = false }, "write_disabled"},
		"provider removed":         {func(e *confirmEnv) { e.app.Config.AITools.Providers = nil }, "provider_not_validated"},
		"opt-in removed": {func(e *confirmEnv) {
			require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Delete(&models.AIToolSetting{}).Error)
		}, "not_enabled"},
		"chatbot disabled": {func(e *confirmEnv) {
			require.NoError(t, e.app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", e.org.ID).Update("is_enabled", false).Error)
			e.app.InvalidateChatbotSettingsCache(e.org.ID)
		}, "chatbot_disabled"},
	}
	for name, c := range cases {
		e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
		_, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero uma pessoa")
		require.NoError(t, err, name)
		c.change(e) // between the proposal and the tap

		e.app.HandleAIToolTapForTest(e.account, e.contact, buttonID(buttons, "aitc:"), "wamid.tap")
		assert.Empty(t, e.transfers(t), name)
		conf := e.confirmations(t)[0]
		assert.Equal(t, models.AIConfirmationDenied, conf.Status, name)
		assert.Equal(t, c.reason, conf.DenialReason, name)
		assert.Nil(t, conf.ConfirmedAt, name+": the authorization was not consumed")
		assert.Contains(t, e.sentToCustomer(t), "Não foi possível concluir essa solicitação agora.", name)
	}
}

func TestAIConfirmation_OutsideBusinessHoursNoProposalAtAllAndNoTransferAtTheTap(t *testing.T) {
	// at the proposal: the model hears it, no buttons exist
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("Estamos fechados agora, tente mais tarde."))
	closed := models.ChatbotSettings{}
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).First(&closed).Error)
	require.NoError(t, e.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", closed.ID).Updates(map[string]any{
		"business_hours_enabled": true, "business_hours": closedDays(), "out_of_hours_message": "Voltamos amanhã.",
	}).Error)
	e.app.InvalidateChatbotSettingsCache(e.org.ID)
	e.settings, _ = e.app.GetChatbotSettingsCachedForTest(e.org.ID, "")

	text, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero uma pessoa")
	require.NoError(t, err)
	assert.Equal(t, "Estamos fechados agora, tente mais tarde.", text)
	assert.Empty(t, buttons)
	assert.Contains(t, e.tr.bodies[1]["messages"].([]any)[len(e.tr.bodies[1]["messages"].([]any))-1].(map[string]any)["content"], "outside_business_hours")
	assert.Empty(t, e.confirmations(t))
	assert.Empty(t, e.transfers(t))
}

func closedDays() models.JSONBArray {
	var h models.JSONBArray
	for d := 0; d < 7; d++ {
		h = append(h, map[string]any{"day": float64(d), "enabled": false, "start_time": "08:00", "end_time": "18:00"})
	}
	return h
}

func TestAIConfirmation_AProviderErrorAfterTheProposalStillGivesTheCustomerTheButtons(t *testing.T) {
	e := newConfirmEnv(t,
		callWith("call_1", "request_agent_transfer", `{"reason":"quero uma pessoa"}`),
		seqReply{500, `{"error":{"message":"boom"}}`}) // the second step of the loop fails

	text, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero falar com uma pessoa")
	require.NoError(t, err, "the caller must not take the fallback path: a proposal exists")
	assert.Contains(t, text, "atendente")
	assert.NotEmpty(t, buttonID(buttons, "aitc:"))
	assert.NotEmpty(t, buttonID(buttons, "aitd:"))
	assert.Len(t, buttons, 2)

	rows := e.confirmations(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AIConfirmationPending, rows[0].Status)
	assert.Empty(t, e.transfers(t))
}
