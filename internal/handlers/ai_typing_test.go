package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastTyping(t *testing.T) {
	t.Helper()
	r, c, to := aiTypingRenewEvery, aiTypingCheckEvery, aiTypingCallTimeout
	aiTypingRenewEvery, aiTypingCheckEvery, aiTypingCallTimeout = 80*time.Millisecond, 20*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { aiTypingRenewEvery, aiTypingCheckEvery, aiTypingCallTimeout = r, c, to })
}

func (w *wiring) typingBodies() []string {
	var out []string
	for _, b := range w.wa.all() {
		if strings.Contains(b, "typing_indicator") {
			out = append(out, b)
		}
	}
	return out
}

// The indicator is shown at once, renewed while the agent works, only with type "text" and only for
// the message it was started for; and no renewal leaves after stop.
func TestAITyping_ShowsRenewsAndStops(t *testing.T) {
	fastTyping(t)
	w := newWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)

	stop := w.app.startAITyping(w.f.account, contact.ID, "wamid.cliente1")
	require.Eventually(t, func() bool { return len(w.typingBodies()) >= 3 }, 3*time.Second, 10*time.Millisecond, "shown, then renewed")
	stats := stop()
	assert.Equal(t, 1, stats.Starts)
	assert.GreaterOrEqual(t, stats.Renewals, 2)
	assert.Zero(t, stats.Failures)

	for _, b := range w.typingBodies() {
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(b), &body))
		assert.Equal(t, "wamid.cliente1", body["message_id"])
		assert.Equal(t, map[string]any{"type": "text"}, body["typing_indicator"], "only text is ever sent")
	}

	n := len(w.typingBodies())
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, n, len(w.typingBodies()), "nothing is sent after stop")
	stop() // idempotent
}

// A human taking over ends the renewals without anyone calling stop.
func TestAITyping_StopsWhenAHumanTakesOver(t *testing.T) {
	fastTyping(t)
	w := newWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)

	stop := w.app.startAITyping(w.f.account, contact.ID, "wamid.cliente1")
	require.Eventually(t, func() bool { return len(w.typingBodies()) >= 2 }, 3*time.Second, 10*time.Millisecond)
	w.openAttendance(t, contact.ID)

	require.Eventually(t, func() bool {
		n := len(w.typingBodies())
		time.Sleep(200 * time.Millisecond)
		return n == len(w.typingBodies())
	}, 3*time.Second, 10*time.Millisecond, "renewals stop on their own")
	assert.True(t, stop().StoppedByHuman)
}

// The API failing or hanging never delays the caller: stop returns at once, the failure is counted
// with a short redacted message, and nothing panics.
func TestAITyping_FailureAndTimeoutNeverBlock(t *testing.T) {
	fastTyping(t)
	w := newWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)

	hang := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(hang); srv.Close() })
	w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)

	started := time.Now()
	stop := w.app.startAITyping(w.f.account, contact.ID, "wamid.cliente1")
	assert.Less(t, time.Since(started), 100*time.Millisecond, "starting it does not wait for the API")

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 3*time.Second, 10*time.Millisecond,
		"a call that never answers ends at its own deadline (150 ms) and the next renewal still goes out")
	stats := stop()
	assert.GreaterOrEqual(t, stats.Failures, 1, "the timeouts are counted as failures")
	assert.LessOrEqual(t, len(stats.LastError), 200)
	assert.NotContains(t, stats.LastError, "access", "no credentials in the audit text")

	begin := time.Now()
	stop()
	assert.Less(t, time.Since(begin), 50*time.Millisecond, "stop does not wait for the API either")
}

// A refused call (HTTP 400, the same answer Meta gives for an invalid message) is a counted failure, not an error for the caller.
func TestAITyping_RefusedCallIsCounted(t *testing.T) {
	fastTyping(t)
	w := newWiring(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = rw.Write([]byte(`{"error":{"message":"(#131009) Parameter value is not valid","code":131009}}`))
	}))
	t.Cleanup(srv.Close)
	w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)

	stop := w.app.startAITyping(w.f.account, contact.ID, "wamid.velha")
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 3*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	stats := stop()
	assert.GreaterOrEqual(t, stats.Failures, 1)
	assert.Zero(t, stats.Starts)
	assert.Contains(t, stats.LastError, "131009")
	_ = models.Contact{}
}
