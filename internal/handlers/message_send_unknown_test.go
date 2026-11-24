package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B3: what the attendant sees on a message depends on what is KNOWN about it. A proven refusal shows
// Meta's own (already public) reason; anything that proves nothing says, in plain words, that the message
// may have arrived. Neither shows the request URL, the phone id or the network error, and nothing is
// ever resent on its own.

func (w *wiring) lastFailedMessage(t *testing.T) models.Message {
	t.Helper()
	var m models.Message
	require.NoError(t, w.app.DB.Where("organization_id = ? AND direction = ? AND status = ?", w.f.org.ID, models.DirectionOutgoing, models.MessageStatusFailed).
		Order("created_at DESC").First(&m).Error)
	return m
}

func TestSendOutcome_UnknownIsPlainAndFreeOfInternals(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, false)
	calls := w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		if hj, ok := rw.(http.Hijacker); ok { // the request arrives, the answer never does
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
	})

	w.inbound(t, "5511988880011", msgA)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	row := w.pendingRows(t)[0]
	require.Equal(t, aiOutcomeAmbiguousSend, row.Outcome, "recorded for investigation")

	m := w.lastFailedMessage(t)
	assert.Equal(t, MessageSendUnknownText, m.ErrorMessage)
	for _, internal := range []string{"http", "127.0.0.1", "/messages", w.f.account.PhoneID, "EOF", "Post"} {
		assert.NotContains(t, m.ErrorMessage, internal, "the attendant must not see %q", internal)
	}
	assert.Equal(t, 1, calls.N(), "never resent on its own")
	assert.Zero(t, w.app.drainAIWait(later()))
	assert.Equal(t, 1, calls.N())
}

func TestSendOutcome_ARefusalIsAConfirmedFailureWithMetasPublicReason(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, false)
	w.waServer(t, func(rw http.ResponseWriter, r *http.Request, body string) {
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009,"type":"OAuthException"}}`))
	})

	w.inbound(t, "5511988880012", msgA)
	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Equal(t, aiOutcomeSendFailed, w.pendingRows(t)[0].Outcome)

	m := w.lastFailedMessage(t)
	assert.NotEqual(t, MessageSendUnknownText, m.ErrorMessage, "a proven refusal is not 'unknown'")
	assert.True(t, strings.Contains(m.ErrorMessage, "131009"), m.ErrorMessage)
	b, _ := json.Marshal(m.ErrorMessage)
	assert.NotContains(t, string(b), "127.0.0.1")
}
