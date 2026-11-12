package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const waitTestSeconds = 5

// later is a clock after every wait of these tests: the worker is driven with it instead of sleeping.
func later() time.Time { return time.Now().Add(10 * time.Minute) }

func newWaitWiring(t *testing.T, replies ...string) *wiring {
	t.Helper()
	w := newWiring(t, replies...)
	// the test database keeps rows between tests and the worker claims across organizations, as in
	// production: start from an empty queue so only this test's rows are in play
	require.NoError(t, w.app.DB.Exec("DELETE FROM ai_pending_replies").Error)
	require.NoError(t, w.app.DB.Model(w.f.agent).Update("wait_seconds", waitTestSeconds).Error)
	// history on: it is where grouped messages could be repeated, so every test checks they are not
	w.f.settings.AI.IncludeHistory, w.f.settings.AI.HistoryLimit = true, 20
	require.NoError(t, w.app.DB.Save(w.f.settings).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	return w
}

// textSends are the WhatsApp calls that are messages to the customer (not the typing indicator).
func (w *wiring) textSends() []string {
	var out []string
	for _, b := range w.wa.all() {
		if !strings.Contains(b, "typing_indicator") {
			out = append(out, b)
		}
	}
	return out
}

func (w *wiring) pendingRows(t *testing.T) []models.AIPendingReply {
	t.Helper()
	var rows []models.AIPendingReply
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.f.org.ID).Order("created_at").Find(&rows).Error)
	return rows
}

func (w *wiring) aiPrompt(i int) string {
	b, _ := json.Marshal(w.ai.bodies[i])
	return string(b)
}

// --- grouping ------------------------------------------------------------------------------

// Several messages in a row become one model call and one answer, and each wait restart moves the time.
func TestAIWait_ConsecutiveMessagesAreGroupedIntoOneAnswer(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	const from = "5511988880001"

	w.inbound(t, from, "Oi")
	rows := w.pendingRows(t)
	require.Len(t, rows, 1)
	firstDue := rows[0].DueAt
	time.Sleep(20 * time.Millisecond)
	w.inbound(t, from, "quero ver porcelanato")
	w.inbound(t, from, "para a sala")

	assert.Empty(t, w.ai.bodies, "no model call while the customer is still writing")
	assert.Empty(t, w.wa.all(), "and nothing is sent")
	rows = w.pendingRows(t)
	require.Len(t, rows, 1, "one pending reply for the contact")
	assert.Len(t, rows[0].MessageIDs, 3)
	assert.True(t, rows[0].DueAt.After(firstDue), "every message restarts the timer")
	assert.Equal(t, models.AIPendingStatusPending, rows[0].Status)

	assert.Equal(t, 0, w.app.drainAIWait(time.Now()), "not due yet")
	require.Equal(t, 1, w.app.drainAIWait(later()))

	require.Len(t, w.ai.bodies, 1, "ONE model call for the three messages")
	prompt := w.aiPrompt(0)
	for _, text := range []string{"Oi", "quero ver porcelanato", "para a sala"} {
		assert.Equal(t, 1, strings.Count(prompt, text), "%q is in the prompt exactly once (no history repeat)", text)
	}
	sends := w.textSends()
	require.Len(t, sends, 1, "one answer")
	assert.Contains(t, sends[0], "Qual ambiente seria?")

	rows = w.pendingRows(t)
	assert.Equal(t, models.AIPendingStatusDone, rows[0].Status)
	assert.Equal(t, aiOutcomeReplied, rows[0].Outcome)
}

// The same webhook delivered twice does not add the message twice.
func TestAIWait_RedeliveredMessageIsNotCountedTwice(t *testing.T) {
	w := newWaitWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive}
	require.NoError(t, w.app.DB.Create(session).Error)

	now := time.Now()
	for i := 0; i < 3; i++ {
		require.NoError(t, w.app.enqueueAIWait(w.f.agent, w.f.account, contact, session, "wamid.same", now, false))
	}
	rows := w.pendingRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.StringArray{"wamid.same"}, rows[0].MessageIDs)
}

// A customer who never stops is still answered: the hold is bounded.
func TestAIWait_HoldIsBounded(t *testing.T) {
	w := newWaitWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive}
	require.NoError(t, w.app.DB.Create(session).Error)

	first := time.Now().Add(-aiWaitMaxHold + time.Second) // the first message is almost at the limit
	require.NoError(t, w.app.enqueueAIWait(w.f.agent, w.f.account, contact, session, "wamid.a", first, false))
	require.NoError(t, w.app.enqueueAIWait(w.f.agent, w.f.account, contact, session, "wamid.b", time.Now(), false))
	rows := w.pendingRows(t)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].DueAt.After(first.Add(aiWaitMaxHold).Add(time.Second)), "due is capped at first message + max hold, got %v", rows[0].DueAt.Sub(first))
}

// A message that arrives while the answer is being produced neither interrupts it nor is lost: the
// first answer goes out, the new message becomes the next pending reply, answered once.
func TestAIWait_MessageDuringGenerationBecomesTheNextTurn(t *testing.T) {
	w := newWaitWiring(t, "Olá! Em que posso ajudar?", "Claro, qual a metragem?")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")

	w.ai.onCall = func(i int) {
		if i == 0 {
			w.inbound(t, from, "na verdade quero piso para a sala") // arrives mid-generation
		}
	}
	require.Equal(t, 1, w.app.drainAIWait(w.pendingRows(t)[0].DueAt), "the clock stops at the first row's time: the new one is not due")

	require.Len(t, w.ai.bodies, 1)
	require.Len(t, w.textSends(), 1, "the first answer was not cancelled")
	rows := w.pendingRows(t)
	require.Len(t, rows, 2)
	assert.Equal(t, models.AIPendingStatusDone, rows[0].Status)
	assert.Equal(t, models.AIPendingStatusPending, rows[1].Status, "the new message waits for its own quiet period")

	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 2, "exactly one more call")
	require.Len(t, w.textSends(), 2)
	assert.Contains(t, w.textSends()[1], "qual a metragem")
	assert.Contains(t, w.aiPrompt(1), "piso para a sala")
	assert.Contains(t, w.aiPrompt(1), "Olá! Em que posso ajudar?", "the second turn has the first answer in its history")
}

// Nothing breaks when the agent has no wait: the message is answered at once and nothing is queued.
func TestAIWait_ZeroWaitAnswersAtOnce(t *testing.T) {
	w := newWiring(t, "Resposta imediata")
	w.inbound(t, "5511988880001", "Oi")
	require.Len(t, w.ai.bodies, 1)
	require.Len(t, w.textSends(), 1)
	assert.Empty(t, w.pendingRows(t))
}

// The wait belongs to the agent that answers on that number: a number the agent is not allowed on
// is not held (it never reaches the agent).
func TestAIWait_OnlyForTheNumbersOfTheAgent(t *testing.T) {
	w := newWaitWiring(t, "x")
	other := testutil.CreateTestWhatsAppAccountWith(t, w.app.DB, w.f.org.ID, testutil.WithAccountName("outra-"+uuid.NewString()[:6]))
	var msg IncomingTextMessage
	require.NoError(t, json.Unmarshal([]byte(`{"from":"5511977770000","id":"wamid.zz","type":"text","text":{"body":"Oi"}}`), &msg))
	w.app.processIncomingMessageFull(other.PhoneID, msg, "Cliente")
	assert.Empty(t, w.pendingRows(t), "a number outside the agent's list is never held")
}

// --- restart -------------------------------------------------------------------------------

// A new process (same database) finishes what the old one left waiting: nothing is lost, one answer.
func TestAIWait_SurvivesARestart(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	w.inbound(t, "5511988880001", "Oi")
	w.inbound(t, "5511988880001", "quero porcelanato")
	require.Empty(t, w.wa.all())

	// another instance of the service: new process state, same database and outside services
	restarted := &App{Config: w.app.Config, DB: w.app.DB, Log: w.app.Log, WhatsApp: w.app.WhatsApp, HTTPClient: w.app.HTTPClient, Redis: w.app.Redis}
	require.Equal(t, 1, restarted.drainAIWait(later()))

	require.Len(t, w.ai.bodies, 1)
	assert.Contains(t, w.aiPrompt(0), "quero porcelanato")
	require.Len(t, w.textSends(), 1)
}

// A worker that died mid-answer: when its lease ends the row is taken back and answered once...
func TestAIWait_RecoversARowWhoseWorkerDied(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	w.inbound(t, "5511988880001", "Oi")
	now := time.Now()
	rows, err := w.app.claimDueAIReplies(later(), 5) // "the worker" claims it and dies
	require.NoError(t, err)
	require.Len(t, rows, 1)

	w.app.reclaimStaleAIReplies(now) // lease not over: untouched
	assert.Equal(t, models.AIPendingStatusProcessing, w.pendingRows(t)[0].Status)

	w.app.reclaimStaleAIReplies(later().Add(aiWaitLease + time.Minute))
	assert.Equal(t, models.AIPendingStatusPending, w.pendingRows(t)[0].Status, "taken back")
	require.Equal(t, 1, w.app.drainAIWait(later().Add(aiWaitLease+time.Minute)))
	require.Len(t, w.textSends(), 1)
}

// ...but if the dead worker had already sent the answer, the row is closed, never repeated.
func TestAIWait_NeverRepeatsAnAnswerThatAlreadyLeft(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	w.inbound(t, "5511988880001", "Oi")
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.textSends(), 1)

	// the worker "died" after sending but before closing the row
	require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("1 = 1").Updates(map[string]any{
		"status": models.AIPendingStatusProcessing, "outcome": "", "locked_until": time.Now().Add(-time.Minute),
		"started_at": time.Now().Add(-time.Hour), "send_started_at": time.Now().Add(-time.Hour)}).Error)
	w.app.reclaimStaleAIReplies(time.Now())

	rows := w.pendingRows(t)
	assert.Equal(t, models.AIPendingStatusDone, rows[0].Status)
	assert.Equal(t, aiOutcomeRecovered, rows[0].Outcome)
	assert.Equal(t, 0, w.app.drainAIWait(later()))
	assert.Len(t, w.textSends(), 1, "no duplicate")
	assert.Len(t, w.ai.bodies, 1)
}

// A row that keeps killing its worker is closed after a few attempts.
func TestAIWait_GivesUpAfterRepeatedFailures(t *testing.T) {
	w := newWaitWiring(t)
	w.inbound(t, "5511988880001", "Oi")
	require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("1 = 1").Updates(map[string]any{
		"status": models.AIPendingStatusProcessing, "attempts": aiWaitMaxAttempts, "locked_until": time.Now().Add(-time.Minute),
		"started_at": time.Now().Add(time.Hour)}).Error)
	w.app.reclaimStaleAIReplies(time.Now())
	assert.Equal(t, aiOutcomeAbandoned, w.pendingRows(t)[0].Outcome)
	assert.Empty(t, w.ai.bodies)
}

// Two instances polling at the same time never answer the same row twice.
func TestAIWait_TwoWorkersAnswerOnce(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	w.inbound(t, "5511988880001", "Oi")

	var wg sync.WaitGroup
	var total atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); total.Add(int32(w.app.drainAIWait(later()))) }()
	}
	wg.Wait()
	assert.EqualValues(t, 1, total.Load())
	assert.Len(t, w.ai.bodies, 1)
	assert.Len(t, w.textSends(), 1)
}

// --- human takeover ------------------------------------------------------------------------

// A human who takes over while the customer is still writing: no model call, no message.
func TestAIWait_HumanTakesOverDuringTheWait(t *testing.T) {
	w := newWaitWiring(t, "não deveria ser usada")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")
	w.inbound(t, from, "alguém aí?")
	w.openAttendance(t, w.contactFor(t, from).ID)

	require.Equal(t, 1, w.app.drainAIWait(later()))
	assert.Empty(t, w.ai.bodies, "not a single token is spent after the takeover")
	assert.Empty(t, w.wa.all())
	assert.Equal(t, aiOutcomeHumanActive, w.pendingRows(t)[0].Outcome)
}

// A human who takes over while the model is writing: the answer is discarded, never sent.
func TestAIWait_HumanTakesOverDuringGeneration(t *testing.T) {
	w := newWaitWiring(t, "resposta que não pode sair")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")
	contactID := w.contactFor(t, from).ID
	w.ai.onCall = func(i int) { w.openAttendance(t, contactID) }

	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 1)
	assert.Empty(t, w.textSends(), "the lock and recheck of the send discard the answer")
	assert.Equal(t, aiOutcomeNotSent, w.pendingRows(t)[0].Outcome)
	var discarded int64
	w.app.DB.Model(&models.AIUsageLog{}).Where("organization_id = ? AND feature = ?", w.f.org.ID, "ai_reply_discarded").Count(&discarded)
	assert.EqualValues(t, 1, discarded, "and the discard is recorded")
}

// A customer who entered a flow meanwhile is no longer the agent's.
func TestAIWait_FlowStartedMeanwhileSkipsTheAnswer(t *testing.T) {
	w := newWaitWiring(t)
	w.inbound(t, "5511988880001", "Oi")
	flow := &models.ChatbotFlow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, Name: "fluxo-" + uuid.NewString()[:6]}
	require.NoError(t, w.app.DB.Create(flow).Error)
	require.NoError(t, w.app.DB.Model(&models.ChatbotSession{}).Where("organization_id = ?", w.f.org.ID).Update("current_flow_id", flow.ID).Error)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	assert.Empty(t, w.ai.bodies)
	assert.Equal(t, aiOutcomeFlowActive, w.pendingRows(t)[0].Outcome)
}

// --- typing indicator in the wait ----------------------------------------------------------

// While the model works the customer's latest message gets the indicator; it carries type text only,
// stops with the answer, and its counters are in the audit row.
func TestAIWait_TypingWhileTheAgentWorks(t *testing.T) {
	fastTyping(t)
	w := newWaitWiring(t, "Qual ambiente seria?")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")
	w.inbound(t, from, "quero porcelanato")
	last := w.pendingRows(t)[0].LastMessageID

	w.ai.onCall = func(i int) { time.Sleep(400 * time.Millisecond) } // the model "thinks"
	require.Equal(t, 1, w.app.drainAIWait(later()))

	typing := w.typingBodies()
	require.GreaterOrEqual(t, len(typing), 2, "shown, then renewed while the model works")
	for _, b := range typing {
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(b), &body))
		assert.Equal(t, last, body["message_id"], "for the customer's latest message")
		assert.Equal(t, map[string]any{"type": "text"}, body["typing_indicator"])
	}
	require.Len(t, w.textSends(), 1)

	n := len(w.typingBodies())
	time.Sleep(250 * time.Millisecond)
	assert.Equal(t, n, len(w.typingBodies()), "stopped when the answer was ready")

	row := w.pendingRows(t)[0]
	assert.Equal(t, 1, row.TypingStarts)
	assert.GreaterOrEqual(t, row.TypingRenewals, 1)
	assert.Zero(t, row.TypingFailures)
}

// No agent answer, no indicator: with the wait off nothing is shown (and nothing changes).
func TestAIWait_NoTypingWithoutWait(t *testing.T) {
	fastTyping(t)
	w := newWiring(t, "ok")
	w.inbound(t, "5511988880001", "Oi")
	assert.Empty(t, w.typingBodies())
}

// A broken or hanging typing API never blocks nor delays the answer, and is audited.
func TestAIWait_TypingFailureDoesNotBlockTheAnswer(t *testing.T) {
	for _, mode := range []string{"refuse", "hang"} {
		t.Run(mode, func(t *testing.T) {
			fastTyping(t)
			w := newWaitWiring(t, "Qual ambiente seria?")
			var mu sync.Mutex
			var sent []string
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body := string(raw)
				if strings.Contains(body, "typing_indicator") {
					if mode == "refuse" {
						rw.WriteHeader(http.StatusBadRequest)
						_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009}}`))
						return
					}
					select {
					case <-release:
					case <-r.Context().Done():
					}
					return
				}
				mu.Lock()
				sent = append(sent, body)
				mu.Unlock()
				_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.mock_" + uuid.NewString()[:8]}}})
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)

			w.inbound(t, "5511988880001", "Oi")
			w.ai.onCall = func(i int) { time.Sleep(300 * time.Millisecond) }
			started := time.Now()
			require.Equal(t, 1, w.app.drainAIWait(later()))
			assert.Less(t, time.Since(started), 3*time.Second)

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, sent, 1, "the answer went out")
			assert.Contains(t, sent[0], "Qual ambiente seria?")
			row := w.pendingRows(t)[0]
			assert.Equal(t, aiOutcomeReplied, row.Outcome)
			assert.GreaterOrEqual(t, row.TypingFailures, 1, "the failures are audited")
			assert.NotContains(t, row.TypingLastError, "access_token")
		})
	}
}

// --- tokens --------------------------------------------------------------------------------

// The wait does not make the model run more often: N messages, one call, one usage row of the turn.
func TestAIWait_DoesNotSpendMoreTokensThanOneCallPerGroup(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	for _, text := range []string{"Oi", "boa tarde", "queria ver pisos", "porcelanato", "para a sala"} {
		w.inbound(t, "5511988880001", text)
	}
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Equal(t, 0, w.app.drainAIWait(later()), "and nothing is left to run")

	assert.Len(t, w.ai.bodies, 1, "five messages, one model call")
	var logs []models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ? AND feature = ?", w.f.org.ID, aiFeatureChatbotReply).Find(&logs).Error)
	assert.Len(t, logs, 1)
}

// The real worker loop: a wait that started under one process is answered by the next one, once.
func TestAIWait_WorkerLoopAnswersAfterARestart(t *testing.T) {
	old := aiWaitPollEvery
	aiWaitPollEvery = 50 * time.Millisecond
	t.Cleanup(func() { aiWaitPollEvery = old })
	w := newWaitWiring(t, "Qual ambiente seria?")
	require.NoError(t, w.app.DB.Model(w.f.agent).Update("wait_seconds", 1).Error)

	w.inbound(t, "5511988880001", "Oi")
	w.inbound(t, "5511988880001", "quero porcelanato")

	// process 1 starts and is stopped before the quiet period ends
	ctx1, stop1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { w.app.StartAIWaitWorker(ctx1); close(done1) }()
	time.Sleep(150 * time.Millisecond)
	stop1()
	<-done1
	require.Empty(t, w.textSends(), "nothing was due yet")
	require.Len(t, w.pendingRows(t), 1)

	// process 2 (fresh state, same database) picks it up
	restarted := &App{Config: w.app.Config, DB: w.app.DB, Log: w.app.Log, WhatsApp: w.app.WhatsApp, HTTPClient: w.app.HTTPClient, Redis: w.app.Redis}
	ctx2, stop2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { restarted.StartAIWaitWorker(ctx2); close(done2) }()
	require.Eventually(t, func() bool { return len(w.textSends()) == 1 }, 6*time.Second, 25*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // and it does not answer again
	stop2()
	<-done2

	assert.Len(t, w.textSends(), 1)
	assert.Len(t, w.ai.bodies, 1)
	assert.Contains(t, w.aiPrompt(0), "quero porcelanato")
}
