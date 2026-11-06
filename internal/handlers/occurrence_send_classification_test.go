package handlers_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// failed means "proven NOT sent"; uncertain means "the provider may have it". What decides is the
// evidence about the send (where it stopped, what the provider said), never the HTTP status alone.
// Every case also proves that nothing is sent again by asking again with the same key.

func (w *msgWorld) lastIntent(t *testing.T) models.OccurrenceSendIntent {
	t.Helper()
	rows := w.intents(t)
	require.NotEmpty(t, rows)
	return rows[len(rows)-1]
}

func (w *msgWorld) chooseProviderAnswer(status, code int, raw string) {
	w.m.mu.Lock()
	w.m.failStatus, w.m.failCode, w.m.failRaw = status, code, raw
	w.m.mu.Unlock()
}

// 1. The provider's answer decides, not the status alone.
func TestSendClassification_ProviderAnswer_DecidesNotTheStatusAlone(t *testing.T) {
	cases := []struct {
		name         string
		status, code int
		raw          string
		result       string
		http         int
	}{
		{"documented refusal (100)", http.StatusBadRequest, 100, "", "failed", fasthttp.StatusBadGateway},
		{"expired token (190)", http.StatusUnauthorized, 190, "", "failed", fasthttp.StatusBadGateway},
		{"documented throttle (130429)", http.StatusTooManyRequests, 130429, "", "failed", fasthttp.StatusBadGateway},
		{"4xx with a code Meta does not document as a refusal", http.StatusBadRequest, 999999, "", "uncertain", fasthttp.StatusAccepted},
		{"4xx without a Meta body", http.StatusBadRequest, 0, "<html>bad</html>", "uncertain", fasthttp.StatusAccepted},
		{"408 even with a refusal code: it describes the transport", http.StatusRequestTimeout, 100, "", "uncertain", fasthttp.StatusAccepted},
		{"500", http.StatusInternalServerError, 1, "", "uncertain", fasthttp.StatusAccepted},
		{"502 from a proxy", http.StatusBadGateway, 0, "<html>proxy</html>", "uncertain", fasthttp.StatusAccepted},
		{"503 even with a refusal code", http.StatusServiceUnavailable, 100, "", "uncertain", fasthttp.StatusAccepted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newMsgWorld(t)
			w.chooseProviderAnswer(c.status, c.code, c.raw)
			key := uuid.NewString()
			a := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "classificar"})
			assert.Equal(t, c.http, a.code)
			assert.Equal(t, c.result, a.env.Data["result"])
			assert.Equal(t, c.result == "failed", a.env.Data["confirmed"], "confirmed = the outcome is known")
			assert.Equal(t, false, a.env.Data["sent"])
			assert.Equal(t, c.result, string(w.lastIntent(t).State))

			// The provider recovers; asking again with the same key sends nothing and answers the same.
			w.chooseProviderAnswer(0, 0, "")
			b := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "classificar"})
			assert.True(t, b.replay)
			assert.Equal(t, c.http, b.code)
			assert.Zero(t, w.providerTotal(), "no automatic resend, no resend on a repeat")
		})
	}
}

// 2. Stopped before the provider was reached: failed, whatever it was that stopped.
func TestSendClassification_BeforeTheProvider_IsFailed(t *testing.T) {
	t.Run("the attempt record cannot be written", func(t *testing.T) {
		w := newMsgWorld(t)
		const cb = "test:fail_attempt_row"
		require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
			if tx.Statement.Table == "audit_logs" {
				_ = tx.AddError(errors.New("simulated audit failure"))
			}
		}))
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "sem rastro"})
		_ = w.app.DB.Callback().Create().Remove(cb)
		assert.Equal(t, fasthttp.StatusInternalServerError, a.code)
		assert.Equal(t, "failed", a.env.Data["result"])
		assert.Equal(t, true, a.env.Data["confirmed"])
		assert.Zero(t, w.providerTotal(), "nothing reached the provider")
		assert.Zero(t, w.outgoingMessages(t), "and no message row exists")
	})
	t.Run("the message row cannot be written", func(t *testing.T) {
		w := newMsgWorld(t)
		const cb = "test:fail_message_row_2"
		require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
			if tx.Statement.Table == "messages" {
				_ = tx.AddError(errors.New("simulated message failure"))
			}
		}))
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "sem linha"})
		_ = w.app.DB.Callback().Create().Remove(cb)
		assert.Equal(t, fasthttp.StatusInternalServerError, a.code)
		assert.Equal(t, "failed", a.env.Data["result"])
		assert.Zero(t, w.providerTotal())
	})
	t.Run("the connection was never established", func(t *testing.T) {
		w := newMsgWorld(t)
		w.m.close()
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "ninguem"})
		assert.Equal(t, "failed", a.env.Data["result"])
		assert.Equal(t, true, a.env.Data["confirmed"])
	})
}

// 3. The audit result row cannot be written AFTER the call: the answer follows the provider's, never
// the audit failure. A possibly delivered message must not become a confirmed failure.
func TestSendClassification_AuditFailureAfterTheCall_NeverBecomesAConfirmedFailure(t *testing.T) {
	failResultRow := func(w *msgWorld) func() {
		var n atomic.Int32
		const cb = "test:fail_result_row_2"
		require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
			if tx.Statement.Table == "audit_logs" && n.Add(1) > 1 { // the attempt row passes, the result row does not
				_ = tx.AddError(errors.New("simulated audit failure"))
			}
		}))
		return func() { _ = w.app.DB.Callback().Create().Remove(cb) }
	}
	t.Run("accepted by the provider: sent", func(t *testing.T) {
		w := newMsgWorld(t)
		undo := failResultRow(w)
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "aceita sem auditoria"})
		undo()
		assert.Equal(t, fasthttp.StatusOK, a.code)
		assert.Equal(t, "sent", a.env.Data["result"])
		assert.Equal(t, 1, w.providerTotal())
	})
	t.Run("no answer in time: uncertain, not failed", func(t *testing.T) {
		w := newMsgWorld(t)
		w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
		undo := failResultRow(w)
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "tardia sem auditoria"})
		undo()
		assert.Equal(t, fasthttp.StatusAccepted, a.code)
		assert.Equal(t, "uncertain", a.env.Data["result"])
		assert.Equal(t, false, a.env.Data["confirmed"])
		assert.Equal(t, "uncertain", string(w.lastIntent(t).State))
	})
	t.Run("refused by the provider: still a proven failure", func(t *testing.T) {
		w := newMsgWorld(t)
		w.chooseProviderAnswer(http.StatusBadRequest, 100, "")
		undo := failResultRow(w)
		a := w.sendKeyed(t, w.agent, w.occ, uuid.NewString(), map[string]string{"message": "recusada sem auditoria"})
		undo()
		assert.Equal(t, "failed", a.env.Data["result"])
		assert.Zero(t, w.providerTotal())
	})
}

// 4. The send intent itself cannot be closed after the call: the claim stays "processing", the repeat
// is told so and sends nothing, and reconciliation settles it only from evidence.
func TestSendClassification_IntentCannotBeClosedAfterTheCall_ReconcilesByEvidenceOnly(t *testing.T) {
	failClose := func(w *msgWorld) func() {
		const cb = "test:fail_intent_close"
		require.NoError(t, w.app.DB.Callback().Update().Before("gorm:update").Register(cb, func(tx *gorm.DB) {
			if tx.Statement.Table == "occurrence_send_intents" {
				_ = tx.AddError(errors.New("simulated intent update failure"))
			}
		}))
		return func() { _ = w.app.DB.Callback().Update().Remove(cb) }
	}
	later := func(w *msgWorld) { w.app.ReconcileSendIntents(time.Now().Add(3 * time.Minute)) }

	t.Run("provider accepted: the message row proves it, reconciled to sent", func(t *testing.T) {
		w := newMsgWorld(t)
		undo := failClose(w)
		key := uuid.NewString()
		a := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "aceita sem fechar"})
		assert.Equal(t, fasthttp.StatusOK, a.code, "the caller is told what happened")
		assert.Equal(t, "processing", string(w.lastIntent(t).State))
		b := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "aceita sem fechar"})
		assert.Equal(t, fasthttp.StatusAccepted, b.code)
		assert.Equal(t, "processing", b.env.Data["result"])
		undo()
		later(w)
		assert.Equal(t, "sent", string(w.lastIntent(t).State))
		assert.Equal(t, 1, w.providerTotal(), "one message, whatever the repeats")
	})
	t.Run("no answer in time: uncertain, never failed, even with a failed message row", func(t *testing.T) {
		w := newMsgWorld(t)
		w.lateProvider(400*time.Millisecond, 150*time.Millisecond)
		undo := failClose(w)
		key := uuid.NewString()
		a := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "tardia sem fechar"})
		assert.Equal(t, fasthttp.StatusAccepted, a.code)
		undo()
		// Lose the audit trail too: all that is left is a message row that says "failed" (the client gave up).
		require.NoError(t, w.app.DB.Where("organization_id = ? AND resource_id = ?", w.org.ID, w.occ.ID).Delete(&models.AuditLog{}).Error)
		later(w)
		assert.Equal(t, "uncertain", string(w.lastIntent(t).State), "a failed message row proves nothing about the provider")
		w.lateProvider(0, 2*time.Second)
		b := w.sendKeyed(t, w.agent, w.occ, key, map[string]string{"message": "tardia sem fechar"})
		assert.Equal(t, fasthttp.StatusAccepted, b.code)
		require.Eventually(t, func() bool { return w.providerTotal() == 1 }, 2*time.Second, 20*time.Millisecond)
		time.Sleep(200 * time.Millisecond)
		assert.Equal(t, 1, w.providerTotal(), "no automatic resend")
	})
}
