package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests do not pretend Meta is idempotent: what they prove is that the
// database claim keeps two workers from legitimately starting the same send, and
// that an outcome nobody confirmed is recorded as uncertain and never resent.

// gateMeta is a Meta mock whose handler can stall, so a test can look at (or race
// against) the world while a request is in flight.
type gateMeta struct {
	*httptest.Server
	calls   atomic.Int32
	arrived chan struct{} // signalled when a request reaches the server (if non-nil)
	release chan struct{} // the handler waits for this (if non-nil)
	delay   time.Duration
	status  int
	body    string
	once    sync.Once
}

// Unblock lets a stalled handler go; safe to call more than once, and registered as a
// cleanup so a failing test can never leave the server waiting (httptest's Close waits
// for in-flight handlers).
func (g *gateMeta) Unblock() {
	g.once.Do(func() {
		if g.release != nil {
			close(g.release)
		}
	})
}

func newGateMeta(status int, body string) *gateMeta {
	g := &gateMeta{status: status, body: body}
	g.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		if g.arrived != nil {
			select {
			case g.arrived <- struct{}{}:
			default:
			}
		}
		if g.release != nil {
			<-g.release
		}
		if g.delay > 0 {
			time.Sleep(g.delay)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(g.status)
		_, _ = rw.Write([]byte(g.body))
	}))
	return g
}

const claimOKBody = `{"messages":[{"id":"wamid.claim"}]}`

// claimEnv is an authentication campaign with one recipient holding a valid sealed code.
type claimEnv struct {
	w    *Worker
	org  *models.Organization
	c    *models.BulkMessageCampaign
	r    *models.BulkMessageRecipient
	meta *gateMeta
}

func newClaimEnv(t *testing.T, meta *gateMeta) *claimEnv {
	t.Helper()
	now := time.Now()
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, now)
	w.Config.OTP = config.OTPConfig{}
	until := now.Add(10 * time.Minute)
	reseal(t, w, recipient, now, &until)
	t.Cleanup(meta.Close)
	t.Cleanup(meta.Unblock) // runs first (LIFO)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)
	return &claimEnv{w, org, campaign, recipient, meta}
}

func (e *claimEnv) job() *queue.RecipientJob { return jobFor(e.org, e.c, e.r) }

func (e *claimEnv) rec(t *testing.T) models.BulkMessageRecipient {
	t.Helper()
	var r models.BulkMessageRecipient
	require.NoError(t, e.w.DB.Where("id = ?", e.r.ID).First(&r).Error)
	return r
}

func (e *claimEnv) messages(t *testing.T) []models.Message {
	t.Helper()
	var ms []models.Message
	require.NoError(t, e.w.DB.Where("metadata->>'campaign_id' = ?", e.c.ID.String()).Find(&ms).Error)
	return ms
}

// crashedClaim leaves the database exactly as a worker that died after claiming
// the recipient would: a pending masked message, the recipient pointing at it.
func (e *claimEnv) crashedClaim(t *testing.T, age time.Duration) uuid.UUID {
	t.Helper()
	contact, _, err := contactutil.GetOrCreateContact(e.w.DB, e.org.ID, e.r.PhoneNumber, e.r.RecipientName)
	require.NoError(t, err)
	msg := e.w.newSecretMessage(e.job(), e.c, contact)
	require.NoError(t, e.w.DB.Create(msg).Error)
	require.NoError(t, e.w.DB.Exec("UPDATE messages SET created_at = ? WHERE id = ?", time.Now().Add(-age), msg.ID).Error)
	require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", e.r.ID).UpdateColumn("message_id", msg.ID).Error)
	return msg.ID
}

func TestClaim_TwoWorkersRacingForTheSameRecipientSendOnce(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.delay = 400 * time.Millisecond // the winner is still inside Meta when the other arrives
	e := newClaimEnv(t, meta)
	w2 := &Worker{DB: e.w.DB, Log: e.w.Log, WhatsApp: e.w.WhatsApp, Config: e.w.Config, Redis: e.w.Redis, Publisher: e.w.Publisher}

	var wg sync.WaitGroup
	for _, w := range []*Worker{e.w, w2} {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			require.NoError(t, w.HandleRecipientJob(context.Background(), e.job()))
		}(w)
	}
	wg.Wait()

	assert.EqualValues(t, 1, meta.calls.Load(), "only the claim winner calls Meta")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusSent, got.Status)
	require.NotNil(t, got.MessageID)
	msgs := e.messages(t)
	require.Len(t, msgs, 1, "the loser's message was rolled back: no duplicate messages")
	assert.Equal(t, *got.MessageID, msgs[0].ID)
	assert.Equal(t, models.MessageStatusSent, msgs[0].Status)
	assert.Equal(t, "wamid.claim", msgs[0].WhatsAppMessageID)
	assert.Equal(t, "wamid.claim", got.WhatsAppMessageID)
	assert.NotContains(t, rawRow(t, e.w, "messages", "content", msgs[0].ID.String()), otpCode)
	assert.Empty(t, got.TemplateParams, "the code is dropped once sent")
}

func TestClaim_DuplicateJobsInRedisSendOnce(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	if e.w.Redis == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e.w.Redis.Del(ctx, queue.StreamName)
	defer e.w.Redis.Del(context.Background(), queue.StreamName)

	q := queue.NewRedisQueue(e.w.Redis, e.w.Log)
	require.NoError(t, q.EnqueueRecipient(ctx, e.job()))
	require.NoError(t, q.EnqueueRecipient(ctx, e.job())) // the same job twice
	consumer, err := queue.NewRedisConsumer(e.w.Redis, e.w.Log)
	require.NoError(t, err)
	runCtx, stop := context.WithCancel(ctx)
	go func() { _ = consumer.Consume(runCtx, e.w) }()

	require.Eventually(t, func() bool {
		n, _ := e.w.Redis.XLen(ctx, queue.StreamName).Result()
		return n == 0
	}, 10*time.Second, 50*time.Millisecond, "both entries processed, acknowledged and deleted")
	stop()

	assert.EqualValues(t, 1, meta.calls.Load(), "the duplicate job did not send again")
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
	assert.Len(t, e.messages(t), 1)
}

func TestClaim_CrashBeforeTheMetaCallIsUncertainAndNeverResent(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	msgID := e.crashedClaim(t, 10*time.Minute) // the worker died long ago

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job())) // redelivery

	assert.Zero(t, meta.calls.Load(), "an unresolved claim is never retried automatically")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	require.NotNil(t, got.MessageID, "the claim is kept: that is what blocks a retry")
	assert.Equal(t, msgID, *got.MessageID)
	assert.True(t, templateutil.IsSealed(got.TemplateParams), "the sealed code is preserved")
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)

	// And it stays that way however often the job comes back.
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	assert.Zero(t, meta.calls.Load())
	assert.Len(t, e.messages(t), 1)
}

func TestClaim_CrashAfterMetaAcceptedBeforeTheResultWasRecorded(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	require.EqualValues(t, 1, meta.calls.Load())

	// Rewind the database to what a crash between Meta's answer and the write leaves.
	require.NoError(t, e.w.DB.Exec("UPDATE bulk_message_recipients SET status='pending', whats_app_message_id='', sent_at=NULL WHERE id = ?", e.r.ID).Error)
	require.NoError(t, e.w.DB.Exec("UPDATE messages SET status='pending', whats_app_message_id='', created_at = ? WHERE metadata->>'campaign_id' = ?",
		time.Now().Add(-10*time.Minute), e.c.ID.String()).Error)
	until := time.Now().Add(10 * time.Minute)
	reseal(t, e.w, e.r, time.Now(), &until) // the code had not been dropped yet
	e.r.TemplateParams = e.rec(t).TemplateParams

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job())) // Redis delivers it again

	assert.EqualValues(t, 1, meta.calls.Load(), "Meta already accepted it once: no second send")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.NotNil(t, got.MessageID)
}

func TestClaim_AClaimStillInFlightIsLeftAlone(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	e.crashedClaim(t, 5*time.Second) // another worker claimed it moments ago

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	assert.Zero(t, meta.calls.Load())
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status, "not declared uncertain while it may still be running")
	assert.NotNil(t, got.MessageID)
}

func TestClaim_TimeoutAndServerErrorsAreUncertain_RefusalsAreNot(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		meta := newGateMeta(200, claimOKBody)
		meta.delay = 1500 * time.Millisecond
		e := newClaimEnv(t, meta)
		e.w.WhatsApp.HTTPClient.Timeout = 200 * time.Millisecond

		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

		assert.EqualValues(t, 1, meta.calls.Load(), "the request did reach Meta")
		got := e.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status)
		assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		assert.NotNil(t, got.MessageID, "the claim stays: no automatic retry")
		assert.True(t, templateutil.IsSealed(got.TemplateParams))
		assert.NotContains(t, got.ErrorMessage, otpCode)
	})
	t.Run("5xx", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(502, `{"error":{"code":1,"message":"temporarily unavailable"}}`))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		assert.NotNil(t, got.MessageID)
	})
	t.Run("200 with an unreadable answer", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(200, `not json`))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		assert.Contains(t, e.rec(t).ErrorMessage, ReasonDeliveryUncertain, "Meta may have accepted it")
	})
	t.Run("4xx refusal is definitive", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(400, `{"error":{"code":131008,"message":"Required parameter is missing"}}`))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status)
		assert.NotContains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		assert.Nil(t, got.MessageID, "provably not sent: the claim is released so a retry is possible")
		assert.True(t, templateutil.IsSealed(got.TemplateParams), "the code is kept for the retry")
	})
	t.Run("connection never established is definitive", func(t *testing.T) {
		meta := newGateMeta(200, claimOKBody)
		e := newClaimEnv(t, meta)
		e.w.WhatsApp = whatsapp.NewWithBaseURL(e.w.Log, "http://127.0.0.1:1")
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.NotContains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		assert.Nil(t, got.MessageID)
	})
}

func TestClaim_ADefinitiveFailureCanBeRetriedWithANewMessage(t *testing.T) {
	meta := newGateMeta(400, `{"error":{"code":100,"message":"bad"}}`)
	e := newClaimEnv(t, meta)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	failed := e.rec(t)
	require.Nil(t, failed.MessageID)

	meta.status, meta.body = 200, claimOKBody // RetryFailed: back to pending, same stored envelope
	require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", e.r.ID).Update("status", models.MessageStatusPending).Error)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), jobFor(e.org, e.c, &failed)))

	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
	assert.EqualValues(t, 2, meta.calls.Load())
	assert.Len(t, e.messages(t), 2, "one failed attempt and one sent, each with its own message")
}

func TestClaim_KeyProblemsClaimNothingAndCreateNoMessage(t *testing.T) {
	e := newClaimEnv(t, newGateMeta(200, claimOKBody))
	e.w.Config.App.EncryptionKey = "another-encryption-key-another-encryption-key"

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	got := e.rec(t)
	assert.Contains(t, got.ErrorMessage, "otp_unavailable")
	assert.Nil(t, got.MessageID, "nothing was claimed")
	assert.Empty(t, e.messages(t), "no message for an attempt that never started")
	assert.True(t, templateutil.IsSealed(got.TemplateParams))

	e.w.Config.App.EncryptionKey = encKey // restored: RetryFailed -> pending -> sent
	require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", e.r.ID).Update("status", models.MessageStatusPending).Error)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), jobFor(e.org, e.c, &got)))
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
}

func TestClaim_CodeExpiringWhileWaitingIsNotSent(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	t0 := time.Now()
	until := t0.Add(2 * time.Minute)
	reseal(t, e.w, e.r, t0, &until)
	e.r.TemplateParams = e.rec(t).TemplateParams

	// Valid when the job is picked up (open + first check) and expired by the time
	// the claim is held, right before Meta would be called.
	var calls int32
	e.w.Now = func() time.Time {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return t0.Add(time.Minute)
		}
		return t0.Add(5 * time.Minute)
	}

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	assert.Zero(t, meta.calls.Load(), "never after valid_until")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, templateutil.ReasonOTPExpired)
	assert.Nil(t, got.MessageID, "released: it provably was not sent")
	assert.Empty(t, got.TemplateParams, "the expired secret is dropped")
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
}

// While a send is in flight the world already shows a pending message; it must
// not carry the code anywhere.
func TestClaim_PendingMessageNeverCarriesTheCode(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.w.HandleRecipientJob(context.Background(), e.job())
	}()
	select {
	case <-meta.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached Meta")
	}

	// The request is in flight, the claim is held and no transaction is open.
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status)
	require.NotNil(t, got.MessageID)
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusPending, msgs[0].Status)
	for _, col := range []string{"content", "template_params", "metadata", "interactive_data", "error_message"} {
		assert.NotContains(t, rawRow(t, e.w, "messages", col, msgs[0].ID.String()), otpCode, "messages."+col)
	}
	assert.NotContains(t, rawRow(t, e.w, "bulk_message_recipients", "template_params", e.r.ID.String()), otpCode)
	// A second worker meets the live claim and leaves it alone, without blocking on a lock.
	w2 := &Worker{DB: e.w.DB, Log: e.w.Log, WhatsApp: e.w.WhatsApp, Config: e.w.Config, Redis: e.w.Redis, Publisher: e.w.Publisher}
	require.NoError(t, w2.HandleRecipientJob(context.Background(), e.job()))
	assert.EqualValues(t, 1, meta.calls.Load())

	meta.Unblock()
	<-done
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
}

// Ordinary campaigns keep the old flow: no claim, the message is written after the send.
func TestClaim_NormalCampaignsAreUntouched(t *testing.T) {
	w := testWorker(t)
	org, account, _, campaign, recipient := createTestCampaignData(t, w)
	w.Config = &config.Config{App: config.AppConfig{EncryptionKey: encKey}}
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	meta := newGateMeta(200, claimOKBody)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	var got models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&got, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, got.Status)
	assert.Nil(t, got.MessageID, "normal campaigns never use the claim column")
	assert.Equal(t, "John", got.TemplateParams["1"], "params stay as before")
	var n int64
	require.NoError(t, w.DB.Model(&models.Message{}).Where("metadata->>'campaign_id' = ?", campaign.ID.String()).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}
