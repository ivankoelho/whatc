package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// M3 of the review: without the database protection (one open send per case) the endpoint must REFUSE to send,
// never fall back to accepting concurrent sends under different keys. Each case damages the index on the shared
// test database and puts it back afterwards.

func restoreGuard(t *testing.T, w *msgWorld) {
	t.Helper()
	t.Cleanup(func() {
		_ = w.app.DB.Exec("DROP INDEX IF EXISTS " + database.OpenSendIntentIndexName).Error
		require.NoError(t, database.EnsureOpenSendIntentUniqueness(w.app.DB))
		w.app.ResetSendGuardForTest()
	})
}

func TestSendGuard_IndexAbsent_TheSendIsRefusedAndNothingIsClaimedOrSent(t *testing.T) {
	w := newMsgWorld(t)
	restoreGuard(t, w)
	defer handlers.SetSendGuardTTLForTest(time.Minute, 0)() // a bad check is re-read at once
	w.app.ResetSendGuardForTest()
	require.NoError(t, w.app.DB.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)

	for i := 0; i < 3; i++ { // different keys, different texts: none may go through
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "sem protecao"})
		assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
		assert.Equal(t, "send_guard_unavailable", a.env.ErrorType)
	}
	assert.Equal(t, 0, w.providerTotal(), "nothing reached the customer")
	assert.Empty(t, w.intents(t), "nothing was claimed")
	assert.EqualValues(t, 0, w.outgoingMessages(t))

	// Only the sends are refused: asking about the case's open send still works (it only reads).
	code, _ := w.getOpen(t, w.agent, w.occ)
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

func TestSendGuard_IndexInvalidOrWrong_TheSendIsRefused(t *testing.T) {
	w := newMsgWorld(t)
	restoreGuard(t, w)
	defer handlers.SetSendGuardTTLForTest(time.Minute, 0)()
	for name, ddl := range map[string]string{
		"not unique":       `CREATE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"covers one state": `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing'`,
		"other columns":    `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, w.app.DB.Exec("DROP INDEX IF EXISTS uq_send_intent_open").Error)
			require.NoError(t, w.app.DB.Exec(ddl).Error)
			w.app.ResetSendGuardForTest()
			a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "protecao errada"})
			assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
			assert.Equal(t, "send_guard_unavailable", a.env.ErrorType)
			assert.Equal(t, 0, w.providerTotal())
		})
	}
}

// Once the migration is run, the same server recovers by itself, without a restart; a good check is remembered.
func TestSendGuard_IndexPresent_SendsWork_AndARecoveredServerNeedsNoRestart(t *testing.T) {
	w := newMsgWorld(t)
	restoreGuard(t, w)
	defer handlers.SetSendGuardTTLForTest(time.Minute, 0)()
	w.app.ResetSendGuardForTest()

	// present: works
	ok := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "com protecao"})
	require.Equal(t, fasthttp.StatusOK, ok.code)

	// a good answer is remembered (the index disappearing is only noticed after the TTL, like any cache)...
	require.NoError(t, w.app.DB.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	still := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "ainda lembrado"})
	assert.Equal(t, fasthttp.StatusOK, still.code, "within the TTL of a good check")

	// ...and once it is forgotten the damage is seen and the send refused
	w.app.ResetSendGuardForTest()
	refused := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "depois do ttl"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, refused.code)

	// the migration is run: the same App sends again, with no restart
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(w.app.DB))
	back := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "recuperado"})
	assert.Equal(t, fasthttp.StatusOK, back.code)
	assert.Equal(t, 1, w.mockHas("recuperado"))
	assert.Equal(t, 0, w.mockHas("depois do ttl"))
}
