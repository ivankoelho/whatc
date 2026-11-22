package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A message whose answer was not delivered has not been answered. These tests pin that rule for the
// turn's lower bound (turnCustomerText): a failed send does not close what the customer said before it,
// while an unknown outcome (ambiguous_send) stays closed so nothing is ever asked or sent twice.

// firstSendFails refuses the first answer the way Meta does (#131009, a refusal that proves the message
// was not accepted) and accepts the next ones.
func (w *wiring) firstSendFails(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		if n.Add(1) == 1 {
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009,"type":"OAuthException"}}`))
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.ok" + time.Now().Format("150405.000000")}}})
	})
	return &n
}

// 1 + 2. A is not delivered (send_failed); the customer writes B; the model gets A and B, history off.
func TestAITurn_SendFailedMessageComesBackInTheNextTurn_HistoryOff(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	attempts := w.firstSendFails(t)
	const from = "5511988880001"

	w.inbound(t, from, msgA)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Equal(t, aiOutcomeSendFailed, w.pendingRows(t)[0].Outcome, "the first answer was refused")
	time.Sleep(10 * time.Millisecond)

	w.inbound(t, from, msgB)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 2)
	assert.Equal(t, msgA, w.userText(0))
	assert.Equal(t, msgA+"\n"+msgB, w.userText(1), "A was never answered: it is part of the next turn")
	assert.EqualValues(t, 2, attempts.Load())
	assert.Equal(t, aiOutcomeReplied, w.pendingRows(t)[1].Outcome)
}

// 3. With the history on, A is not in the prompt twice.
func TestAITurn_SendFailedMessageIsNotDuplicatedWhenHistoryIsOn(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, true)
	w.firstSendFails(t)
	const from = "5511988880001"

	w.inbound(t, from, msgA)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgB)
	require.Equal(t, 1, w.app.drainAIWait(later()))

	require.Len(t, w.ai.bodies, 2)
	prompt := w.aiPrompt(1)
	assert.Equal(t, 1, strings.Count(prompt, msgA), "A appears once in the second prompt (history stops before the turn)")
	assert.Equal(t, 1, strings.Count(prompt, msgB))
	assert.Equal(t, msgA+"\n"+msgB, w.userText(1))
}

// 4. An unknown outcome stays closed: the customer's message is not asked again, and nothing is sent
// automatically after it.
func TestAITurn_AmbiguousSendIsNotAskedAgainNorResent(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	var n atomic.Int32
	w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		if n.Add(1) == 1 { // the request arrives, the answer never does
			if hj, ok := rw.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
				}
			}
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.ok"}}})
	})
	const from = "5511988880001"

	w.inbound(t, from, msgA)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Equal(t, aiOutcomeAmbiguousSend, w.pendingRows(t)[0].Outcome)
	assert.EqualValues(t, 1, n.Load())
	assert.Zero(t, w.app.drainAIWait(later()), "nothing is retried by itself")
	assert.EqualValues(t, 1, n.Load(), "and nothing is resent")
	time.Sleep(10 * time.Millisecond)

	w.inbound(t, from, msgB)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 2)
	assert.Equal(t, msgB, w.userText(1), "A may have been delivered and answered: it is not asked again")
	assert.EqualValues(t, 2, n.Load(), "only the new turn sends")
}

// 5. An answered group stays out of the next turn even when a later answer failed.
func TestAITurn_AnAnsweredGroupStaysOutAfterAFailedOne(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2", "resposta 3")
	w.setHistory(t, false)
	var n atomic.Int32
	w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		if n.Add(1) == 2 { // the second answer is refused
			rw.WriteHeader(http.StatusBadRequest)
			_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009}}`))
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.ok" + time.Now().Format("150405.000000")}}})
	})
	const from = "5511988880001"

	w.inbound(t, from, msgA)
	require.Equal(t, 1, w.app.drainAIWait(later())) // answered
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgB)
	require.Equal(t, 1, w.app.drainAIWait(later())) // refused
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgC)
	require.Equal(t, 1, w.app.drainAIWait(later()))

	require.Len(t, w.ai.bodies, 3)
	assert.Equal(t, msgA, w.userText(0))
	assert.Equal(t, msgB, w.userText(1))
	assert.Equal(t, msgB+"\n"+msgC, w.userText(2), "A was answered and stays out; B was not delivered and comes back")
}
