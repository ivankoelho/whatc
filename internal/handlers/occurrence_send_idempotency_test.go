package handlers_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Idempotency of the protocol send: one Idempotency-Key is one send, whatever the number of
// requests, retries, servers or recoveries. Every test counts what the provider received.

type keyedAnswer struct {
	code   int
	replay bool
	env    sendEnvelope
}

// sendKeyed sends with an explicit key (a repeat of the same intention uses the same key).
func (w *msgWorld) sendKeyed(t *testing.T, user *models.User, occ *models.Occurrence, key string, body map[string]string) keyedAnswer {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := testutil.NewJSONRequest(t, nil)
	if body != nil {
		req.RequestCtx.Request.SetBody(raw)
	}
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	if key != "" {
		testutil.SetHeader(req, "Idempotency-Key", key)
	}
	require.NoError(t, w.app.SendOccurrenceProtocol(req))
	a := keyedAnswer{code: testutil.GetResponseStatusCode(req), replay: string(req.RequestCtx.Response.Header.Peek("Idempotent-Replay")) == "true"}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &a.env)
	return a
}

func (w *msgWorld) getSend(t *testing.T, user *models.User, occ *models.Occurrence, sendID string) (int, map[string]any) {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	testutil.SetPathParam(req, "send_id", sendID)
	require.NoError(t, w.app.GetOccurrenceProtocolSend(req))
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &out)
	return testutil.GetResponseStatusCode(req), out.Data
}

func (w *msgWorld) intents(t *testing.T) []models.OccurrenceSendIntent {
	t.Helper()
	var rows []models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.org.ID).Order("created_at").Find(&rows).Error)
	return rows
}

func (w *msgWorld) outgoingMessages(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Count(&n).Error)
	return n
}

func (w *msgWorld) lateProvider(delay time.Duration, clientWaits time.Duration) {
	w.shortTimeoutClient(clientWaits)
	w.m.mu.Lock()
	w.m.delay = delay
	w.m.mu.Unlock()
}

func (w *msgWorld) providerTotal() int {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	return len(w.m.sentMessages)
}

// abandon turns a finished intent into a claim whose owner vanished 3 minutes ago.
func (w *msgWorld) abandon(t *testing.T, sendID string, age time.Duration) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("id = ?", sendID).Updates(map[string]any{
		"state": models.SendIntentProcessing, "http_status": 0, "response": nil, "finished_at": nil, "reconciled_at": nil,
		"claimed_at": time.Now().Add(-age),
	}).Error)
}

// --- the key itself -------------------------------------------------------------------

func TestSendIdempotency_KeyIsMandatoryAndValidated(t *testing.T) {
	w := newMsgWorld(t)
	for name, key := range map[string]string{"missing": "", "not a uuid": "abc", "sql-ish": "1; DROP TABLE x"} {
		a := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "sem chave"})
		assert.Equal(t, fasthttp.StatusBadRequest, a.code, name)
		assert.Contains(t, []string{"idempotency_key_required", "idempotency_key_invalid"}, a.env.ErrorType, name)
	}
	assert.Zero(t, w.providerTotal(), "nothing is sent without a valid key")
	assert.Empty(t, w.intents(t), "and nothing is claimed")
	assert.Zero(t, w.outgoingMessages(t))
	assert.Empty(t, auditRows(t, w.app.DB, w.occ.ID))
}

func TestSendIdempotency_DatabaseRefusesTwoClaimsOfOneKey(t *testing.T) {
	w := newMsgWorld(t)
	mk := func(org uuid.UUID, key string) error {
		return w.app.DB.Create(&models.OccurrenceSendIntent{ID: uuid.New(), OrganizationID: org, IdempotencyKey: key, OccurrenceID: w.occ.ID,
			UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
			MessageID: uuid.New(), ClaimedAt: time.Now()}).Error
	}
	k := uuid.New().String()
	require.NoError(t, mk(w.org.ID, k))
	assert.Error(t, mk(w.org.ID, k), "UNIQUE (organization_id, idempotency_key)")
	assert.NoError(t, mk(w.outsider.OrganizationID, k), "the key is scoped by organization")
}

// --- repetition -------------------------------------------------------------------------

func TestSendIdempotency_SameKeySameRequest_SendsOnce(t *testing.T) {
	w := newMsgWorld(t)
	key := uuid.New().String()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "uma vez so"})
	require.Equal(t, fasthttp.StatusOK, first.code)
	assert.False(t, first.replay)

	for i := 0; i < 3; i++ {
		again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "uma vez so"})
		assert.Equal(t, fasthttp.StatusOK, again.code)
		assert.True(t, again.replay, "a repeat is marked as a replay")
		assert.Equal(t, first.env.Data["send_id"], again.env.Data["send_id"])
		assert.Equal(t, first.env.Data["message_id"], again.env.Data["message_id"])
		assert.Equal(t, "sent", again.env.Data["result"])
	}
	assert.Equal(t, 1, w.mockHas("uma vez so"), "the provider received it once")
	assert.EqualValues(t, 1, w.outgoingMessages(t))
	assert.Len(t, auditRows(t, w.app.DB, w.occ.ID), 2, "one attempt, one result: repeats leave no trail of their own")
	assert.Len(t, w.intents(t), 1)
}

func TestSendIdempotency_AnotherKeyIsANewSend(t *testing.T) {
	w := newMsgWorld(t)
	a := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "nova intencao"})
	b := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "nova intencao"})
	require.Equal(t, fasthttp.StatusOK, a.code)
	require.Equal(t, fasthttp.StatusOK, b.code)
	assert.NotEqual(t, a.env.Data["send_id"], b.env.Data["send_id"])
	assert.Equal(t, 2, w.mockHas("nova intencao"), "a new intention uses a new key and is sent")
}

func TestSendIdempotency_TwoConcurrentRequestsWithTheSameKey_OneSend(t *testing.T) {
	w := newMsgWorld(t)
	w.m.mu.Lock()
	w.m.delay = 300 * time.Millisecond // keeps the first one in "processing" while the others arrive
	w.m.mu.Unlock()
	key := uuid.New().String()

	const n = 16
	var wg sync.WaitGroup
	answers := make([]keyedAnswer, n)
	// Requests are built on the test goroutine (testutil asserts); only the handler call is concurrent.
	reqs := make([]func() keyedAnswer, n)
	for i := range reqs {
		reqs[i] = func() keyedAnswer {
			return w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "corrida"})
		}
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = reqs[i]()
		}(i)
	}
	wg.Wait()

	owners, replays, sentCodes := 0, 0, 0
	ids := map[any]bool{}
	for _, a := range answers {
		ids[a.env.Data["send_id"]] = true
		if a.replay {
			replays++
		} else {
			owners++
		}
		if a.code == fasthttp.StatusOK {
			sentCodes++
		}
		assert.Contains(t, []int{fasthttp.StatusOK, fasthttp.StatusAccepted}, a.code)
	}
	assert.Equal(t, 1, owners, "exactly one request owns the send")
	assert.Equal(t, n-1, replays)
	assert.Len(t, ids, 1, "everyone is told about the same send")
	require.Eventually(t, func() bool { return w.mockHas("corrida") >= 1 }, 2*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, w.mockHas("corrida"), "the provider received it exactly once")
	assert.EqualValues(t, 1, w.outgoingMessages(t))
	assert.Len(t, w.intents(t), 1)
	assert.Len(t, auditRows(t, w.app.DB, w.occ.ID), 2)
}

// --- the same key for something else ---------------------------------------------------

func TestSendIdempotency_SameKeyDifferentRequest_Is409AndSendsNothing(t *testing.T) {
	w := newMsgWorld(t)
	other := models.Occurrence{OrganizationID: w.org.ID, ContactID: w.contact.ID, Title: "Outro caso", StageID: w.occ.StageID,
		OpenedByUserID: w.agent.ID, AssignedUserID: &w.agent.ID}
	require.NoError(t, w.app.CreateOccurrenceForTest(&other))

	key := uuid.New().String()
	require.Equal(t, fasthttp.StatusOK, w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "original"}).code)
	before := w.providerTotal()

	cases := map[string]keyedAnswer{
		"another text":       w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "outro texto"}),
		"default text":       w.sendKeyed(t, w.agent, w.occ, key, nil),
		"another occurrence": w.sendKeyed(t, w.agent, &other, key, map[string]string{"message": "original"}),
	}
	for name, a := range cases {
		assert.Equal(t, fasthttp.StatusConflict, a.code, name)
		assert.Equal(t, "idempotency_key_reused", a.env.ErrorType, name)
	}
	assert.Equal(t, before, w.providerTotal(), "a conflicting request sends nothing")
	assert.Len(t, w.intents(t), 1, "and claims nothing")

	// Same key, same text, but a different destination (the contact now has another phone).
	require.NoError(t, w.app.DB.Model(w.contact).Update("phone_number", "5511900000099").Error)
	moved := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "original"})
	assert.Equal(t, fasthttp.StatusConflict, moved.code, "another destination under the same key is a different request")
	assert.Equal(t, before, w.providerTotal())
}

// --- every state repeats without sending ---------------------------------------------

func TestSendIdempotency_RepeatWhileProcessing_ReturnsProcessingAndDoesNotSend(t *testing.T) {
	w := newMsgWorld(t)
	w.m.mu.Lock()
	w.m.delay = 600 * time.Millisecond
	w.m.mu.Unlock()
	key := uuid.New().String()

	done := make(chan keyedAnswer, 1)
	go func() { done <- w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "em andamento"}) }()
	require.Eventually(t, func() bool {
		rows := w.intents(t)
		return len(rows) == 1 && rows[0].State == models.SendIntentProcessing
	}, 2*time.Second, 10*time.Millisecond)

	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "em andamento"})
	assert.Equal(t, fasthttp.StatusAccepted, again.code)
	assert.True(t, again.replay)
	assert.Equal(t, "processing", again.env.Data["result"])
	assert.Equal(t, false, again.env.Data["sent"])
	assert.Equal(t, false, again.env.Data["confirmed"])

	first := <-done
	assert.Equal(t, fasthttp.StatusOK, first.code)
	assert.Equal(t, first.env.Data["send_id"], again.env.Data["send_id"])
	assert.Equal(t, 1, w.mockHas("em andamento"))
}

func TestSendIdempotency_RepeatAfterFailed_ReplaysTheFailureAndKeepsTheKey(t *testing.T) {
	w := newMsgWorld(t)
	key := uuid.New().String()
	w.closeWindow(t) // a proven refusal: nothing is sent
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "janela fechada"})
	require.Equal(t, fasthttp.StatusUnprocessableEntity, first.code)
	assert.Equal(t, "failed", first.env.Data["result"])

	// The window reopens, but the key is spent: the same intention is answered, not sent.
	require.NoError(t, w.app.DB.Model(w.contact).Update("last_inbound_at", time.Now()).Error)
	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "janela fechada"})
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, again.code)
	assert.True(t, again.replay)
	assert.Equal(t, first.env.Data["send_id"], again.env.Data["send_id"])
	assert.Zero(t, w.providerTotal())

	// The person decides to try again: that is a new intention, a new key, and it goes.
	retry := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "janela fechada"})
	assert.Equal(t, fasthttp.StatusOK, retry.code)
	assert.Equal(t, 1, w.mockHas("janela fechada"))
}

func TestSendIdempotency_ProviderRefusal_IsReplayedAsFailed(t *testing.T) {
	w := newMsgWorld(t)
	w.m.returnError, w.m.errorMessage = true, "recusado"
	key := uuid.New().String()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "recusa"})
	require.Equal(t, fasthttp.StatusBadGateway, first.code)
	w.m.returnError = false // the provider is fine now
	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "recusa"})
	assert.Equal(t, fasthttp.StatusBadGateway, again.code)
	assert.True(t, again.replay)
	assert.Equal(t, "failed", again.env.Data["result"])
	assert.Equal(t, 0, w.mockHas("recusa"), "the spent key does not send, even though the provider recovered")
}

func TestSendIdempotency_ProviderTimeoutAndLateAnswer_RepeatIsUncertainAndNeverResends(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	key := uuid.New().String()

	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "timeout"})
	require.Equal(t, fasthttp.StatusAccepted, first.code)
	require.Equal(t, "uncertain", first.env.Data["result"])
	require.Eventually(t, func() bool { return w.mockHas("timeout") == 1 }, 2*time.Second, 20*time.Millisecond)

	// The provider's late answer arrives after the timeout; the repeat is still "uncertain", not a second send.
	time.Sleep(450 * time.Millisecond)
	w.lateProvider(0, 2*time.Second)
	for i := 0; i < 3; i++ {
		again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "timeout"})
		assert.Equal(t, fasthttp.StatusAccepted, again.code)
		assert.True(t, again.replay)
		assert.Equal(t, "uncertain", again.env.Data["result"])
		assert.Equal(t, false, again.env.Data["sent"])
		assert.Equal(t, first.env.Data["send_id"], again.env.Data["send_id"])
	}
	assert.Equal(t, 1, w.mockHas("timeout"), "the provider has exactly one copy")
	assert.EqualValues(t, 1, w.outgoingMessages(t))
}

// --- a claim nobody finishes ---------------------------------------------------------------

func TestSendIdempotency_FreshClaimIsLeftAlone_StaleClaimIsReconciledAfterTwoMinutes(t *testing.T) {
	w := newMsgWorld(t)
	key := uuid.New().String()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "reivindicacao"})
	require.Equal(t, fasthttp.StatusOK, first.code)
	sendID := first.env.Data["send_id"].(string)

	w.abandon(t, sendID, 90*time.Second)   // under 2 minutes: the owner may still be working
	w.app.ReconcileSendIntents(time.Now()) // the sweep is global: judge only this claim
	assert.Equal(t, models.SendIntentProcessing, w.intents(t)[0].State)
	a := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "reivindicacao"})
	assert.Equal(t, fasthttp.StatusAccepted, a.code, "still reported as in progress")
	assert.Equal(t, "processing", a.env.Data["result"])

	w.abandon(t, sendID, 3*time.Minute) // past 2 minutes: recovery starts
	w.app.ReconcileSendIntents(time.Now())
	got := w.intents(t)[0]
	assert.Equal(t, models.SendIntentSent, got.State, "the trail's result row says it was sent")
	assert.NotNil(t, got.ReconciledAt)
	assert.Equal(t, 1, w.mockHas("reivindicacao"), "recovery never sends")

	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "reivindicacao"})
	assert.Equal(t, fasthttp.StatusOK, again.code)
	assert.Equal(t, 1, w.mockHas("reivindicacao"))
}

func TestSendIdempotency_Reconciliation_ByEvidence_NeverFailsFromAMissingRow(t *testing.T) {
	type scenario struct {
		name    string
		prepare func(t *testing.T, w *msgWorld, sendID string, messageID string)
		want    string
	}
	dropResultRows := func(t *testing.T, w *msgWorld, sendID string) {
		// The handler died before writing the result row: only the "attempted" row is left.
		require.NoError(t, w.app.DB.Exec(`DELETE FROM audit_logs WHERE resource_type = 'occurrence_message' AND resource_id = ?
			AND jsonb_path_exists(changes, '$[*] ? (@.field == "result" && @.new_value != "attempted")')`, w.occ.ID).Error)
	}
	scenarios := []scenario{
		{"trail says failed", func(t *testing.T, w *msgWorld, sendID, msgID string) {
			require.NoError(t, w.app.DB.Exec(`UPDATE audit_logs SET changes = replace(changes::text, '"sent"', '"failed"')::jsonb
				WHERE resource_type = 'occurrence_message' AND resource_id = ? AND changes::text LIKE '%"result"%"sent"%'`, w.occ.ID).Error)
		}, models.SendIntentFailed},
		{"no result row, message row says sent", func(t *testing.T, w *msgWorld, sendID, msgID string) { dropResultRows(t, w, sendID) }, models.SendIntentSent},
		{"no result row, message still pending", func(t *testing.T, w *msgWorld, sendID, msgID string) {
			dropResultRows(t, w, sendID)
			require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", msgID).Update("status", models.MessageStatusPending).Error)
		}, models.SendIntentUncertain},
		{"no result row, message row marked failed locally", func(t *testing.T, w *msgWorld, sendID, msgID string) {
			dropResultRows(t, w, sendID)
			require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", msgID).Update("status", models.MessageStatusFailed).Error)
		}, models.SendIntentUncertain},
		{"no result row and no message row at all", func(t *testing.T, w *msgWorld, sendID, msgID string) {
			dropResultRows(t, w, sendID)
			require.NoError(t, w.app.DB.Exec(`DELETE FROM messages WHERE id = ?`, msgID).Error)
		}, models.SendIntentUncertain},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			w := newMsgWorld(t)
			key := uuid.New().String()
			first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "evidencia"})
			require.Equal(t, fasthttp.StatusOK, first.code)
			sendID, msgID := first.env.Data["send_id"].(string), first.env.Data["message_id"].(string)
			sc.prepare(t, w, sendID, msgID)
			w.abandon(t, sendID, 3*time.Minute)

			w.app.ReconcileSendIntents(time.Now())
			got := w.intents(t)[0]
			assert.Equal(t, sc.want, got.State)
			assert.NotEqual(t, models.SendIntentProcessing, got.State)

			// The key stays taken in every outcome, and nothing is ever sent by recovery or by a repeat.
			again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "evidencia"})
			assert.True(t, again.replay)
			assert.NotEqual(t, fasthttp.StatusConflict, again.code)
			assert.Equal(t, 1, w.mockHas("evidencia"), "no second copy, whatever the evidence")
			assert.Equal(t, 1, w.providerTotal())
			assert.Len(t, w.intents(t), 1)
			if sc.want == models.SendIntentUncertain {
				assert.Equal(t, fasthttp.StatusAccepted, again.code)
				assert.Equal(t, "uncertain", again.env.Data["result"])
				assert.Nil(t, got.ReconciledAt, "an open uncertain stays open for later evidence")
			}
		})
	}
}

func TestSendIdempotency_Uncertain_UpgradesOnlyOnTheProvidersOwnConfirmation(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "confirmacao"})
	require.Equal(t, "uncertain", first.env.Data["result"])
	sendID, msgID := first.env.Data["send_id"].(string), first.env.Data["message_id"].(string)
	require.Eventually(t, func() bool { return w.mockHas("confirmacao") == 1 }, 2*time.Second, 20*time.Millisecond)

	// A message row marked "sent" without the provider's id proves nothing.
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", msgID).Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": ""}).Error)
	w.app.ReconcileSendIntents(time.Now())
	assert.Equal(t, models.SendIntentUncertain, w.intents(t)[0].State)

	// The provider's status webhook arrives (id + delivered): now there is evidence.
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", msgID).Updates(map[string]any{"status": models.MessageStatusDelivered, "whats_app_message_id": "wamid.confirmed"}).Error)
	w.app.ReconcileSendIntents(time.Now())
	code, data := w.getSend(t, w.agent, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "sent", data["state"])
	assert.Equal(t, 1, w.mockHas("confirmacao"))
}

// --- the state query ---------------------------------------------------------------------------

func TestSendState_Get_RecoversTheOutcomeAfterALostAnswer(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "resposta perdida"})
	sendID := first.env.Data["send_id"].(string)

	code, data := w.getSend(t, w.agent, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "uncertain", data["state"])
	assert.Equal(t, false, data["sent"])
	assert.Equal(t, false, data["confirmed"])
	assert.Equal(t, sendID, data["send_id"])
	assert.Equal(t, first.env.Data["message_id"], data["message_id"])
	assert.NotContains(t, data, "content", "the customer's text is not returned")
	assert.NotContains(t, data, "request_hash")

	w2 := newMsgWorld(t)
	ok := w2.sendKeyed(t, w2.agent, w2.occ, uuid.New().String(), map[string]string{"message": "tudo certo"})
	code, data = w2.getSend(t, w2.agent, w2.occ, ok.env.Data["send_id"].(string))
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "sent", data["state"])
	assert.Equal(t, true, data["confirmed"])

	before := w2.providerTotal()
	for i := 0; i < 3; i++ {
		w2.getSend(t, w2.agent, w2.occ, ok.env.Data["send_id"].(string))
	}
	assert.Equal(t, before, w2.providerTotal(), "asking never sends")
}

func TestSendState_Get_ReportsAnAbandonedClaimAndReconcilesIt(t *testing.T) {
	w := newMsgWorld(t)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "esquecida"})
	sendID := first.env.Data["send_id"].(string)
	w.abandon(t, sendID, 30*time.Second)
	_, data := w.getSend(t, w.agent, w.occ, sendID)
	assert.Equal(t, "processing", data["state"])
	w.abandon(t, sendID, 3*time.Minute)
	_, data = w.getSend(t, w.agent, w.occ, sendID)
	assert.Equal(t, "sent", data["state"], "the query itself reconciles from the evidence")
	assert.Equal(t, 1, w.mockHas("esquecida"))
}

func TestSendState_Get_AuthorizationAndIsolation(t *testing.T) {
	w := newMsgWorld(t)
	first := w.sendKeyed(t, w.agent, w.occ, uuid.New().String(), map[string]string{"message": "acesso"})
	sendID := first.env.Data["send_id"].(string)

	code, _ := w.getSend(t, w.agent, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusOK, code, "the person working the case")

	// Improper access: same rules as sending.
	code, _ = w.getSend(t, w.jur, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "no chat:write / not the case's channel")
	code, _ = w.getSend(t, w.jur, w.occJur, sendID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "the Juridico's own case, but no chat:write")
	code, _ = w.getSend(t, w.chatOnly, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "chat:write without occurrences:write")
	code, _ = w.getSend(t, w.sup, w.occ, sendID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "seeing every case is not a tie to this conversation")

	// Isolation: another organization, another case, another id.
	code, _ = w.getSend(t, w.outsider, w.occ, sendID)
	assert.Contains(t, []int{fasthttp.StatusNotFound, fasthttp.StatusForbidden}, code, "another organization")
	other := models.Occurrence{OrganizationID: w.org.ID, ContactID: w.contact.ID, Title: "Outro", StageID: w.occ.StageID,
		OpenedByUserID: w.agent.ID, AssignedUserID: &w.agent.ID}
	require.NoError(t, w.app.CreateOccurrenceForTest(&other))
	code, _ = w.getSend(t, w.agent, &other, sendID)
	assert.Equal(t, fasthttp.StatusNotFound, code, "a send of another case is not found through this case")
	code, _ = w.getSend(t, w.agent, w.occ, uuid.New().String())
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = w.getSend(t, w.agent, w.occ, "not-a-uuid")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
}

// --- retention ------------------------------------------------------------------------------------

func TestSendIdempotency_Retention_90Days_OnlyTerminalRecords(t *testing.T) {
	w := newMsgWorld(t)
	now := time.Now()
	old := now.Add(-91 * 24 * time.Hour)
	recent := now.Add(-89 * 24 * time.Hour)
	mk := func(state string, finished, reconciled *time.Time, claimed time.Time) uuid.UUID {
		id := uuid.New()
		require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.New().String(),
			OccurrenceID: w.occ.ID, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: state,
			MessageID: uuid.New(), ClaimedAt: claimed, FinishedAt: finished, ReconciledAt: reconciled}).Error)
		return id
	}
	oldSent := mk(models.SendIntentSent, &old, &old, old)
	oldFailed := mk(models.SendIntentFailed, &old, &old, old)
	recentSent := mk(models.SendIntentSent, &recent, &recent, recent)
	oldProcessing := mk(models.SendIntentProcessing, nil, nil, old)
	oldUncertainOpen := mk(models.SendIntentUncertain, &old, nil, old)
	oldUncertainReconciled := mk(models.SendIntentUncertain, &old, &old, old)
	recentUncertainReconciled := mk(models.SendIntentUncertain, &recent, &recent, recent)

	w.app.PurgeSendIntents(now)
	left := map[uuid.UUID]bool{}
	for _, r := range w.intents(t) {
		left[r.ID] = true
	}
	for _, id := range []uuid.UUID{oldSent, oldFailed, oldUncertainReconciled} {
		assert.False(t, left[id], "terminal and older than 90 days: removed")
	}
	for name, id := range map[string]uuid.UUID{"recent sent": recentSent, "old processing": oldProcessing,
		"old uncertain, never reconciled": oldUncertainOpen, "recent reconciled uncertain": recentUncertainReconciled} {
		assert.True(t, left[id], name+": kept")
	}
	assert.EqualValues(t, 0, w.app.PurgeSendIntents(now), "a second pass removes nothing")
}

// After the record is gone the key means nothing: the same key is a new intention. This is the
// documented behavior after expiry; the interface makes a key per intention, never reusing one.
func TestSendIdempotency_AfterExpiry_TheKeyIsAForgottenKey(t *testing.T) {
	w := newMsgWorld(t)
	key := uuid.New().String()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "expirada"})
	require.Equal(t, fasthttp.StatusOK, first.code)

	// Within the retention: protected.
	w.app.PurgeSendIntents(time.Now().Add(89 * 24 * time.Hour))
	assert.Len(t, w.intents(t), 1, "inside the retention the record stays")
	assert.True(t, w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "expirada"}).replay)
	assert.Equal(t, 1, w.mockHas("expirada"))

	// After it: gone, so the same key is accepted as new and sends again.
	w.app.PurgeSendIntents(time.Now().Add(91 * 24 * time.Hour))
	assert.Empty(t, w.intents(t), "after it the record is gone")
	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "expirada"})
	assert.Equal(t, fasthttp.StatusOK, again.code)
	assert.False(t, again.replay)
	assert.Equal(t, 2, w.mockHas("expirada"))
}

// --- state by key (the answer to the POST never arrived) ---------------------------------------

func (w *msgWorld) getSendByKey(t *testing.T, user *models.User, occ *models.Occurrence, key string) (int, map[string]any) {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	testutil.SetQueryParam(req, "idempotency_key", key)
	require.NoError(t, w.app.GetOccurrenceProtocolSendByKey(req))
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &out)
	return testutil.GetResponseStatusCode(req), out.Data
}

// A page that sent the request and lost the answer knows only the key. It can still learn the
// outcome, and asking never sends anything.
func TestSendState_ByKey_RecoversAfterALostAnswerWithoutSending(t *testing.T) {
	w := newMsgWorld(t)
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	key := uuid.New().String()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "perdeu a resposta"})
	require.Equal(t, "uncertain", first.env.Data["result"])
	require.Eventually(t, func() bool { return w.mockHas("perdeu a resposta") == 1 }, 2*time.Second, 20*time.Millisecond)

	for i := 0; i < 3; i++ { // reopening, another navigation, another tab: the same answer
		code, data := w.getSendByKey(t, w.agent, w.occ, key)
		assert.Equal(t, fasthttp.StatusOK, code)
		assert.Equal(t, "uncertain", data["state"])
		assert.Equal(t, first.env.Data["send_id"], data["send_id"], "it hands back the send_id for later queries")
	}
	assert.Equal(t, 1, w.mockHas("perdeu a resposta"), "recovering never sends")
	assert.EqualValues(t, 1, w.outgoingMessages(t))
	assert.Len(t, w.intents(t), 1, "and never claims a key")
}

func TestSendState_ByKey_UnknownKeyIsNotFoundAndClaimsNothing(t *testing.T) {
	w := newMsgWorld(t)
	code, _ := w.getSendByKey(t, w.agent, w.occ, uuid.New().String())
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = w.getSendByKey(t, w.agent, w.occ, "not-a-uuid")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = w.getSendByKey(t, w.agent, w.occ, "")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Empty(t, w.intents(t))
	assert.Zero(t, w.providerTotal())
}

// Several cases, several sends: each answer is about its own send.
func TestSendState_ByKey_SeveralCasesNeverMixTheirStates(t *testing.T) {
	w := newMsgWorld(t)
	mk := func(title string) *models.Occurrence {
		o := models.Occurrence{OrganizationID: w.org.ID, ContactID: w.contact.ID, Title: title, StageID: w.occ.StageID,
			OpenedByUserID: w.agent.ID, AssignedUserID: &w.agent.ID}
		require.NoError(t, w.app.CreateOccurrenceForTest(&o))
		return &o
	}
	b, c := mk("B"), mk("C")
	kA, kB, kC := uuid.New().String(), uuid.New().String(), uuid.New().String()
	a1 := w.sendKeyed(t, w.agent, w.occ, kA, map[string]string{"message": "caso A"})
	b1 := w.sendKeyed(t, w.agent, b, kB, map[string]string{"message": "caso B"})
	w.closeWindow(t)
	c1 := w.sendKeyed(t, w.agent, c, kC, map[string]string{"message": "caso C"}) // refused: window closed

	for _, tc := range []struct {
		occ   *models.Occurrence
		key   string
		first keyedAnswer
		state string
	}{{w.occ, kA, a1, "sent"}, {b, kB, b1, "sent"}, {c, kC, c1, "failed"}} {
		code, data := w.getSendByKey(t, w.agent, tc.occ, tc.key)
		assert.Equal(t, fasthttp.StatusOK, code)
		assert.Equal(t, tc.state, data["state"])
		assert.Equal(t, tc.first.env.Data["send_id"], data["send_id"])
	}
	// A key asked about under another case does not exist there.
	code, _ := w.getSendByKey(t, w.agent, b, kA)
	assert.Equal(t, fasthttp.StatusNotFound, code)
	assert.Equal(t, 2, w.providerTotal(), "only the two real sends reached the provider")
}

func TestSendState_ByKey_AuthorizationAndIsolation(t *testing.T) {
	w := newMsgWorld(t)
	key := uuid.New().String()
	require.Equal(t, fasthttp.StatusOK, w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "privado"}).code)
	for name, u := range map[string]*models.User{"juridico": w.jur, "chat only": w.chatOnly, "supervision without a tie": w.sup} {
		code, data := w.getSendByKey(t, u, w.occ, key)
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
		assert.Empty(t, data, name+": nothing leaks")
	}
	code, _ := w.getSendByKey(t, w.outsider, w.occ, key)
	assert.Contains(t, []int{fasthttp.StatusNotFound, fasthttp.StatusForbidden}, code, "another organization")
}

// "confirmed" is about the outcome of the operation being known, never about delivery.
func TestSendState_ConfirmedMeansTheOutcomeIsKnown_NotDelivery(t *testing.T) {
	type want struct {
		http            int
		sent, confirmed bool
	}
	check := func(t *testing.T, name string, env sendEnvelope, code int, w want) {
		t.Helper()
		assert.Equal(t, w.http, code, name+" http")
		assert.Equal(t, w.sent, env.Data["sent"], name+" sent")
		assert.Equal(t, w.confirmed, env.Data["confirmed"], name+" confirmed")
	}
	// sent: accepted by the provider (not "delivered": the message row is only "sent")
	w := newMsgWorld(t)
	k1 := uuid.New().String()
	ok := w.sendKeyed(t, w.agent, w.occ, k1, map[string]string{"message": "c1"})
	check(t, "sent", ok.env, ok.code, want{200, true, true})
	var msg models.Message
	require.NoError(t, w.app.DB.First(&msg, "id = ?", ok.env.Data["message_id"]).Error)
	assert.NotEqual(t, models.MessageStatusDelivered, msg.Status, "confirmed=true does not mean delivered")
	_, st := w.getSendByKey(t, w.agent, w.occ, k1)
	assert.Equal(t, true, st["sent"])
	assert.Equal(t, true, st["confirmed"])

	// failed: proven not sent
	w.m.returnError, w.m.errorMessage = true, "recusado"
	k2 := uuid.New().String()
	bad := w.sendKeyed(t, w.agent, w.occ, k2, map[string]string{"message": "c2"})
	check(t, "failed", bad.env, bad.code, want{502, false, true})
	_, st = w.getSendByKey(t, w.agent, w.occ, k2)
	assert.Equal(t, false, st["sent"])
	assert.Equal(t, true, st["confirmed"])
	w.m.returnError = false

	// uncertain: not known
	w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
	k3 := uuid.New().String()
	unk := w.sendKeyed(t, w.agent, w.occ, k3, map[string]string{"message": "c3"})
	check(t, "uncertain", unk.env, unk.code, want{202, false, false})
	_, st = w.getSendByKey(t, w.agent, w.occ, k3)
	assert.Equal(t, false, st["sent"])
	assert.Equal(t, false, st["confirmed"])
	// and so is a send still being worked on
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("id = ?", unk.env.Data["send_id"]).
		Updates(map[string]any{"state": models.SendIntentProcessing, "claimed_at": time.Now()}).Error)
	_, st = w.getSendByKey(t, w.agent, w.occ, k3)
	assert.Equal(t, "processing", st["state"])
	assert.Equal(t, false, st["sent"])
	assert.Equal(t, false, st["confirmed"])
}

// However long the clock runs, the cleanup never touches what still needs reconciling.
func TestSendIdempotency_Retention_NeverRemovesWhatStillNeedsReconciling(t *testing.T) {
	w := newMsgWorld(t)
	mk := func(state string) uuid.UUID {
		id := uuid.New()
		require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.New().String(),
			OccurrenceID: w.occ.ID, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: state,
			MessageID: uuid.New(), ClaimedAt: time.Now().Add(-time.Hour)}).Error)
		return id
	}
	processing, uncertain := mk(models.SendIntentProcessing), mk(models.SendIntentUncertain)
	w.app.PurgeSendIntents(time.Now().Add(10 * 365 * 24 * time.Hour))
	var n int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("id IN ?", []uuid.UUID{processing, uncertain}).Count(&n).Error)
	assert.EqualValues(t, 2, n, "processing and an unreconciled uncertain survive ten years")
}
