package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// Fail closed: without the database protection (one open send per case) the endpoint REFUSES to send, and it
// checks it inside every new claim (no cache, no trust window). The shared test database is never touched: the
// protection these tests break lives on a scratch database of its own and the App is pointed at it through the
// verifier, so nothing here can disturb, or be disturbed by, another test.

type scratchGuard struct{ db *gorm.DB }

func newScratchGuard(t *testing.T, w *msgWorld) *scratchGuard {
	t.Helper()
	db := testutil.ScratchDatabase(t)
	require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
	g := &scratchGuard{db: db}
	w.app.SetOpenSendGuardVerifierForTest(func(_ *gorm.DB) error { return database.VerifyOpenSendIntentGuard(db) })
	t.Cleanup(func() { w.app.SetOpenSendGuardVerifierForTest(nil) })
	return g
}

func (g *scratchGuard) drop(t *testing.T) {
	t.Helper()
	require.NoError(t, g.db.Exec("DROP INDEX IF EXISTS "+database.OpenSendIntentIndexName).Error)
}

func (g *scratchGuard) create(t *testing.T) {
	t.Helper()
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(g.db))
}

func TestSendGuard_IndexAbsent_TheSendIsRefusedAndNothingIsClaimedOrSent(t *testing.T) {
	w := newMsgWorld(t)
	g := newScratchGuard(t, w)
	g.drop(t)

	for i := 0; i < 3; i++ { // different keys, different texts: none may go through
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "sem protecao"})
		assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
		assert.Equal(t, "send_guard_unavailable", a.env.ErrorType)
	}
	assert.Equal(t, 0, w.providerTotal(), "nothing reached the customer")
	assert.Empty(t, w.intents(t), "the claim was rolled back: nothing was claimed")
	assert.EqualValues(t, 0, w.outgoingMessages(t))

	// Only the sends are refused: asking about the case's open send still works (it only reads).
	code, _ := w.getOpen(t, w.agent, w.occ)
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

func TestSendGuard_IndexInvalidOrWrong_TheSendIsRefused(t *testing.T) {
	w := newMsgWorld(t)
	g := newScratchGuard(t, w)
	for name, ddl := range map[string]string{
		"not unique":       `CREATE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"covers one state": `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing'`,
		"other columns":    `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"not partial":      `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind)`,
	} {
		t.Run(name, func(t *testing.T) {
			g.drop(t)
			require.NoError(t, g.db.Exec(ddl).Error)
			a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "protecao errada"})
			assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
			assert.Equal(t, "send_guard_unavailable", a.env.ErrorType)
			assert.Equal(t, 0, w.providerTotal())
			assert.Empty(t, w.intents(t))
		})
	}
	t.Run("invalid", func(t *testing.T) {
		g.drop(t)
		g.create(t)
		require.NoError(t, g.db.Exec(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'uq_send_intent_open'::regclass`).Error)
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "invalida"})
		assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
		assert.Equal(t, 0, w.providerTotal())
	})
}

// No cache and no trust window: the check is made inside every new claim, so an index that disappears right after a
// good send is seen by the very next one; and once the migration is run the same server sends again, no restart.
func TestSendGuard_NoTrustWindow_TheNextSendSeesTheIndexGone_AndRecoversWithoutRestart(t *testing.T) {
	w := newMsgWorld(t)
	g := newScratchGuard(t, w)
	g.create(t)

	ok := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "com protecao"})
	require.Equal(t, fasthttp.StatusOK, ok.code)

	g.drop(t) // right after a good check
	refused := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "logo depois"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, refused.code, "there is no window in which the earlier good check is trusted")
	assert.Equal(t, 0, w.mockHas("logo depois"))

	g.create(t) // the migration is run
	back := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "recuperado"})
	assert.Equal(t, fasthttp.StatusOK, back.code)
	assert.Equal(t, 1, w.mockHas("recuperado"))
}

// A repeat of a key that already exists inserts nothing and needs no new protection: it still replays its answer
// (so a page that lost an answer is not told "closed" by a guard failure), while a NEW key is refused.
func TestSendGuard_RepeatOfAnExistingKeyStillReplays_WhileANewKeyIsRefused(t *testing.T) {
	w := newMsgWorld(t)
	g := newScratchGuard(t, w)
	g.create(t)
	key := uuid.NewString()
	first := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "ja enviada"})
	require.Equal(t, fasthttp.StatusOK, first.code)

	g.drop(t)
	again := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "ja enviada"})
	assert.Equal(t, fasthttp.StatusOK, again.code)
	assert.True(t, again.replay)
	fresh := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "chave nova"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, fresh.code)
	assert.Equal(t, 1, w.providerTotal())
}

// The check is made on the claim's own transaction, after the INSERT, and a refusal rolls the INSERT back: the
// shared table never keeps a claim that was made without the protection.
func TestSendGuard_TheCheckRunsInsideTheClaimTransaction_AndARefusalRollsItBack(t *testing.T) {
	w := newMsgWorld(t)
	var sawTx, sawRowInTx bool
	w.app.SetOpenSendGuardVerifierForTest(func(tx *gorm.DB) error {
		sawTx = tx != nil
		var n int64
		tx.Model(&models.OccurrenceSendIntent{}).Where("occurrence_id = ?", w.occ.ID).Count(&n)
		sawRowInTx = n == 1 // the claim's own INSERT is visible here: the check follows it, in the same transaction
		return assert.AnError
	})
	defer w.app.SetOpenSendGuardVerifierForTest(nil)

	a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "dentro da transacao"})
	assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
	assert.True(t, sawTx)
	assert.True(t, sawRowInTx)
	assert.Empty(t, w.intents(t), "rolled back")
	assert.Equal(t, 0, w.providerTotal())
}
