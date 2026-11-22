package handlers

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
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Recovery and consistency tests of the agent's wait: a worker that dies or stalls, the line between
// "Meta accepted it" and "the database knows", and failures of the send. See ai_send_track.go and the
// comment of reclaimStaleAIReplies for the rules these tests pin.

const farFuture = 2 * time.Hour // beyond aiWaitLease

func (w *wiring) sessionLogCount(t *testing.T, step string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, w.app.DB.Model(&models.ChatbotSessionMessage{}).
		Joins("JOIN chatbot_sessions s ON s.id = chatbot_session_messages.session_id").
		Where("s.organization_id = ? AND chatbot_session_messages.step_name = ?", w.f.org.ID, step).Count(&n).Error)
	return n
}

// waServer replaces the fake WhatsApp API with one whose answer to a message is chosen by the test.
func (w *wiring) waServer(t *testing.T, handle func(rw http.ResponseWriter, r *http.Request, body string)) *int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		if strings.Contains(body, "typing_indicator") {
			_ = json.NewEncoder(rw).Encode(map[string]bool{"success": true})
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		handle(rw, r, body)
	}))
	t.Cleanup(srv.Close)
	w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	return &calls
}

// --- 1. the worker dies and the customer writes again: the two rows are merged ------------------

func TestAIWaitRecovery_MergesAStaleRowIntoTheNewerPendingOne(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")
	rows, err := w.app.claimDueAIReplies(later(), 5) // worker 1 takes row 1 and dies
	require.NoError(t, err)
	require.Len(t, rows, 1)
	time.Sleep(15 * time.Millisecond)
	w.inbound(t, from, "quero porcelanato") // the customer writes again: row 2
	require.Len(t, w.pendingRows(t), 2)

	w.app.reclaimStaleAIReplies(time.Now().Add(farFuture))

	all := w.pendingRows(t)
	require.Len(t, all, 2)
	old, newer := all[0], all[1]
	assert.Equal(t, models.AIPendingStatusDone, old.Status)
	assert.Equal(t, aiOutcomeMerged, old.Outcome)
	assert.Equal(t, models.AIPendingStatusPending, newer.Status, "one pending row for the contact, as the index demands")
	require.Len(t, newer.MessageIDs, 2, "the messages of both rows, none lost, none twice")
	assert.Equal(t, old.MessageIDs[0], newer.MessageIDs[0], "in the order they arrived: the old message first")
	assert.True(t, newer.FirstMessageAt.Equal(old.FirstMessageAt) || newer.FirstMessageAt.Sub(old.FirstMessageAt).Abs() < time.Millisecond,
		"the group starts at the first message, so the history sent to the model leaves all of them out")
	var stuck int64
	w.app.DB.Model(&models.AIPendingReply{}).Where("organization_id = ? AND status = ?", w.f.org.ID, models.AIPendingStatusProcessing).Count(&stuck)
	assert.Zero(t, stuck, "nothing is left in processing")

	require.Equal(t, 1, w.app.drainAIWait(time.Now().Add(farFuture)))
	require.Len(t, w.ai.bodies, 1, "one model call for both messages")
	prompt := w.aiPrompt(0)
	assert.Equal(t, 1, strings.Count(prompt, "Oi"))
	assert.Equal(t, 1, strings.Count(prompt, "quero porcelanato"))
	require.Len(t, w.textSends(), 1, "one answer, no duplicate")
}

// --- 2. a failed merge does not leave a row stuck in processing --------------------------------

func TestAIWaitRecovery_FailedMergeNeverLeavesTheRowInProcessing(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	const from = "5511988880001"
	w.inbound(t, from, "Oi")
	_, err := w.app.claimDueAIReplies(later(), 5)
	require.NoError(t, err)
	w.inbound(t, from, "alguém aí?")

	// the database refuses the close of the stale row as "merged"
	require.NoError(t, w.app.DB.Exec(`CREATE OR REPLACE FUNCTION zz_refuse_merge() RETURNS trigger AS $$
		BEGIN IF NEW.outcome = 'merged' THEN RAISE EXCEPTION 'merge refused (test)'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, w.app.DB.Exec(`CREATE TRIGGER zz_refuse_merge BEFORE UPDATE ON ai_pending_replies
		FOR EACH ROW EXECUTE FUNCTION zz_refuse_merge()`).Error)
	t.Cleanup(func() {
		w.app.DB.Exec("DROP TRIGGER IF EXISTS zz_refuse_merge ON ai_pending_replies")
		w.app.DB.Exec("DROP FUNCTION IF EXISTS zz_refuse_merge()")
	})

	w.app.reclaimStaleAIReplies(time.Now().Add(farFuture))

	all := w.pendingRows(t)
	require.Len(t, all, 2)
	assert.Equal(t, models.AIPendingStatusDone, all[0].Status, "closed, not stuck")
	assert.Equal(t, aiOutcomeReclaimFailed, all[0].Outcome)
	assert.Equal(t, models.AIPendingStatusPending, all[1].Status)
	assert.Len(t, all[1].MessageIDs, 1, "the newer row is untouched by the failed merge")

	// the contact is not blocked: its pending reply is answered
	require.Equal(t, 1, w.app.drainAIWait(time.Now().Add(farFuture)))
	require.Len(t, w.textSends(), 1)
}

// --- 3. ambiguous send: a marker without a confirmation is never resent -------------------------

func TestAIWaitRecovery_MarkerWithoutConfirmationIsAmbiguousAndNeverResent(t *testing.T) {
	for _, tc := range []struct {
		name, wamid, want string
	}{
		{"no confirmation recorded", "", aiOutcomeAmbiguousSend},
		{"confirmation recorded", "wamid.HBgM-confirmada", aiOutcomeRecovered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWaitWiring(t, "não deve ser chamada")
			const from = "5511988880001"
			w.inbound(t, from, "Oi")
			contact := w.contactFor(t, from)
			rows, err := w.app.claimDueAIReplies(later(), 5)
			require.NoError(t, err)
			require.Len(t, rows, 1)

			// the worker persisted "a send started", handed the message to Meta, and died
			sendStart := time.Now()
			require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("id = ?", rows[0].ID).Update("send_started_at", sendStart).Error)
			status := models.MessageStatusPending
			if tc.wamid != "" {
				status = models.MessageStatusSent
			}
			require.NoError(t, w.app.DB.Create(&models.Message{
				BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, WhatsAppAccount: w.f.account.Name,
				ContactID: contact.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Qual ambiente?",
				Status: status, WhatsAppMessageID: tc.wamid,
			}).Error)

			w.app.reclaimStaleAIReplies(time.Now().Add(farFuture))
			assert.Equal(t, tc.want, w.pendingRows(t)[0].Outcome)
			assert.Equal(t, models.AIPendingStatusDone, w.pendingRows(t)[0].Status)
			assert.Zero(t, w.app.drainAIWait(time.Now().Add(farFuture)), "and it is never queued again")
			assert.Empty(t, w.ai.bodies, "no model call")
			assert.Empty(t, w.textSends(), "no resend")
		})
	}
}

// The marker is written before the message goes out, by the real send path.
func TestAIWaitRecovery_TheSendMarkerIsPersistedBeforeTheMessageLeaves(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	var markerSeenAtSend *time.Time
	calls := w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		var row models.AIPendingReply
		if w.app.DB.Where("organization_id = ?", w.f.org.ID).First(&row).Error == nil {
			markerSeenAtSend = row.SendStartedAt
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.ok"}}})
	})
	w.inbound(t, "5511988880001", "Oi")
	require.Equal(t, 1, w.app.drainAIWait(later()))
	assert.Equal(t, 1, *calls)
	require.NotNil(t, markerSeenAtSend, "when Meta was called, the database already said a send had started")
}

// An unknown outcome (the connection dies after the request): audited as ambiguous, not recorded as an
// answer in the session, and never resent.
func TestAIWaitRecovery_UnknownSendOutcomeIsAmbiguous(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	calls := w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		if hj, ok := rw.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close() // the request arrived; the answer never will
			}
		}
	})
	w.inbound(t, "5511988880001", "Oi")
	require.Equal(t, 1, w.app.drainAIWait(later()))

	row := w.pendingRows(t)[0]
	assert.Equal(t, aiOutcomeAmbiguousSend, row.Outcome)
	assert.NotNil(t, row.SendStartedAt)
	assert.Zero(t, w.sessionLogCount(t, "ai_response"), "an answer that may not have arrived is not recorded as an answer")
	assert.Equal(t, 1, *calls)
	assert.Zero(t, w.app.drainAIWait(later()), "and nothing is resent")
	assert.Equal(t, 1, *calls)
}

// --- 4. a definitive failure is not recorded as an answer ---------------------------------------

func TestAIWaitRecovery_DefinitiveSendFailureIsNotAnAnswer(t *testing.T) {
	w := newWaitWiring(t, "Qual ambiente seria?")
	calls := w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009,"type":"OAuthException"}}`))
	})
	w.inbound(t, "5511988880001", "Oi")
	require.Equal(t, 1, w.app.drainAIWait(later()))

	row := w.pendingRows(t)[0]
	assert.Equal(t, aiOutcomeSendFailed, row.Outcome, "a refusal that proves the message was not accepted")
	assert.Zero(t, w.sessionLogCount(t, "ai_response"), "not recorded as an answer")
	var failed int64
	w.app.DB.Model(&models.Message{}).Where("organization_id = ? AND status = ?", w.f.org.ID, models.MessageStatusFailed).Count(&failed)
	assert.EqualValues(t, 1, failed, "the message row says it failed")
	assert.Equal(t, 1, *calls)
	assert.Zero(t, w.app.drainAIWait(later()))
	assert.Equal(t, 1, *calls, "no automatic retry")
}

// --- 5. a worker that lost its claim cannot send ------------------------------------------------

func TestAIWaitRecovery_AWorkerThatLostItsClaimDoesNotSend(t *testing.T) {
	w := newWaitWiring(t, "resposta do worker antigo", "resposta do worker novo")
	w.inbound(t, "5511988880001", "Oi")

	// While the old worker waits for the model, its lease ends and a new worker takes the row and answers.
	w.ai.onCall = func(i int) {
		if i != 0 {
			return
		}
		require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("organization_id = ?", w.f.org.ID).
			Update("locked_until", time.Now().Add(-time.Minute)).Error)
		w.app.reclaimStaleAIReplies(time.Now())
		rows, err := w.app.claimDueAIReplies(time.Now().Add(time.Minute), 1)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, 2, rows[0].Attempts, "the new claim is a new attempt")
		w.app.processAIPendingReply(&rows[0])
	}
	require.Equal(t, 1, w.app.drainAIWait(later()))

	sends := w.textSends()
	require.Len(t, sends, 1, "exactly one answer left, and it is the new worker's")
	assert.Contains(t, sends[0], "resposta do worker novo")
	assert.NotContains(t, sends[0], "worker antigo")
	require.Len(t, w.ai.bodies, 2)
	row := w.pendingRows(t)[0]
	assert.Equal(t, aiOutcomeReplied, row.Outcome, "the old worker did not overwrite the new one's result")
	assert.Equal(t, 2, row.Attempts)
}

// The fence also holds against a row somebody closed meanwhile.
func TestAIWaitRecovery_AClosedRowCannotBeAnswered(t *testing.T) {
	w := newWaitWiring(t, "resposta que não pode sair")
	w.inbound(t, "5511988880001", "Oi")
	w.ai.onCall = func(i int) {
		require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("organization_id = ?", w.f.org.ID).
			Updates(map[string]any{"status": models.AIPendingStatusDone, "outcome": "closed_elsewhere"}).Error)
	}
	require.Equal(t, 1, w.app.drainAIWait(later()))
	assert.Empty(t, w.textSends())
	assert.Equal(t, "closed_elsewhere", w.pendingRows(t)[0].Outcome)
}

// --- 6. a panic during generation stops the typing indicator ------------------------------------

func TestAIWaitRecovery_PanicDuringGenerationStopsTheIndicator(t *testing.T) {
	fastTyping(t)
	w := newWaitWiring(t, "x")
	w.inbound(t, "5511988880001", "Oi")
	w.ai.onCall = func(i int) {
		require.Eventually(t, func() bool { return len(w.typingBodies()) >= 2 }, 2*time.Second, 10*time.Millisecond, "the indicator was running")
		panic("boom (test)")
	}
	require.Equal(t, 1, w.app.drainAIWait(later()))

	row := w.pendingRows(t)[0]
	assert.Equal(t, aiOutcomePanic, row.Outcome, "the row is closed, not left processing")
	assert.Equal(t, models.AIPendingStatusDone, row.Status)
	n := len(w.typingBodies())
	time.Sleep(300 * time.Millisecond) // several renewal periods
	assert.Equal(t, n, len(w.typingBodies()), "no renewal after the panic")
	assert.Empty(t, w.textSends())
}
