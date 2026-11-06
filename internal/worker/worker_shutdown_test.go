package worker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---- B1: markUncertain is atomic ----

// failMessageUpdates makes every UPDATE that sets a message of the campaign to 'failed'
// raise an error, until the returned function is called.
func (e *claimEnv) failMessageUpdates(t *testing.T) (restore func()) {
	t.Helper()
	fn := "fn_" + strings.ReplaceAll(e.c.ID.String()[:8], "-", "")
	require.NoError(t, e.w.DB.Exec(`CREATE FUNCTION `+fn+`() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'messages are not writable'; END $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, e.w.DB.Exec(`CREATE TRIGGER `+fn+` BEFORE UPDATE ON messages FOR EACH ROW
		WHEN (NEW.status = 'failed' AND NEW.metadata->>'campaign_id' = '`+e.c.ID.String()+`') EXECUTE FUNCTION `+fn+`()`).Error)
	done := false
	restore = func() {
		if done {
			return
		}
		done = true
		_ = e.w.DB.Exec(`DROP TRIGGER IF EXISTS ` + fn + ` ON messages`).Error
		_ = e.w.DB.Exec(`DROP FUNCTION IF EXISTS ` + fn + `()`).Error
	}
	t.Cleanup(restore)
	return restore
}

func TestB1_AFailedSecondWriteRollsBackTheRecipientToo(t *testing.T) {
	meta := newGateMeta(500, `{"error":{"code":1,"message":"boom"}}`) // unknown outcome -> markUncertain
	e := newClaimEnv(t, meta)
	restore := e.failMessageUpdates(t)

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	// Neither write happened: no contradiction, the recipient is still just claimed.
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status, "the recipient update was rolled back with the message update")
	assert.NotNil(t, got.MessageID)
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusPending, msgs[0].Status)
	sent, failed := e.campaignCounts(t)
	assert.Zero(t, sent)
	assert.Zero(t, failed, "counters run only after a commit")

	// Housekeeping then resolves it once the claim is old enough, and nothing is lost.
	restore()
	n, err := e.clone(time.Millisecond).ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	got = e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.Equal(t, models.MessageStatusFailed, e.messages(t)[0].Status)
	_, failed = e.campaignCounts(t)
	assert.Equal(t, 1, failed)
}

// markUncertain (the sending worker) and the recovery routine race for the same claim:
// exactly one wins, the counters move once and recipient and message agree.
func TestB1_MarkUncertainAndRecoveryRacingCountTheFailureOnce(t *testing.T) {
	for i := 0; i < 8; i++ {
		e := newClaimEnv(t, newGateMeta(200, claimOKBody))
		msgID := e.crashedClaim(t, 10*time.Minute)
		var msg models.Message
		require.NoError(t, e.w.DB.First(&msg, msgID).Error)

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			e.w.markUncertain(context.Background(), e.job(), &msg, ReasonDeliveryUncertain+": racing")
		}()
		go func() {
			defer wg.Done()
			<-start
			_, _ = e.clone(time.Millisecond).ResolveStaleClaims(context.Background())
		}()
		close(start)
		wg.Wait()

		got := e.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status, "round %d", i)
		assert.NotNil(t, got.MessageID)
		assert.Equal(t, models.MessageStatusFailed, e.messages(t)[0].Status, "round %d", i)
		_, failed := e.campaignCounts(t)
		assert.Equal(t, 1, failed, "round %d: counted once", i)
	}
}

// ---- B2: the database clock decides ----

// skewed returns a copy of the worker whose machine clock is off by d: its Go clock
// (used for created_at by default, and for the validity check) is shifted.
func (e *claimEnv) skewed(d time.Duration) *Worker {
	w := e.clone(0)
	w.DB = e.w.DB.Session(&gorm.Session{NowFunc: func() time.Time { return time.Now().Add(d) }})
	w.Now = func() time.Time { return time.Now().Add(d) }
	return w
}

func TestB2_ALiveClaimIsNeverMistakenForStuckBecauseOfClockSkew(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	// The sending worker's machine runs ONE HOUR BEHIND: its own clock would stamp the
	// claim an hour in the past, which a resolver on a correct clock would call stuck.
	sender := e.skewed(-time.Hour)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sender.HandleRecipientJob(context.Background(), e.job())
	}()
	<-meta.arrived

	// The claim is stamped by the database clock, whatever the sender's machine says.
	var ageSecs float64
	require.NoError(t, e.w.DB.Raw(`SELECT extract(epoch FROM (clock_timestamp() - created_at)) FROM messages WHERE metadata->>'campaign_id' = ?`, e.c.ID.String()).Scan(&ageSecs).Error)
	assert.Less(t, ageSecs, 30.0, "the claim instant is the database's, not the skewed worker's")

	// Resolvers on correct, fast and slow clocks all see a live claim.
	for _, skew := range []time.Duration{0, time.Hour, -time.Hour} {
		n, err := e.skewed(skew).ResolveStaleClaims(context.Background())
		require.NoError(t, err)
		assert.Zero(t, n, "skew %v", skew)
	}
	assert.Equal(t, models.MessageStatusPending, e.rec(t).Status)

	meta.Unblock()
	<-done
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
}

func TestB2_ADeadClaimIsResolvedWhateverTheResolversClock(t *testing.T) {
	for _, skew := range []time.Duration{0, time.Hour, -time.Hour} {
		e := newClaimEnv(t, newGateMeta(200, claimOKBody))
		e.crashedClaim(t, 10*time.Minute) // ten minutes old by the database clock

		n, err := e.skewed(skew).ResolveStaleClaims(context.Background())
		require.NoError(t, err)
		assert.GreaterOrEqual(t, n, 1, "skew %v", skew)
		assert.Equal(t, models.MessageStatusFailed, e.rec(t).Status, "skew %v", skew)
	}
}

// ---- B3: graceful shutdown ----

func TestB3_NothingIsClaimedOnceShuttingDown(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the shutdown signal arrived before the job was handled
	err := e.w.HandleRecipientJob(ctx, e.job())

	assert.ErrorIs(t, err, errShuttingDown, "the job is not acknowledged: it will be delivered again")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status)
	assert.Nil(t, got.MessageID, "no claim")
	assert.Empty(t, e.messages(t))
	assert.Zero(t, meta.calls.Load())

	// The same through the worker's own flag (no context involved).
	e.w.BeginShutdown()
	assert.ErrorIs(t, e.w.HandleRecipientJob(context.Background(), e.job()), errShuttingDown)
	assert.Nil(t, e.rec(t).MessageID)
	assert.Zero(t, meta.calls.Load())
}

func TestB3_ShutdownAfterTheClaimButBeforeTheRequestUndoesTheClaimAndTheJobRecovers(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)

	ctx, cancel := context.WithCancel(context.Background())
	e.w.afterClaim = cancel // the signal lands right after the claim

	err := e.w.HandleRecipientJob(ctx, e.job())

	assert.ErrorIs(t, err, errShuttingDown)
	assert.Zero(t, meta.calls.Load(), "no new Meta call while shutting down")
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status, "back to pending: nothing left, so nothing is uncertain")
	assert.Nil(t, got.MessageID)
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
	assert.Contains(t, msgs[0].ErrorMessage, "worker_shutdown")
	assert.True(t, got.TemplateParams != nil && len(got.TemplateParams) > 0, "the sealed code is kept")

	// The job comes back (the new process): it simply sends.
	e.w.afterClaim = nil
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	assert.EqualValues(t, 1, meta.calls.Load())
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
}

func TestB3_ShutdownDuringTheCallLetsTheCallFinishAndRecordsTheResult(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.arrived = make(chan struct{}, 1)
	meta.release = make(chan struct{})
	e := newClaimEnv(t, meta)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.w.HandleRecipientJob(ctx, e.job()) }()
	<-meta.arrived // the request is inside Meta

	cancel() // the shutdown signal arrives mid-call
	time.Sleep(50 * time.Millisecond)
	meta.Unblock()
	require.NoError(t, <-done)

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusSent, got.Status, "the claim finished its own send: a clean stop makes nothing uncertain")
	assert.NotEmpty(t, got.WhatsAppMessageID)
	assert.EqualValues(t, 1, meta.calls.Load())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestB3_ShutdownRightAfterTheResponseStillRecordsIt(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	ctx, cancel := context.WithCancel(context.Background())

	base := e.w.WhatsApp.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	e.w.WhatsApp.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		cancel() // the signal arrives the moment Meta has answered
		return resp, err
	})

	require.NoError(t, e.w.HandleRecipientJob(ctx, e.job()))

	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
	assert.EqualValues(t, 1, meta.calls.Load())
}

// A call that cannot finish in time is not an answer: the claim stays and is resolved
// as uncertain, never resent.
func TestB3_ACallThatDoesNotFinishWithinItsTimeoutStaysUncertain(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	meta.delay = 1500 * time.Millisecond
	e := newClaimEnv(t, meta)
	e.w.WhatsApp.HTTPClient.Timeout = 150 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	// The cancellation is timed from the claim, not from the start of the test, so a slow
	// database cannot make it land before the claim exists.
	e.w.afterClaim = func() { go func() { time.Sleep(30 * time.Millisecond); cancel() }() }

	require.NoError(t, e.w.HandleRecipientJob(ctx, e.job()))

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.NotNil(t, got.MessageID)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	assert.EqualValues(t, 1, meta.calls.Load(), "never resent")
}

func TestB3_CloseWaitsBrieflyForJobsInFlightAndRefusesNewOnes(t *testing.T) {
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

	e.w.BeginShutdown()
	assert.False(t, e.w.Drain(100*time.Millisecond), "a send is in flight: draining times out, it does not hang")

	// Nothing new starts meanwhile.
	other := newClaimEnv(t, newGateMeta(200, claimOKBody))
	other.w.BeginShutdown()
	assert.True(t, errors.Is(other.w.HandleRecipientJob(context.Background(), other.job()), errShuttingDown))

	closed := make(chan struct{})
	go func() { _ = e.w.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a send was still in flight")
	case <-time.After(150 * time.Millisecond):
	}
	meta.Unblock()
	<-done
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the in-flight send finished")
	}
	assert.Equal(t, models.MessageStatusSent, e.rec(t).Status)
	_ = whatsapp.OutcomeUnknown
}

// The drain bound is derived like the claim limit, so shutdown cannot wait forever.
func TestB3_TheDrainBoundFollowsTheMetaTimeout(t *testing.T) {
	w := &Worker{WhatsApp: whatsapp.New(testutil.NopLogger())}
	assert.Equal(t, 30*time.Second+claimWriteBudget/2, w.drainTimeout())
	w.WhatsApp.HTTPClient.Timeout = 5 * time.Second
	assert.Equal(t, 5*time.Second+claimWriteBudget/2, w.drainTimeout())
}

// Several recovery routines (several workers, each with housekeeping) run at once over
// many stuck claims: every claim is resolved and counted exactly once.
func TestRecovery_ManyResolversCountEachStuckClaimOnce(t *testing.T) {
	e := newClaimEnv(t, newGateMeta(200, claimOKBody))
	contact, _, err := contactutilGetOrCreate(e)
	require.NoError(t, err)
	const claims = 20
	ids := make([]string, 0, claims)
	for i := 0; i < claims; i++ {
		r := &models.BulkMessageRecipient{CampaignID: e.c.ID, PhoneNumber: "+5511900000000", Status: models.MessageStatusPending}
		require.NoError(t, e.w.DB.Create(r).Error)
		job := jobFor(e.org, e.c, r)
		msg := e.w.newSecretMessage(job, e.c, contact)
		require.NoError(t, e.w.DB.Create(msg).Error)
		require.NoError(t, e.w.DB.Exec("UPDATE messages SET created_at = clock_timestamp() - interval '10 minutes' WHERE id = ?", msg.ID).Error)
		require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", r.ID).UpdateColumn("message_id", msg.ID).Error)
		ids = append(ids, msg.ID.String())
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.clone(0).ResolveStaleClaims(context.Background())
			assert.NoError(t, err)
		}()
	}
	close(start)
	wg.Wait()

	var failedMsgs int64
	require.NoError(t, e.w.DB.Model(&models.Message{}).Where("id IN ? AND status = ?", ids, models.MessageStatusFailed).Count(&failedMsgs).Error)
	assert.EqualValues(t, claims, failedMsgs, "every stuck claim was resolved")
	var failedRecs int64
	require.NoError(t, e.w.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ? AND message_id IN ? AND status = ?", e.c.ID, ids, models.MessageStatusFailed).Count(&failedRecs).Error)
	assert.EqualValues(t, claims, failedRecs)
	_, failed := e.campaignCounts(t)
	assert.Equal(t, claims, failed, "and counted once each, however many resolvers ran")
}

func contactutilGetOrCreate(e *claimEnv) (*models.Contact, bool, error) {
	return contactutil.GetOrCreateContact(e.w.DB, e.org.ID, e.r.PhoneNumber, e.r.RecipientName)
}
