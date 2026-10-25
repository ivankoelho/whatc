package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"context"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// fakeMeta is the simulated provider: no real number is ever contacted.
// Modes: ok | 400 | 429 | 500 | drop (connection closed without an answer).
type fakeMeta struct {
	srv  *httptest.Server
	mu   sync.Mutex
	mode string
	reqs []fakeReq
	seq  int
	pfx  string
}

type fakeReq struct {
	Path     string
	Template string
	Lang     string
	Params   []string
	To       string
}

func newFakeMeta(t *testing.T) *fakeMeta {
	f := &fakeMeta{mode: "ok", pfx: uuid.NewString()[:8]}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To       string `json:"to"`
			Template struct {
				Name     string `json:"name"`
				Language struct {
					Code string `json:"code"`
				} `json:"language"`
				Components []struct {
					Parameters []struct {
						Text string `json:"text"`
					} `json:"parameters"`
				} `json:"components"`
			} `json:"template"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		req := fakeReq{Path: r.URL.Path, Template: body.Template.Name, Lang: body.Template.Language.Code, To: body.To}
		for _, c := range body.Template.Components {
			for _, p := range c.Parameters {
				req.Params = append(req.Params, p.Text)
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.seq++
		mode, seq := f.mode, f.seq
		f.mu.Unlock()
		switch mode {
		case "drop":
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		case "400":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid parameter","code":100}}`))
		case "429":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"Too many messages","code":130429}}`))
		case "500":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"Internal error","code":2}}`))
		default:
			_, _ = w.Write([]byte(fmt.Sprintf(`{"messaging_product":"whatsapp","messages":[{"id":"wamid.%s-%d"}]}`, f.pfx, seq)))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMeta) setMode(m string) { f.mu.Lock(); f.mode = m; f.mu.Unlock() }
func (f *fakeMeta) count() int       { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }
func (f *fakeMeta) last() fakeReq    { f.mu.Lock(); defer f.mu.Unlock(); return f.reqs[len(f.reqs)-1] }

// notifWorld: iso world + account/templates + an active org-level automation +
// the simulated provider wired in as the app's WhatsApp client.
type notifWorld struct {
	*autoWorld
	meta *fakeMeta
}

func newNotifWorld(t *testing.T) *notifWorld {
	t.Helper()
	w := &notifWorld{autoWorld: newAutoWorld(t), meta: newFakeMeta(t)}
	w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), w.meta.srv.URL)
	w.app.Config.WhatsApp.AutomationSendingEnabled = true
	// The worker is global (it serves every organization), so intents left by other
	// tests in this database would be processed here. Park them before starting.
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).
		Where("status IN ?", []string{models.NotificationPending, models.NotificationProcessing}).
		Update("status", models.NotificationCancelled).Error)
	code, body := w.save(t, w.admin.ID, w.cfg(nil))
	require.Equal(t, fasthttp.StatusOK, code, body)
	return w
}

func (w *notifWorld) intent(t *testing.T, occID uuid.UUID) models.OccurrenceNotification {
	t.Helper()
	var n models.OccurrenceNotification
	require.NoError(t, w.app.DB.Where("occurrence_id = ?", occID).First(&n).Error)
	return n
}

func (w *notifWorld) run() int { return w.app.ProcessDueNotifications(context.Background(), 50) }

func (w *notifWorld) attempts(t *testing.T, id uuid.UUID) []models.OccurrenceNotificationAttempt {
	var as []models.OccurrenceNotificationAttempt
	w.app.DB.Where("notification_id = ?", id).Order("number").Find(&as)
	return as
}

func TestNotifier_SendsOnceWithTheRightAccountTemplateAndParameters(t *testing.T) {
	w := newNotifWorld(t)
	id := w.forward(t, nil)
	n := w.intent(t, id)
	require.Equal(t, models.NotificationPending, n.Status, "persisted before any send")
	require.Zero(t, w.meta.count(), "the request never calls the provider")

	assert.Equal(t, 1, w.run())
	assert.Equal(t, 1, w.meta.count())
	req := w.meta.last()
	assert.Contains(t, req.Path, w.account.PhoneID, "the configured logistics account's number")
	assert.Equal(t, "recebimento_logistica", req.Template)
	assert.Equal(t, "pt_BR", req.Lang)
	var occ models.Occurrence
	require.NoError(t, w.app.DB.First(&occ, id).Error)
	var contact models.Contact
	require.NoError(t, w.app.DB.First(&contact, occ.ContactID).Error)
	assert.Equal(t, []string{contact.ProfileName, "45872", occ.ProtocolNumber}, req.Params)

	n = w.intent(t, id)
	assert.Equal(t, models.NotificationAccepted, n.Status, "accepted is not delivered")
	assert.Equal(t, "wamid."+w.meta.pfx+"-1", n.ProviderMessageID)
	assert.NotNil(t, n.AcceptedAt)
	assert.Nil(t, n.DeliveredAt)
	as := w.attempts(t, n.ID)
	require.Len(t, as, 1)
	assert.Equal(t, "accepted", as[0].Outcome)

	var msg models.Message
	require.NoError(t, w.app.DB.First(&msg, "id = ?", n.MessageID).Error)
	assert.Equal(t, w.account.Name, msg.WhatsAppAccount, "the message belongs to the logistics account, not the SAC's")
	assert.Equal(t, n.ID.String(), msg.Metadata["occurrence_notification_id"])

	// Running again must not send again, and the case itself is untouched by the send.
	assert.Equal(t, 0, w.run())
	assert.Equal(t, 1, w.meta.count())
	require.NoError(t, w.app.DB.First(&occ, id).Error)
	assert.Equal(t, models.GeneralStateNew, occ.GeneralState, "a message never concludes the case")
	assert.Nil(t, occ.ClosedAt)
}

func TestNotifier_ProviderStatusesAdvanceItMonotonically(t *testing.T) {
	w := newNotifWorld(t)
	id := w.forward(t, nil)
	w.run()
	n := w.intent(t, id)
	events := func() int64 {
		var c int64
		w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", id, models.OccurrenceEventCustomerNotice).Count(&c)
		return c
	}
	status := func() string { return w.intent(t, id).Status }

	w.app.UpdateMessageStatusForTest(n.ProviderMessageID, "delivered")
	assert.Equal(t, models.NotificationDelivered, status())
	e := events()
	w.app.UpdateMessageStatusForTest(n.ProviderMessageID, "delivered") // duplicated webhook
	assert.Equal(t, models.NotificationDelivered, status())
	assert.Equal(t, e, events(), "a duplicate webhook writes nothing")
	w.app.UpdateMessageStatusForTest(n.ProviderMessageID, "read")
	assert.Equal(t, models.NotificationRead, status())
	w.app.UpdateMessageStatusForTest(n.ProviderMessageID, "delivered") // arrives late
	w.app.UpdateMessageStatusForTest(n.ProviderMessageID, "sent")
	assert.Equal(t, models.NotificationRead, status(), "out-of-order evidence never goes backwards")
	got := w.intent(t, id)
	assert.NotNil(t, got.DeliveredAt)
	assert.NotNil(t, got.ReadAt)
}

func TestNotifier_DefiniteRefusalFailsWithoutBlockingTheCase(t *testing.T) {
	w := newNotifWorld(t)
	w.meta.setMode("400")
	id := w.forward(t, nil)
	w.run()
	n := w.intent(t, id)
	assert.Equal(t, models.NotificationFailed, n.Status)
	assert.Equal(t, "provider_rejected", n.Reason)
	assert.Contains(t, n.LastError, "Invalid parameter")
	assert.Equal(t, 1, w.meta.count(), "a refusal is not retried blindly")

	// The case stays available and operational.
	code, _ := w.call(t, w.app.GetOccurrence, w.logPorto.ID, id, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	code, _ = w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, id, map[string]any{}, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	code, _ = w.call(t, w.app.ChangeOccurrenceStage, w.logPorto.ID, id, map[string]any{"stage_id": w.stageOf(t, models.GeneralStateInProgress).ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
}

func TestNotifier_RateLimitIsRetriedWithBackoffThenAccepted(t *testing.T) {
	w := newNotifWorld(t)
	w.meta.setMode("429")
	id := w.forward(t, nil)
	w.run()
	n := w.intent(t, id)
	require.Equal(t, models.NotificationPending, n.Status)
	require.NotNil(t, n.NextAttemptAt)
	assert.True(t, n.NextAttemptAt.After(time.Now().Add(30*time.Second)), "backoff, not an immediate hammer")
	assert.Equal(t, 0, w.run(), "not due yet")

	w.meta.setMode("ok")
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).Update("next_attempt_at", time.Now().Add(-time.Second)).Error)
	w.run()
	n = w.intent(t, id)
	assert.Equal(t, models.NotificationAccepted, n.Status)
	as := w.attempts(t, n.ID)
	require.Len(t, as, 2)
	assert.Equal(t, "failed", as[0].Outcome)
	assert.Equal(t, "accepted", as[1].Outcome)
}

func TestNotifier_TwoWorkersNeverSendTheSameIntentTwice(t *testing.T) {
	w := newNotifWorld(t)
	id := w.forward(t, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := w.run()
			mu.Lock()
			total += h
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, total, "exactly one worker claimed it")
	assert.Equal(t, 1, w.meta.count())
	assert.Len(t, w.attempts(t, w.intent(t, id).ID), 1)
}

func TestNotifier_AmbiguousResultsAreNeverResentBlindly(t *testing.T) {
	for _, mode := range []string{"drop", "500"} {
		t.Run(mode, func(t *testing.T) {
			w := newNotifWorld(t)
			w.meta.setMode(mode)
			id := w.forward(t, nil)
			w.run()
			n := w.intent(t, id)
			require.Equal(t, models.NotificationUncertain, n.Status, "the provider may have accepted it")
			require.Equal(t, 1, w.meta.count())

			// Not even a healthy provider later triggers an automatic repeat.
			w.meta.setMode("ok")
			assert.Equal(t, 0, w.run())
			assert.Equal(t, 1, w.meta.count())

			// A person must choose, and accept the duplicate risk explicitly.
			resolve := func(user uuid.UUID, body map[string]any) int {
				c, _ := w.call(t, w.app.ResolveOccurrenceNotification, user, id, body, map[string]any{"nid": n.ID.String()})
				return c
			}
			assert.Equal(t, fasthttp.StatusForbidden, resolve(w.logOutra.ID, map[string]any{"action": "retry"}), "another store")
			require.Equal(t, fasthttp.StatusOK, func() int {
				c, _ := w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, id, map[string]any{}, nil)
				return c
			}())
			assert.Equal(t, fasthttp.StatusForbidden, resolve(w.logPorto.ID, map[string]any{"action": "retry", "confirm_duplicate_risk": true}), "the assignee is not enough for an unknown result")
			assert.Equal(t, fasthttp.StatusConflict, resolve(w.supPorto.ID, map[string]any{"action": "retry"}), "no acknowledgement of the risk")
			assert.Equal(t, 1, w.meta.count())
			require.Equal(t, fasthttp.StatusOK, resolve(w.supPorto.ID, map[string]any{"action": "retry", "confirm_duplicate_risk": true}))
			w.run()
			assert.Equal(t, 2, w.meta.count())
			assert.Equal(t, models.NotificationAccepted, w.intent(t, id).Status)
			as := w.attempts(t, n.ID)
			require.Len(t, as, 2)
			assert.Equal(t, []int{1, 2}, []int{as[0].Number, as[1].Number}, "numbering survives a manual retry")
			require.NotNil(t, as[1].TriggeredByID, "who asked is on the attempt")
			assert.Equal(t, w.supPorto.ID, *as[1].TriggeredByID)
		})
	}
}

func TestNotifier_AWorkerThatDiesMidSendLeavesItUncertain(t *testing.T) {
	w := newNotifWorld(t)
	id := w.forward(t, nil)
	n := w.intent(t, id)
	old := time.Now().Add(-time.Hour)
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).
		Updates(map[string]any{"status": models.NotificationProcessing, "locked_at": old, "locked_by": "dead-worker", "attempts": 1}).Error)
	require.NoError(t, w.app.DB.Create(&models.OccurrenceNotificationAttempt{OrganizationID: w.org.ID, NotificationID: n.ID, Number: 1, StartedAt: old}).Error)

	assert.EqualValues(t, 1, w.app.RecoverStuckNotifications(time.Now()))
	assert.Equal(t, models.NotificationUncertain, w.intent(t, id).Status)
	assert.Equal(t, 0, w.run())
	assert.Zero(t, w.meta.count(), "recovery never resends")
	assert.Equal(t, "uncertain", w.attempts(t, n.ID)[0].Outcome)
}

func TestNotifier_SendTimeRulesStopUnsafeSends(t *testing.T) {
	t.Run("automation switched off after the intent", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, nil)
		code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"is_active": false}))
		require.Equal(t, fasthttp.StatusOK, code)
		w.run()
		assert.Equal(t, models.NotificationCancelled, w.intent(t, id).Status)
		assert.Zero(t, w.meta.count())
	})
	t.Run("template no longer approved", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, nil)
		require.NoError(t, w.app.DB.Model(&models.Template{}).Where("organization_id = ? AND name = ?", w.org.ID, "recebimento_logistica").Update("status", "REJECTED").Error)
		w.run()
		n := w.intent(t, id)
		assert.Equal(t, models.NotificationFailed, n.Status)
		assert.Equal(t, "template_not_approved", n.Reason)
		assert.Zero(t, w.meta.count())
	})
	t.Run("category is not assumed to be utility", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, nil)
		require.NoError(t, w.app.DB.Model(&models.Template{}).Where("organization_id = ? AND name = ?", w.org.ID, "recebimento_logistica").Update("category", "MARKETING").Error)
		w.run()
		assert.Equal(t, "template_category_not_allowed", w.intent(t, id).Reason)
		assert.Zero(t, w.meta.count())
	})
	t.Run("account removed from the organization", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, nil)
		require.NoError(t, w.app.DB.Where("id = ?", w.account.ID).Delete(&models.WhatsAppAccount{}).Error)
		w.run()
		assert.Equal(t, "account_not_found_in_organization", w.intent(t, id).Reason)
		assert.Zero(t, w.meta.count(), "no other account is used instead")
	})
	t.Run("configuration changed during the wait keeps the snapshot", func(t *testing.T) {
		w := newNotifWorld(t)
		_ = w.forward(t, nil)
		other := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
		w.template(t, other.Name, "recebimento_logistica", "pt_BR", "UTILITY", "APPROVED", "Olá {{1}} {{2}} {{3}}")
		code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"whatsapp_account": other.Name, "fallback_template_name": ""}))
		require.Equal(t, fasthttp.StatusOK, code)
		w.run()
		assert.Contains(t, w.meta.last().Path, w.account.PhoneID, "the account of the moment the case was forwarded")
	})
}

func TestNotifier_MissingDataNeverSendsInventedValues(t *testing.T) {
	t.Run("no order number uses the fallback template", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, map[string]any{"order_ref": ""})
		w.run()
		req := w.meta.last()
		assert.Equal(t, "recebimento_sem_pedido", req.Template)
		require.Len(t, req.Params, 2)
		assert.NotEmpty(t, req.Params[0])
		assert.Equal(t, models.NotificationAccepted, w.intent(t, id).Status)
	})
	t.Run("no order number and no fallback is skipped", func(t *testing.T) {
		w := newNotifWorld(t)
		code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"fallback_template_name": ""}))
		require.Equal(t, fasthttp.StatusOK, code)
		id := w.forward(t, map[string]any{"order_ref": ""})
		w.run()
		assert.Equal(t, models.NotificationSkipped, w.intent(t, id).Status)
		assert.Equal(t, "missing_order_number", w.intent(t, id).Reason)
		assert.Zero(t, w.meta.count())
	})
	t.Run("customer without a name is skipped", func(t *testing.T) {
		w := newNotifWorld(t)
		id := w.forward(t, nil)
		var occ models.Occurrence
		require.NoError(t, w.app.DB.First(&occ, id).Error)
		require.NoError(t, w.app.DB.Model(&models.Contact{}).Where("id = ?", occ.ContactID).Update("profile_name", "").Error)
		w.run()
		assert.Equal(t, "missing_customer_name", w.intent(t, id).Reason)
		assert.Zero(t, w.meta.count())
	})
}

func TestNotifier_FailedFollowsThePolicyAndCanBeRetriedOrCancelled(t *testing.T) {
	w := newNotifWorld(t)
	code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"on_failure": "cancel"}))
	require.Equal(t, fasthttp.StatusOK, code)
	w.meta.setMode("400")
	id := w.forward(t, nil)
	w.run()
	assert.Equal(t, models.NotificationCancelled, w.intent(t, id).Status, "policy: cancel instead of waiting for a person")

	// Manual-retry policy: it stays failed and a person retries it.
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"on_failure": "manual_retry"}))
	require.Equal(t, fasthttp.StatusOK, code)
	id2 := w.forward(t, nil)
	w.run()
	n := w.intent(t, id2)
	require.Equal(t, models.NotificationFailed, n.Status)

	resolve := func(user uuid.UUID, action string) int {
		c, _ := w.call(t, w.app.ResolveOccurrenceNotification, user, id2, map[string]any{"action": action}, map[string]any{"nid": n.ID.String()})
		return c
	}
	assert.Equal(t, fasthttp.StatusForbidden, resolve(w.sac.ID, "retry"), "the SAC follows the case but does not drive its communication")
	w.meta.setMode("ok")
	require.Equal(t, fasthttp.StatusOK, resolve(w.supPorto.ID, "retry"))
	w.run()
	assert.Equal(t, models.NotificationAccepted, w.intent(t, id2).Status)
	assert.Equal(t, fasthttp.StatusConflict, resolve(w.supPorto.ID, "retry"), "an accepted message cannot be retried")
	assert.Equal(t, fasthttp.StatusConflict, resolve(w.supPorto.ID, "cancel"))

	// Listing exposes state, attempts and what the caller may do.
	code2, body := w.call(t, w.app.ListOccurrenceNotifications, w.supPorto.ID, id2, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code2)
	assert.True(t, strings.Contains(string(body), "attempt_history") && strings.Contains(string(body), "wamid."+w.meta.pfx))
	code2, _ = w.call(t, w.app.ListOccurrenceNotifications, w.logOutra.ID, id2, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code2, "another store cannot even read it")
}

func TestNotifier_InactiveAutomationIsPreparedAndCanBeActivatedLater(t *testing.T) {
	w := newNotifWorld(t)
	code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"is_active": false}))
	require.Equal(t, fasthttp.StatusOK, code)
	id := w.forward(t, nil)
	n := w.intent(t, id)
	require.Equal(t, models.NotificationPrepared, n.Status)
	w.run()
	assert.Zero(t, w.meta.count())

	code, _ = w.call(t, w.app.ResolveOccurrenceNotification, w.supPorto.ID, id, map[string]any{"action": "retry"}, map[string]any{"nid": n.ID.String()})
	assert.Equal(t, fasthttp.StatusConflict, code, "still inactive: no way to send")
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"is_active": true}))
	require.Equal(t, fasthttp.StatusOK, code)
	code, _ = w.call(t, w.app.ResolveOccurrenceNotification, w.supPorto.ID, id, map[string]any{"action": "retry"}, map[string]any{"nid": n.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code)
	w.run()
	assert.Equal(t, models.NotificationAccepted, w.intent(t, id).Status)
}
