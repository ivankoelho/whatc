package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// What a protocol send leaves behind in every outcome, how a send whose result row is
// missing is found and matched to what really happened, and what is (and is not)
// protected against a second message reaching the customer.

// pendingSQL is the reconciliation query of the R2 report. It is read-only: it lists
// "attempted" rows that have no result row and says what the message row shows.
const pendingSQL = `
WITH rec AS (
  SELECT id, created_at, resource_id AS occurrence_id, user_name,
         jsonb_path_query_first(changes, '$[*] ? (@.field == "send_id").new_value')         #>> '{}' AS send_id,
         jsonb_path_query_first(changes, '$[*] ? (@.field == "result").new_value')          #>> '{}' AS result,
         jsonb_path_query_first(changes, '$[*] ? (@.field == "message_id").new_value')      #>> '{}' AS message_id,
         jsonb_path_query_first(changes, '$[*] ? (@.field == "content").new_value')         #>> '{}' AS content
  FROM audit_logs
  WHERE resource_type = 'occurrence_message' AND organization_id = @org)
SELECT a.send_id, a.content, m.status AS message_status,
       CASE WHEN m.id IS NULL THEN 'never_sent'
            WHEN m.status IN ('sent','delivered','read') THEN 'sent'
            WHEN m.status = 'failed' THEN 'failed'
            ELSE 'in_flight_or_unknown' END AS resolution
FROM rec a
LEFT JOIN rec r ON r.send_id = a.send_id AND r.result <> 'attempted'
LEFT JOIN messages m ON m.id = a.message_id::uuid
WHERE a.result = 'attempted' AND r.id IS NULL
ORDER BY a.created_at`

// uncertainSQL lists the sends whose outcome the provider never confirmed: these need a
// person to look at the conversation, and are never repeated by the system.
const uncertainSQL = `
SELECT jsonb_path_query_first(changes, '$[*] ? (@.field == "content").new_value') #>> '{}' AS content,
       jsonb_path_query_first(changes, '$[*] ? (@.field == "send_id").new_value') #>> '{}' AS send_id
FROM audit_logs
WHERE resource_type = 'occurrence_message' AND organization_id = @org
  AND jsonb_path_exists(changes, '$[*] ? (@.field == "result" && @.new_value == "uncertain")')
ORDER BY created_at`

type pendingRow struct {
	SendID        string
	Content       string
	MessageStatus *string
	Resolution    string
}

func (w *msgWorld) pending(t *testing.T) []pendingRow {
	t.Helper()
	var rows []pendingRow
	require.NoError(t, w.app.DB.Raw(withOrg(pendingSQL, w.org.ID)).Scan(&rows).Error)
	return rows
}

// sendEnvelope is the answer of send-protocol, as the caller sees it.
type sendEnvelope struct {
	Status    string         `json:"status"`
	Message   string         `json:"message"`
	ErrorType string         `json:"error_type"`
	Data      map[string]any `json:"data"`
}

func (w *msgWorld) sendText(t *testing.T, user *models.User, text string) (int, sendEnvelope) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"message": text})
	code, resp := w.protocol(t, user, w.occ, body)
	var out sendEnvelope
	_ = json.Unmarshal(resp, &out)
	return code, out
}

// shortTimeoutClient makes a provider answer that takes longer than the client waits
// look like what it is in production: no answer.
func (w *msgWorld) shortTimeoutClient(timeout time.Duration) {
	c := whatsapp.NewWithTimeout(testutil.NopLogger(), timeout)
	c.HTTPClient = &http.Client{Timeout: timeout, Transport: &testServerTransport{serverURL: w.m.server.URL}}
	w.app.WhatsApp = c
}

func (w *msgWorld) mockHas(text string) int {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	n := 0
	for _, m := range w.m.sentMessages {
		if tx, ok := m["text"].(map[string]any); ok && tx["body"] == text {
			n++
		}
	}
	return n
}

func TestProtocolSend_MessageIDIsAssignedBeforeTheSend(t *testing.T) {
	w := newMsgWorld(t)
	code, _ := w.sendText(t, w.agent, "id atribuido antes")
	require.Equal(t, fasthttp.StatusOK, code)
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, changeOf(t, rows[0], "send_id"), changeOf(t, rows[1], "send_id"), "both rows name the same send")
	assert.Equal(t, changeOf(t, rows[0], "message_id"), changeOf(t, rows[1], "message_id"), "the id is the same before and after")
	var msg models.Message
	require.NoError(t, w.app.DB.Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).First(&msg).Error)
	assert.Equal(t, msg.ID.String(), changeOf(t, rows[0], "message_id"), "...and it is the id of the message row")
}

// The four ways a send can end, side by side, and what the reconciliation query says
// about each. Only the one whose RESULT row could not be written is "pending".
func TestProtocolSend_StatesAndReconciliation(t *testing.T) {
	w := newMsgWorld(t)
	rowsOf := func(text string) []models.AuditLog {
		var out []models.AuditLog
		for _, r := range auditRows(t, w.app.DB, w.occ.ID) {
			if changeOf(t, r, "content") == text {
				out = append(out, r)
			}
		}
		return out
	}
	results := func(text string) []any {
		var out []any
		for _, r := range rowsOf(text) {
			out = append(out, changeOf(t, r, "result"))
		}
		return out
	}

	// 1. Accepted, result recorded.
	code, _ := w.sendText(t, w.agent, "estado 1 aceito")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, []any{"attempted", "sent"}, results("estado 1 aceito"))

	// 2. Provably refused by the provider.
	w.m.returnError, w.m.errorMessage = true, "recusado"
	code, env := w.sendText(t, w.agent, "estado 2 recusado")
	assert.Equal(t, fasthttp.StatusBadGateway, code, "a proven refusal is an error: nothing was delivered, trying again is safe")
	assert.Equal(t, "failed", env.Data["result"])
	assert.Equal(t, true, env.Data["confirmed"])
	assert.NotEmpty(t, env.Data["send_id"])
	assert.Equal(t, []any{"attempted", "failed"}, results("estado 2 recusado"))
	w.m.returnError = false

	// 3. Accepted by the provider, but the record AFTER the send cannot be written.
	var creates atomic.Int32
	const cb = "test:fail_result_row"
	require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
		// the "attempted" row is the first create of this send, the result row the second
		if tx.Statement.Table == "audit_logs" && creates.Add(1)%2 == 0 {
			_ = tx.AddError(errors.New("simulated audit write failure"))
		}
	}))
	code, _ = w.sendText(t, w.agent, "estado 3 aceito sem resultado")
	_ = w.app.DB.Callback().Create().Remove(cb)
	assert.Equal(t, fasthttp.StatusOK, code, "the message went out: the answer must not become an error that invites a resend")
	assert.Equal(t, []any{"attempted"}, results("estado 3 aceito sem resultado"))
	assert.Equal(t, 1, w.mockHas("estado 3 aceito sem resultado"))

	// 4. No answer in time, although the provider accepted it.
	w.shortTimeoutClient(150 * time.Millisecond)
	w.m.mu.Lock()
	w.m.delay = 500 * time.Millisecond
	w.m.mu.Unlock()
	code, env = w.sendText(t, w.agent, "estado 4 resposta tardia")
	assert.Equal(t, fasthttp.StatusAccepted, code, "202, not an error: an error invites the caller to send it again")
	assert.Equal(t, "uncertain", env.Data["result"])
	assert.Equal(t, false, env.Data["sent"], "202 is not a delivery confirmation")
	assert.Equal(t, false, env.Data["confirmed"])
	assert.Contains(t, env.Message, "pending confirmation")
	assert.NotEmpty(t, env.Data["send_id"])
	assert.NotEmpty(t, env.Data["message_id"])
	assert.Equal(t, []any{"attempted", "uncertain"}, results("estado 4 resposta tardia"))
	require.Eventually(t, func() bool { return w.mockHas("estado 4 resposta tardia") == 1 }, 2*time.Second, 20*time.Millisecond,
		"the provider did accept it: the customer got the message")

	// The four states side by side (shown with -v): the audit rows, and what the message row and the
	// provider (mock) say about the same send.
	for _, text := range []string{"estado 1 aceito", "estado 2 recusado", "estado 3 aceito sem resultado", "estado 4 resposta tardia"} {
		var msg models.Message
		status := "(sem linha de mensagem)"
		if err := w.app.DB.Where("contact_id = ? AND content = ?", w.contact.ID, text).First(&msg).Error; err == nil {
			status = string(msg.Status)
		}
		t.Logf("%-32s trilha=%v  mensagem=%s  provedor_recebeu=%d", text, results(text), status, w.mockHas(text))
	}

	// Reconciliation: exactly the send whose result row is missing, matched to its message.
	pending := w.pending(t)
	require.Len(t, pending, 1)
	assert.Equal(t, "estado 3 aceito sem resultado", pending[0].Content)
	require.NotNil(t, pending[0].MessageStatus)
	assert.Equal(t, "sent", *pending[0].MessageStatus)
	assert.Equal(t, "sent", pending[0].Resolution, "the message row says it went out: nothing to resend")

	// The uncertain one is listed for a person; the system never repeats it.
	var unc []struct{ Content, SendID string }
	require.NoError(t, w.app.DB.Raw(withOrg(uncertainSQL, w.org.ID)).Scan(&unc).Error)
	require.Len(t, unc, 1)
	assert.Equal(t, "estado 4 resposta tardia", unc[0].Content)

	// Reconciling is reading: nothing was sent by looking.
	before := len(w.m.sentMessages)
	_ = w.pending(t)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, before, len(w.m.sentMessages))
}

// A send that never reached the provider (nothing listening) is a definite "failed",
// not "uncertain": it is safe to try again.
func TestProtocolSend_ConnectionNeverEstablished_IsAProvenFailure(t *testing.T) {
	w := newMsgWorld(t)
	w.m.close() // nothing listens any more
	code, env := w.sendText(t, w.agent, "ninguem ouviu")
	assert.Equal(t, fasthttp.StatusBadGateway, code)
	assert.Equal(t, "failed", env.Data["result"], "a connection that never opened is a proven failure")
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, "failed", changeOf(t, rows[1], "result"))
	assert.Empty(t, w.pending(t), "a failed send is not pending")
}

// An attempt whose message row was never created (we stopped before the provider call)
// reconciles as "never_sent": the one case where sending again is safe by construction.
func TestProtocolSend_AttemptWithoutMessageRow_IsNeverSent(t *testing.T) {
	w := newMsgWorld(t)
	require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register("test:fail_message_row", func(tx *gorm.DB) {
		if tx.Statement.Table == "messages" {
			_ = tx.AddError(errors.New("simulated message insert failure"))
		}
	}))
	t.Cleanup(func() { _ = w.app.DB.Callback().Create().Remove("test:fail_message_row") })

	code, _ := w.sendText(t, w.agent, "sem linha de mensagem")
	assert.Equal(t, fasthttp.StatusInternalServerError, code)
	assert.Zero(t, w.mockHas("sem linha de mensagem"), "nothing reached the provider")
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, "failed", changeOf(t, rows[1], "result"))
}

// --- duplicates: what is protected and what is not --------------------------------

// A repeated request is a new, explicit send: there is no idempotency key, so the same
// request twice is two messages. The protection against a double click is in the
// interface (the button is disabled while the request runs), not in the server.
func TestProtocolSend_RepeatedRequestIsASecondMessage(t *testing.T) {
	w := newMsgWorld(t)
	for i := 0; i < 2; i++ {
		code, _ := w.sendText(t, w.agent, "mesmo texto")
		require.Equal(t, fasthttp.StatusOK, code)
	}
	assert.Equal(t, 2, w.mockHas("mesmo texto"))
	sent := map[any]bool{}
	for _, r := range auditRows(t, w.app.DB, w.occ.ID) {
		sent[changeOf(t, r, "send_id")] = true
	}
	assert.Len(t, sent, 2, "each request has its own send_id, so each can be told apart in the trail")
}

// When the provider answers too late the first message HAS been delivered. What used to be a risk left to
// the person ("check before sending again") is now closed by the server: while that send is uncertain the case
// accepts no other send, from anyone, with any key or text. The system still says "uncertain", still never
// repeats it on its own, and a person settles it after checking the conversation.
func TestProtocolSend_LateProviderAnswer_ASecondSendIsRefusedWhileUncertain(t *testing.T) {
	w := newMsgWorld(t)
	w.shortTimeoutClient(150 * time.Millisecond)
	w.m.mu.Lock()
	w.m.delay = 400 * time.Millisecond
	w.m.mu.Unlock()

	code, env := w.sendText(t, w.agent, "resposta tardia")
	require.Equal(t, fasthttp.StatusAccepted, code)
	require.Equal(t, "uncertain", env.Data["result"])
	require.Eventually(t, func() bool { return w.mockHas("resposta tardia") == 1 }, 2*time.Second, 20*time.Millisecond)

	// Nothing repeats it by itself.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, w.mockHas("resposta tardia"))

	// A person who ignores the warning and sends again, with a new key and even the same text, is refused.
	w.m.mu.Lock()
	w.m.delay = 0
	w.m.mu.Unlock()
	w.shortTimeoutClient(2 * time.Second)
	code, env = w.sendText(t, w.agent, "resposta tardia")
	require.Equal(t, fasthttp.StatusConflict, code)
	assert.Equal(t, "send_pending", env.ErrorType)
	assert.Equal(t, 1, w.mockHas("resposta tardia"), "no second copy reached the customer")

	// The trail has the one send, as uncertain.
	got := map[any]int{}
	for _, r := range auditRows(t, w.app.DB, w.occ.ID) {
		if changeOf(t, r, "result") != "attempted" {
			got[changeOf(t, r, "result")]++
		}
	}
	assert.Equal(t, map[any]int{"uncertain": 1}, got)
}

// withOrg fills the organization into the report's query. GORM would rewrite the "?" of a
// jsonpath filter as a bind parameter, so the (generated) UUID is embedded and no
// parameters are passed.
func withOrg(query string, org uuid.UUID) string {
	return strings.ReplaceAll(query, "@org", "'"+org.String()+"'")
}

// --- the answer for an unknown result ------------------------------------------------

// An unknown result is answered with 202 (not an error that invites a retry), says in so
// many words that it is pending confirmation, hands back the ids to ask about it later,
// and is never sent again by the system.
func TestProtocolSend_UncertainIs202WithHandlesAndNeverResends(t *testing.T) {
	w := newMsgWorld(t)
	w.shortTimeoutClient(150 * time.Millisecond)
	w.m.mu.Lock()
	w.m.delay = 400 * time.Millisecond
	w.m.mu.Unlock()

	code, env := w.sendText(t, w.agent, "resultado incerto")
	require.Equal(t, fasthttp.StatusAccepted, code)
	assert.Equal(t, "success", env.Status, "the request was accepted; the outcome is in the body")
	assert.Equal(t, "uncertain", env.Data["result"])
	assert.Equal(t, false, env.Data["sent"], "202 must never read as 'sent'")
	assert.Equal(t, false, env.Data["confirmed"])
	assert.Contains(t, env.Message, "pending confirmation")
	assert.Contains(t, env.Message, "Do not send it again")

	// The handles are the ones in the trail and on the message row.
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, env.Data["send_id"], changeOf(t, rows[1], "send_id"))
	assert.Equal(t, env.Data["message_id"], changeOf(t, rows[1], "message_id"))
	assert.Equal(t, "uncertain", changeOf(t, rows[1], "result"))
	var msg models.Message
	require.NoError(t, w.app.DB.First(&msg, "id = ?", env.Data["message_id"]).Error)

	// Nothing sends it again: not the request, not a background worker, not time.
	require.Eventually(t, func() bool { return w.mockHas("resultado incerto") == 1 }, 2*time.Second, 20*time.Millisecond)
	time.Sleep(1200 * time.Millisecond)
	assert.Equal(t, 1, w.mockHas("resultado incerto"), "the provider got it once and only once")
	var msgs int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Count(&msgs).Error)
	assert.EqualValues(t, 1, msgs)
	assert.Len(t, auditRows(t, w.app.DB, w.occ.ID), 2, "no further attempts recorded")

	// And the state can be asked about afterwards, without sending anything.
	var unc []struct{ Content, SendID string }
	require.NoError(t, w.app.DB.Raw(withOrg(uncertainSQL, w.org.ID)).Scan(&unc).Error)
	require.Len(t, unc, 1)
	assert.Equal(t, env.Data["send_id"], unc[0].SendID)
	assert.Equal(t, 1, w.mockHas("resultado incerto"), "asking does not send")
}

// Every outcome hands back the ids, so the caller can always ask about the send.
func TestProtocolSend_EveryAnswerCarriesTheHandles(t *testing.T) {
	w := newMsgWorld(t)

	code, ok := w.sendText(t, w.agent, "tudo certo")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "sent", ok.Data["result"])
	assert.Equal(t, true, ok.Data["sent"])
	assert.Equal(t, true, ok.Data["confirmed"])
	assert.NotEmpty(t, ok.Data["send_id"])
	assert.NotEmpty(t, ok.Data["message_id"])
	assert.Equal(t, w.occ.ProtocolNumber, ok.Data["protocol_number"])

	w.m.returnError, w.m.errorMessage = true, "recusado"
	code, bad := w.sendText(t, w.agent, "recusado")
	require.Equal(t, fasthttp.StatusBadGateway, code)
	assert.Equal(t, "error", bad.Status)
	assert.Equal(t, "failed", bad.Data["result"])
	assert.Equal(t, false, bad.Data["sent"])
	assert.Equal(t, true, bad.Data["confirmed"], "a proven failure is a confirmed outcome")
	assert.NotEmpty(t, bad.Data["send_id"])
	assert.NotEmpty(t, bad.Data["message_id"])
	assert.NotEqual(t, ok.Data["send_id"], bad.Data["send_id"])
}
