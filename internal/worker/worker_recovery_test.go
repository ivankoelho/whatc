package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claims that are stuck are found and resolved in PostgreSQL, with no job coming
// back from Redis; a live send is never overridden; and a late result from a worker
// that was cut off can never overwrite what reconciliation decided.

func (e *claimEnv) campaignCounts(t *testing.T) (sent, failed int) {
	t.Helper()
	var c models.BulkMessageCampaign
	require.NoError(t, e.w.DB.First(&c, e.c.ID).Error)
	return c.SentCount, c.FailedCount
}

func (e *claimEnv) clone(stale time.Duration) *Worker {
	return &Worker{DB: e.w.DB, Log: e.w.Log, WhatsApp: e.w.WhatsApp, Config: e.w.Config, Redis: e.w.Redis, Publisher: e.w.Publisher, ClaimStale: stale}
}

func TestRecovery_StuckClaimIsResolvedFromPostgresWithoutRedisOrAJob(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	msgID := e.crashedClaim(t, 10*time.Minute) // the worker died; nothing will ever redeliver a job

	// It can be seen straight from the database...
	list, err := authmask.ListUncertain(e.w.DB, time.Now(), DefaultClaimStale())
	require.NoError(t, err)
	var seen bool
	for _, u := range list {
		if u.RecipientID == e.r.ID {
			seen = true
			assert.Equal(t, authmask.StateStuck, u.State)
		}
	}
	require.True(t, seen)

	// ...and resolved by housekeeping, atomically.
	n, err := e.w.ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	require.NotNil(t, got.MessageID, "still blocked: the claim is kept")
	assert.Equal(t, msgID, *got.MessageID)
	assert.True(t, templateutil.IsSealed(got.TemplateParams))
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
	_, failed := e.campaignCounts(t)
	assert.Equal(t, 1, failed)
	assert.Zero(t, meta.calls.Load(), "recovery never sends")

	n, err = e.w.ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "idempotent")
	_, failed = e.campaignCounts(t)
	assert.Equal(t, 1, failed, "and counts the failure once")
}

func TestRecovery_ALiveWorkerIsNeverOverridden(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.w.HandleRecipientJob(context.Background(), e.job())
	}()
	<-meta.arrived // the request is inside Meta

	n, err := e.clone(0).ResolveStaleClaims(context.Background()) // default, derived limit
	require.NoError(t, err)
	assert.Zero(t, n, "a claim younger than the limit is left alone")
	assert.Equal(t, models.MessageStatusPending, e.rec(t).Status)

	meta.Unblock()
	<-done
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
	sent, failed := e.campaignCounts(t)
	assert.Equal(t, 1, sent)
	assert.Zero(t, failed)
}

// The worker is cut off: reconciliation resolves its claim, then Meta's answer
// finally arrives. It must not overwrite anything.
func TestRecovery_ALateResultCannotOverwriteAReconciledClaim(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.w.HandleRecipientJob(context.Background(), e.job())
	}()
	<-meta.arrived
	time.Sleep(30 * time.Millisecond)

	n, err := e.clone(time.Millisecond).ResolveStaleClaims(context.Background()) // an (over-eager) recovery
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1, "this recipient's claim at least (the shared test database may hold other tests' claims too)")

	meta.Unblock() // Meta's answer arrives late
	<-done

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status, "the reconciled state stands")
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.NotNil(t, got.MessageID, "and the recipient stays blocked")
	assert.Empty(t, got.WhatsAppMessageID, "the late wamid was not written over it")
	assert.Equal(t, models.MessageStatusFailed, e.messages(t)[0].Status)
	sent, _ := e.campaignCounts(t)
	assert.Zero(t, sent)
	assert.EqualValues(t, 1, meta.calls.Load(), "and nothing was sent again")
}

// Same, after a person released the claim and a retry is waiting: the late answer
// of the old attempt must not touch the recipient either.
func TestRecovery_ALateResultCannotOverwriteAHumanRelease(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.w.HandleRecipientJob(context.Background(), e.job())
	}()
	<-meta.arrived
	time.Sleep(30 * time.Millisecond)
	_, err := e.clone(time.Millisecond).ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	// the operator checked and released it
	require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", e.r.ID).UpdateColumn("message_id", nil).Error)

	meta.Unblock()
	<-done

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Nil(t, got.MessageID)
	assert.Empty(t, got.WhatsAppMessageID)
}

// Whoever wins, the recipient and its message always agree.
func TestRecovery_ConcurrentFinalizationIsAlwaysConsistent(t *testing.T) {
	for i := 0; i < 8; i++ {
		meta := newGateMeta(200, claimOKBody)
		meta.delay = 15 * time.Millisecond
		e := newClaimEnv(t, meta)
		resolver := e.clone(time.Millisecond)

		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = e.w.HandleRecipientJob(context.Background(), e.job())
			close(stop)
		}()
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					_, _ = resolver.ResolveStaleClaims(context.Background())
					return
				default:
					_, _ = resolver.ResolveStaleClaims(context.Background())
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
		wg.Wait()

		got := e.rec(t)
		msgs := e.messages(t)
		require.Len(t, msgs, 1)
		switch got.Status {
		case models.MessageStatusSent:
			assert.Equal(t, models.MessageStatusSent, msgs[0].Status, "round %d", i)
			assert.NotEmpty(t, got.WhatsAppMessageID)
		case models.MessageStatusFailed:
			assert.Equal(t, models.MessageStatusFailed, msgs[0].Status, "round %d", i)
			assert.NotNil(t, got.MessageID, "round %d: failed here means uncertain, still blocked", i)
			assert.Empty(t, got.WhatsAppMessageID)
		default:
			t.Fatalf("round %d: unexpected recipient status %s", i, got.Status)
		}
		assert.EqualValues(t, 1, meta.calls.Load(), "round %d: one send, whoever won", i)
	}
}

// The limit comes from the Meta call's own timeout, not from a guess.
func TestRecovery_TheLimitIsDerivedFromTheMetaTimeout(t *testing.T) {
	w := &Worker{WhatsApp: whatsapp.New(testutil.NopLogger())}
	assert.Equal(t, 30*time.Second, w.metaCallTimeout(), "the client's 30s timeout")
	assert.Equal(t, 2*(30*time.Second+claimWriteBudget), w.claimStale(), "twice (the call + the writes around it)")
	assert.Equal(t, DefaultClaimStale(), w.claimStale())

	w.WhatsApp.HTTPClient.Timeout = 5 * time.Second
	assert.Equal(t, 5*time.Second, w.metaCallTimeout())
	assert.Equal(t, 2*(5*time.Second+claimWriteBudget), w.claimStale(), "it follows a shorter timeout")

	w.WhatsApp.HTTPClient.Timeout = 0 // no timeout configured: the worker still bounds the call
	assert.Equal(t, 30*time.Second, w.metaCallTimeout())
	w.WhatsApp.HTTPClient.Timeout = 5 * time.Minute // a longer one is capped too
	assert.Equal(t, 30*time.Second, w.metaCallTimeout())

	w.ClaimStale = 7 * time.Second
	assert.Equal(t, 7*time.Second, w.claimStale(), "tests may override it")
}

// What the worker does with each kind of answer, end to end.
func TestRecovery_AnswersAreHandledByWhatTheyProve(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		uncertain bool
		prefix    string
	}{
		{"documented refusal", 400, `{"error":{"code":131008,"message":"missing"}}`, false, "meta_refused"},
		{"throttled", 429, `{"error":{"code":130429,"message":"rate"}}`, false, "meta_transient"},
		{"bare 429 proves nothing", 429, `slow down`, true, ReasonDeliveryUncertain},
		{"408 request timeout", 408, `{"error":{"code":100,"message":"x"}}`, true, ReasonDeliveryUncertain},
		{"undocumented 4xx code", 400, `{"error":{"code":999999,"message":"new"}}`, true, ReasonDeliveryUncertain},
		{"server error", 500, `{"error":{"code":131000,"message":"boom"}}`, true, ReasonDeliveryUncertain},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			meta := newGateMeta(c.status, c.body)
			e := newClaimEnv(t, meta)

			require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
			got := e.rec(t)
			assert.Equal(t, models.MessageStatusFailed, got.Status)
			assert.Contains(t, got.ErrorMessage, c.prefix)
			assert.True(t, templateutil.IsSealed(got.TemplateParams), "the code is kept either way")

			if c.uncertain {
				assert.NotNil(t, got.MessageID, "blocked")
				// However the job comes back, it is never sent again.
				require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
				require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
				assert.EqualValues(t, 1, meta.calls.Load(), "an unknown result is never resent automatically")
			} else {
				assert.Nil(t, got.MessageID, "provably not sent: retryable by retry-failed")
				// A redelivered job of a failed recipient does not send on its own either.
				require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
				assert.EqualValues(t, 1, meta.calls.Load(), "only an explicit retry (which makes it pending) sends again")
			}
		})
	}
}
