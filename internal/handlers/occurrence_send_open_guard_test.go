package handlers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// A case has at most ONE open customer send (processing, or uncertain until a person settles it), whoever
// asks, with whatever key or text, from whichever device or server. The database enforces it (partial unique
// index uq_send_intent_open); these tests run against the real database, not a mock.

const pendingType = "send_pending"

// colleague is another person who may write on this customer's conversation (conversations:view_all), so the
// only thing between them and a send is the open send of the case.
func (w *msgWorld) colleague(t *testing.T) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "colleague-"+uuid.NewString()[:5],
		[]string{"occurrences:read", "occurrences:write", "occurrences:view_all", "chat:read", "chat:write", "conversations:view_all"})
	return testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
}

func (w *msgWorld) openIntents(t *testing.T, occ *models.Occurrence) int64 {
	t.Helper()
	var n int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).
		Where("occurrence_id = ? AND state IN ?", occ.ID, []string{models.SendIntentProcessing, models.SendIntentUncertain}).Count(&n).Error)
	return n
}

// --- the guard ------------------------------------------------------------------------------------

// Real concurrency against the real database: many requests, many keys, two users, one case, a provider that
// takes a while so the sends overlap. Exactly one gets through, everyone else is told a send is pending, and the
// customer receives one message.
func TestOpenSend_ConcurrentRequests_ManyKeysManyUsers_ExactlyOneSend(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	w.lateProvider(600*time.Millisecond, 5*time.Second) // slow, but within the client's wait: the first send stays "processing" while the rest arrive

	const n = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	answers := make([]keyedAnswer, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := w.agent
			if i%2 == 1 {
				user = other
			}
			<-start
			answers[i] = w.sendKeyedNoRequire(t, user, w.occ, uuid.NewString(), map[string]string{"message": fmt.Sprintf("concorrente %d", i)})
		}(i)
	}
	close(start)
	wg.Wait()

	ok, pending := 0, 0
	for i, a := range answers {
		switch a.code {
		case fasthttp.StatusOK:
			ok++
		case fasthttp.StatusConflict:
			assert.Equal(t, pendingType, a.env.ErrorType, "request %d", i)
			pending++
		default:
			t.Errorf("request %d: unexpected status %d", i, a.code)
		}
	}
	assert.Equal(t, 1, ok, "exactly one send got through")
	assert.Equal(t, n-1, pending, "all the others were told a send is pending")
	assert.Equal(t, 1, w.providerTotal(), "the customer received ONE message")
	assert.EqualValues(t, 1, w.outgoingMessages(t))
	rows := w.intents(t)
	require.Len(t, rows, 1, "a refused request leaves no record: nothing was started")
	assert.Equal(t, models.SendIntentSent, rows[0].State)
}

// sendKeyedNoRequire is sendKeyed for goroutines (no t.FailNow off the test goroutine).
func (w *msgWorld) sendKeyedNoRequire(t *testing.T, user *models.User, occ *models.Occurrence, key string, body map[string]string) keyedAnswer {
	raw, _ := json.Marshal(body)
	req := testutil.NewJSONRequest(t, nil)
	req.RequestCtx.Request.SetBody(raw)
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	testutil.SetHeader(req, "Idempotency-Key", key)
	_ = w.app.SendOccurrenceProtocol(req)
	a := keyedAnswer{code: testutil.GetResponseStatusCode(req), replay: string(req.RequestCtx.Response.Header.Peek("Idempotent-Replay")) == "true"}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &a.env)
	return a
}

// uncertain keeps the case closed for everyone, for as long as it takes: not by time, not by the reconciler, not by
// the retention.
func TestOpenSend_Uncertain_BlocksEveryoneAndEveryText_UntilSettled(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "primeira"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	require.Equal(t, "uncertain", first.env.Data["result"])
	w.lateProvider(0, 2*time.Second)

	try := func(label string, user *models.User, text string) {
		a := w.sendKeyed(t, user, w.occ, uuid.NewString(), map[string]string{"message": text})
		assert.Equal(t, fasthttp.StatusConflict, a.code, label)
		assert.Equal(t, pendingType, a.env.ErrorType, label)
		assert.Equal(t, "uncertain", a.env.Data["state"], label)
	}
	try("same user, same text", w.agent, "primeira")
	try("same user, edited text", w.agent, "texto editado")
	try("another user", other, "outro usuario")

	// Time does not free it: not the reconciler nor the retention, ten days or ten years later.
	for _, later := range []time.Duration{3 * time.Minute, 10 * 24 * time.Hour, 10 * 365 * 24 * time.Hour} {
		w.app.ReconcileSendIntents(time.Now().Add(later))
		w.app.PurgeSendIntents(time.Now().Add(later))
		try(fmt.Sprintf("after %s", later), other, "depois do tempo")
	}
	assert.EqualValues(t, 1, w.openIntents(t, w.occ))
	assert.Equal(t, 1, w.providerTotal(), "nothing was sent again")
	require.Len(t, w.intents(t), 1, "and the record is still there")
}

func TestOpenSend_SentAndFailed_FollowTheNormalRules(t *testing.T) {
	w := newMsgWorld(t)
	// sent: the next send is a new intention.
	a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "um"})
	require.Equal(t, fasthttp.StatusOK, a.code)
	b := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "dois"})
	assert.Equal(t, fasthttp.StatusOK, b.code, "a sent send does not hold the case")
	// failed (proven refusal): the next send is a new intention too.
	w.chooseProviderAnswer(http.StatusBadRequest, 100, "")
	c := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "tres"})
	require.Equal(t, "failed", c.env.Data["result"])
	w.chooseProviderAnswer(0, 0, "")
	d := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "quatro"})
	assert.Equal(t, fasthttp.StatusOK, d.code, "a failed send does not hold the case")
	assert.EqualValues(t, 0, w.openIntents(t, w.occ))
}

func TestOpenSend_OtherCasesAndOtherOrganizations_AreNotAffected(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "caso um"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	w.lateProvider(0, 2*time.Second)

	// Another case of the same customer is its own thing.
	other := w.newCase(t)
	got := w.sendKeyed(t, w.agent, other, uuid.NewString(), map[string]string{"message": "caso dois"})
	assert.Equal(t, fasthttp.StatusOK, got.code, "the guard is per case")

	// The same key in another organization is another key (the key is unique per organization).
	assert.EqualValues(t, 1, w.openIntents(t, w.occ))
}

// The same key keeps answering from its own record, not with "pending": a repeat is a repeat.
func TestOpenSend_RepeatOfTheSameKey_StillReplaysItsOwnAnswer(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	key := uuid.NewString()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "mesma chave"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "mesma chave"})
	assert.Equal(t, fasthttp.StatusAccepted, again.code)
	assert.True(t, again.replay)
	assert.Equal(t, "uncertain", again.env.Data["result"])
	assert.NotEqual(t, pendingType, again.env.ErrorType)
	// And the same key with another text is a reused key, not a pending send.
	reused := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "outro texto"})
	assert.Equal(t, fasthttp.StatusConflict, reused.code)
	assert.Equal(t, "idempotency_key_reused", reused.env.ErrorType)
}

// The refusal tells the page what to ask about, never who started it or what was sent.
func TestOpenSend_PendingAnswer_RevealsOnlyWhatThePageNeeds(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "segredo do cliente"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	w.lateProvider(0, 2*time.Second)

	mine := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "x"})
	theirs := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "y"})
	assert.Equal(t, true, mine.env.Data["own"])
	assert.Equal(t, false, theirs.env.Data["own"])
	for _, a := range []keyedAnswer{mine, theirs} {
		assert.Equal(t, first.env.Data["send_id"], a.env.Data["send_id"], "it points at the send in the way")
		raw, _ := json.Marshal(a.env)
		assert.NotContains(t, string(raw), "segredo do cliente", "never the text")
		assert.NotContains(t, string(raw), w.agent.ID.String(), "never who")
		assert.NotContains(t, string(raw), w.agent.Email)
		assert.NotContains(t, string(raw), w.org.ID.String())
	}
}

// --- asking for the open send ------------------------------------------------------------------------

func (w *msgWorld) getOpen(t *testing.T, user *models.User, occ *models.Occurrence) (int, map[string]any) {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	testutil.SetQueryParam(req, "open", "true")
	require.NoError(t, w.app.GetOccurrenceProtocolSendByKey(req))
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &out)
	return testutil.GetResponseStatusCode(req), out.Data
}

func TestOpenSend_AskingForTheOpenSend_AnyAuthorizedPerson_NoSending(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)

	code, _ := w.getOpen(t, w.agent, w.occ)
	assert.Equal(t, fasthttp.StatusNotFound, code, "nothing open")

	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "aberta"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	w.lateProvider(0, 2*time.Second)
	total := w.providerTotal()

	code, data := w.getOpen(t, other, w.occ) // another person, another device
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "uncertain", data["state"])
	assert.Equal(t, first.env.Data["send_id"], data["send_id"])
	assert.Equal(t, false, data["own"])
	assert.Equal(t, total, w.providerTotal(), "asking never sends")

	// Not the same permission as sending: refused like sending is. Another organization sees nothing.
	code, _ = w.getOpen(t, w.chatOnly, w.occ)
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, code)
	code, _ = w.getOpen(t, w.outsider, w.occ)
	assert.Contains(t, []int{fasthttp.StatusNotFound, fasthttp.StatusForbidden}, code)
	// Another case has none.
	code, _ = w.getOpen(t, w.agent, w.newCase(t))
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

// --- settling an uncertain send ------------------------------------------------------------------------

type resolveAnswer struct {
	code int
	env  sendEnvelope
}

func (w *msgWorld) resolve(t *testing.T, user *models.User, occ *models.Occurrence, sendID string, body map[string]any) resolveAnswer {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	testutil.SetPathParam(req, "send_id", sendID)
	require.NoError(t, w.app.ResolveOccurrenceProtocolSend(req))
	a := resolveAnswer{code: testutil.GetResponseStatusCode(req)}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &a.env)
	return a
}

func goodDecision(outcome string) map[string]any {
	return map[string]any{"outcome": outcome, "conversation_checked": true, "note": "O cliente respondeu na conversa, confirmando o recebimento."}
}

// uncertainSend leaves the case with one uncertain send and returns its id.
func (w *msgWorld) uncertainSend(t *testing.T) string {
	t.Helper()
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "incerta"})
	require.Equal(t, "uncertain", a.env.Data["result"])
	w.lateProvider(0, 2*time.Second)
	return fmt.Sprint(a.env.Data["send_id"])
}

func (w *msgWorld) mkUser(t *testing.T, keys ...string) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "r-"+uuid.NewString()[:5], keys)
	return testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
}

func TestResolveSend_Authorization_PermissionAndAccessToTheCase(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	total := w.providerTotal()

	// Own permission, independent of writing and of chat: a person who can see the case but cannot write
	// anything may settle it.
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	// The sender holds every writing permission and still cannot settle: it is not theirs to decide.
	noPerm := w.agent
	// The permission without the scope of the case: a supervisor settles only what is under their management.
	noScope := w.mkUser(t, "occurrences:read", "occurrences:resolve_send")

	assert.Equal(t, fasthttp.StatusForbidden, w.resolve(t, noPerm, w.occ, sendID, goodDecision("delivered")).code, "no permission, even with occurrences:write and chat:write")
	assert.Equal(t, fasthttp.StatusForbidden, w.resolve(t, noScope, w.occ, sendID, goodDecision("delivered")).code, "permission but no scope of this case")
	assert.Equal(t, fasthttp.StatusForbidden, w.resolve(t, w.chatOnly, w.occ, sendID, goodDecision("delivered")).code)
	other := w.resolve(t, w.outsider, w.occ, sendID, goodDecision("delivered"))
	assert.Contains(t, []int{fasthttp.StatusNotFound, fasthttp.StatusForbidden}, other.code, "another organization")
	assert.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State), "every refusal left it as it was")

	ok := w.resolve(t, resolver, w.occ, sendID, goodDecision("delivered"))
	require.Equal(t, fasthttp.StatusOK, ok.code)
	assert.Equal(t, total, w.providerTotal(), "settling sends nothing")
}

func TestResolveSend_RequiresTheConversationCheckAndAJustification(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	bad := []map[string]any{
		{"outcome": "delivered", "conversation_checked": false, "note": "O cliente respondeu na conversa."},
		{"outcome": "delivered", "note": "O cliente respondeu na conversa."},
		{"outcome": "delivered", "conversation_checked": true, "note": ""},
		{"outcome": "delivered", "conversation_checked": true, "note": "curto"},
		{"outcome": "delivered", "conversation_checked": true, "note": string(make([]byte, 1001))},
		{"outcome": "sent", "conversation_checked": true, "note": "O cliente respondeu na conversa."},
		{"outcome": "failed", "conversation_checked": true, "note": "O cliente respondeu na conversa."},
		{"outcome": "", "conversation_checked": true, "note": "O cliente respondeu na conversa."},
	}
	for i, b := range bad {
		a := w.resolve(t, resolver, w.occ, sendID, b)
		assert.Equal(t, fasthttp.StatusBadRequest, a.code, "case %d", i)
	}
	assert.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State))
	assert.EqualValues(t, 1, w.openIntents(t, w.occ))
}

func TestResolveSend_Delivered_And_NotDelivered_RecordAndFreeTheCase(t *testing.T) {
	for _, c := range []struct{ outcome, state string }{
		{models.SendResolutionDelivered, models.SendIntentSent},
		{models.SendResolutionNotDelivered, models.SendIntentFailed},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			w := newMsgWorld(t)
			sendID := w.uncertainSend(t)
			resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
			total := w.providerTotal()
			// While it is open nothing else goes out.
			blocked := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "ainda nao"})
			require.Equal(t, fasthttp.StatusConflict, blocked.code)

			a := w.resolve(t, resolver, w.occ, sendID, goodDecision(c.outcome))
			require.Equal(t, fasthttp.StatusOK, a.code)
			assert.Equal(t, c.state, a.env.Data["state"])
			assert.Equal(t, true, a.env.Data["confirmed"], "the outcome is known now")
			assert.Equal(t, total, w.providerTotal(), "settling sends nothing and sends nothing again")

			row := w.lastIntent(t)
			assert.Equal(t, c.state, string(row.State))
			require.NotNil(t, row.ResolvedByID)
			assert.Equal(t, resolver.ID, *row.ResolvedByID)
			require.NotNil(t, row.ResolvedAt)
			assert.Equal(t, c.outcome, row.ResolutionOutcome)
			assert.Contains(t, row.ResolutionNote, "O cliente respondeu")
			assert.NotNil(t, row.ReconciledAt)

			// Permanent audit entry: who, what, why, previous state.
			var found bool
			for _, r := range auditRows(t, w.app.DB, w.occ.ID) {
				if changeOf(t, r, "result") == "resolved_"+c.outcome {
					found = true
					assert.Equal(t, resolver.ID, r.UserID)
					assert.Equal(t, c.outcome, changeOf(t, r, "outcome"))
					assert.Contains(t, fmt.Sprint(changeOf(t, r, "note")), "O cliente respondeu")
					assert.Equal(t, true, changeOf(t, r, "conversation_checked"))
					assert.Equal(t, "uncertain", changeOf(t, r, "previous_state"))
					assert.Equal(t, sendID, changeOf(t, r, "send_id"))
				}
			}
			assert.True(t, found, "the decision is in the audit trail")

			// The case is free for a new send, with a new key.
			next := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "agora sim"})
			assert.Equal(t, fasthttp.StatusOK, next.code)

			// And the page that asks about the settled send learns it was a person's decision, never whose.
			code, data := w.getSend(t, w.agent, w.occ, sendID)
			require.Equal(t, fasthttp.StatusOK, code)
			assert.Equal(t, true, data["resolved_by_person"])
			assert.Equal(t, c.outcome, data["resolution_outcome"])
			raw, _ := json.Marshal(data)
			assert.NotContains(t, string(raw), resolver.ID.String())
		})
	}
}

// Not reversible through the interface: a settled send cannot be settled again, and a send that was never
// uncertain cannot be settled at all.
func TestResolveSend_OnlyAnUncertainSend_AndOnlyOnce(t *testing.T) {
	w := newMsgWorld(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")

	sent := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "normal"})
	require.Equal(t, fasthttp.StatusOK, sent.code)
	a := w.resolve(t, resolver, w.occ, fmt.Sprint(sent.env.Data["send_id"]), goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, a.code, "a send that was accepted is not for a person to overrule")
	assert.Equal(t, "send_not_uncertain", a.env.ErrorType)
	assert.Equal(t, models.SendIntentSent, string(w.lastIntent(t).State))

	sendID := w.uncertainSend(t)
	require.Equal(t, fasthttp.StatusOK, w.resolve(t, resolver, w.occ, sendID, goodDecision("delivered")).code)
	again := w.resolve(t, resolver, w.occ, sendID, goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, again.code, "the decision is final through the interface")
	assert.Equal(t, models.SendIntentSent, string(w.lastIntent(t).State))

	assert.Equal(t, fasthttp.StatusNotFound, w.resolve(t, resolver, w.occ, uuid.NewString(), goodDecision("delivered")).code)
	assert.Equal(t, fasthttp.StatusNotFound, w.resolve(t, resolver, w.newCase(t), sendID, goodDecision("delivered")).code, "not through another case")
}

// If the evidence the system itself has says the provider accepted it, that wins over a person's reading: the
// send is settled as sent by the reconciliation first, and then there is nothing left to settle.
func TestResolveSend_TheProvidersOwnConfirmationWins(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	row := w.lastIntent(t)
	// The provider's status arrives later: the message row gets its id and advances.
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", row.MessageID).
		Updates(map[string]any{"whats_app_message_id": "wamid.LATE", "status": models.MessageStatusDelivered}).Error)

	a := w.resolve(t, resolver, w.occ, sendID, goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, a.code)
	assert.Equal(t, models.SendIntentSent, string(w.lastIntent(t).State), "settled by the provider's confirmation, not by the person")
	assert.Nil(t, w.lastIntent(t).ResolvedByID)
}

// One transaction: if the audit entry cannot be written, nothing changes and the case stays closed.
func TestResolveSend_AuditFailure_ChangesNothing(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	const cb = "test:fail_resolve_audit"
	require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			_ = tx.AddError(errors.New("simulated audit failure"))
		}
	}))
	a := w.resolve(t, resolver, w.occ, sendID, goodDecision("not_delivered"))
	_ = w.app.DB.Callback().Create().Remove(cb)
	assert.Equal(t, fasthttp.StatusInternalServerError, a.code)
	row := w.lastIntent(t)
	assert.Equal(t, models.SendIntentUncertain, string(row.State), "no decision without its record")
	assert.Nil(t, row.ResolvedByID)
	assert.EqualValues(t, 1, w.openIntents(t, w.occ))
}

// Two people settle at the same time: one decision wins, the other is told it was already settled.
func TestResolveSend_Concurrent_OneDecisionWins(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	resolver2 := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, c := range []struct {
		u       *models.User
		outcome string
	}{{resolver, "delivered"}, {resolver2, "not_delivered"}} {
		wg.Add(1)
		go func(i int, u *models.User, outcome string) {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, goodDecision(outcome))
			testutil.SetAuthContext(req, u.OrganizationID, u.ID)
			testutil.SetPathParam(req, "id", w.occ.ID.String())
			testutil.SetPathParam(req, "send_id", sendID)
			_ = w.app.ResolveOccurrenceProtocolSend(req)
			codes[i] = testutil.GetResponseStatusCode(req)
		}(i, c.u, c.outcome)
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, codes)
	var settled int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("id = ? AND resolved_at IS NOT NULL", sendID).Count(&settled).Error)
	assert.EqualValues(t, 1, settled)
}

// --- the reconciler ----------------------------------------------------------------------------------

// An old uncertain row, however many, never keeps a newer stale "processing" claim waiting.
func TestReconcile_ManyOldUncertain_DoNotStarveAStaleProcessing(t *testing.T) {
	w := newMsgWorld(t)
	now := time.Now()
	old := now.Add(-80 * time.Hour)
	// 250 unsettled uncertain sends on other cases, older than the recheck window.
	rows := make([]models.OccurrenceSendIntent, 0, 250)
	for i := 0; i < 250; i++ {
		rows = append(rows, models.OccurrenceSendIntent{ID: uuid.New(), OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: uuid.New(), UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentUncertain,
			MessageID: uuid.New(), ClaimedAt: old, FinishedAt: &old})
	}
	require.NoError(t, w.app.DB.CreateInBatches(&rows, 100).Error)

	// A real send whose owner vanished 3 minutes ago, claimed AFTER all of those.
	a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "esquecida"})
	require.Equal(t, fasthttp.StatusOK, a.code)
	w.abandon(t, fmt.Sprint(a.env.Data["send_id"]), 3*time.Minute)
	require.Equal(t, models.SendIntentProcessing, string(w.lastIntent(t).State))

	assert.Greater(t, w.app.ReconcileSendIntents(now), 0)
	assert.NotEqual(t, models.SendIntentProcessing, string(w.lastIntent(t).State), "settled by the very first tick")
	// The old uncertain ones were left alone: still open, nothing guessed.
	var still int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("state = ? AND organization_id = ?", models.SendIntentUncertain, w.org.ID).Count(&still).Error)
	assert.EqualValues(t, 250, still)
}

// Whatever the window does, it never turns an uncertain into failed and never frees the case.
func TestReconcile_UncertainIsNeverTurnedIntoFailed_NorFreedByTime(t *testing.T) {
	w := newMsgWorld(t)
	sendID := w.uncertainSend(t)
	for _, later := range []time.Duration{time.Minute, 71 * time.Hour, 73 * time.Hour, 30 * 24 * time.Hour} {
		w.app.ReconcileSendIntents(time.Now().Add(later))
		assert.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State), "after %s", later)
	}
	// A confirmation that arrives after the window still settles it when somebody asks (the state query).
	row := w.lastIntent(t)
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", row.MessageID).
		Updates(map[string]any{"whats_app_message_id": "wamid.VERYLATE", "status": models.MessageStatusDelivered}).Error)
	code, data := w.getSend(t, w.agent, w.occ, sendID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.SendIntentSent, data["state"])
}

// --- the diagnostic ------------------------------------------------------------------------------------

func TestOpenSendIndex_Exists_AndRefusesASecondOpenRow(t *testing.T) {
	w := newMsgWorld(t)
	var def string
	require.NoError(t, w.app.DB.Raw(`SELECT indexdef FROM pg_indexes WHERE tablename = 'occurrence_send_intents' AND indexname = 'uq_send_intent_open'`).Scan(&def).Error)
	assert.Contains(t, def, "UNIQUE")
	assert.Contains(t, def, "organization_id, occurrence_id, kind")
	assert.Contains(t, def, "processing")
	assert.Contains(t, def, "uncertain")

	mk := func(state string) error {
		return w.app.DB.Create(&models.OccurrenceSendIntent{ID: uuid.New(), OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: w.occ.ID, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: state,
			MessageID: uuid.New(), ClaimedAt: time.Now()}).Error
	}
	require.NoError(t, mk(models.SendIntentUncertain))
	assert.Error(t, mk(models.SendIntentProcessing), "a second open row on the case is refused by the database itself")
	assert.NoError(t, mk(models.SendIntentSent), "settled rows are not held by it")
	assert.NoError(t, mk(models.SendIntentFailed))
}
