package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- B4: the code is cleared by the same transaction that records the send ----

// failMessageWrites makes every UPDATE that sets a message of the campaign to the given
// status raise an error, until the returned function is called.
func (e *claimEnv) failMessageWrites(t *testing.T, status string) (restore func()) {
	t.Helper()
	fn := "fn_b4_" + strings.ReplaceAll(e.c.ID.String()[:8], "-", "")
	require.NoError(t, e.w.DB.Exec(`CREATE FUNCTION `+fn+`() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'messages are not writable'; END $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, e.w.DB.Exec(`CREATE TRIGGER `+fn+` BEFORE UPDATE ON messages FOR EACH ROW
		WHEN (NEW.status = '`+status+`' AND NEW.metadata->>'campaign_id' = '`+e.c.ID.String()+`') EXECUTE FUNCTION `+fn+`()`).Error)
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

func (e *claimEnv) rawParams(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, e.w.DB.Raw("SELECT COALESCE(CAST(template_params AS text),'') FROM bulk_message_recipients WHERE id = ?", e.r.ID).Scan(&s).Error)
	return s
}

func TestB4_AConfirmedSendClearsTheCodeWithTheSameWrite(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	require.True(t, templateutil.IsSealed(e.rec(t).TemplateParams), "precondition: a sealed code is stored")

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusSent, got.Status)
	assert.NotEmpty(t, got.WhatsAppMessageID, "what is needed to audit the send stays")
	assert.NotNil(t, got.MessageID)
	assert.False(t, templateutil.IsSealed(got.TemplateParams))
	assert.Empty(t, got.TemplateParams, "no sealed code, no clear code")
	assert.NotContains(t, e.rawParams(t), otpCode)
	msgs := e.messages(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusSent, msgs[0].Status)
	assert.Equal(t, got.WhatsAppMessageID, msgs[0].WhatsAppMessageID)
}

// If the message write fails, nothing is committed: the recipient is still claimed and
// the code is still there, so the claim can be resolved as uncertain without losing it.
func TestB4_AFailedMessageWriteRollsBackTheSendAndKeepsTheCode(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	restore := e.failMessageWrites(t, "sent")

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusPending, got.Status, "the recipient update was rolled back")
	assert.Empty(t, got.WhatsAppMessageID)
	assert.True(t, templateutil.IsSealed(got.TemplateParams), "the code was not cleared")
	sent, _ := e.campaignCounts(t)
	assert.Zero(t, sent, "counters only after a commit")

	// Meta did accept it, so housekeeping must call it uncertain (never resend), keeping the code.
	restore()
	n, err := e.clone(time.Millisecond).ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	got = e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.True(t, templateutil.IsSealed(got.TemplateParams), "an uncertain delivery keeps its code for reconciliation")
	assert.EqualValues(t, 1, meta.calls.Load(), "and nothing was sent again")
}

func TestB4_DuplicateJobsAfterASendDoNothingAndLeaveNoCode(t *testing.T) {
	meta := newGateMeta(200, claimOKBody)
	e := newClaimEnv(t, meta)
	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	for i := 0; i < 3; i++ {
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
	}

	assert.EqualValues(t, 1, meta.calls.Load())
	assert.Len(t, e.messages(t), 1)
	assert.Empty(t, e.rec(t).TemplateParams)
	sent, failed := e.campaignCounts(t)
	assert.Equal(t, 1, sent)
	assert.Zero(t, failed)
}

// A confirmation that arrives after the claim was reconciled is refused whole: it must
// not clear the code of a claim that is now uncertain.
func TestB4_ALateConfirmationDoesNotClearTheCodeOfAnUncertainClaim(t *testing.T) {
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
	n, err := e.clone(time.Millisecond).ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)

	meta.Unblock()
	<-done

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
	assert.True(t, templateutil.IsSealed(got.TemplateParams), "the code is kept: a person may need to reconcile it")
}

// Whoever wins the race, recipient, message and code agree: sent means no code,
// uncertain means the code is kept. Never "sent with a code" or "uncertain without one".
func TestB4_ConcurrentFinalizationAndRecoveryNeverLeaveAContradiction(t *testing.T) {
	for i := 0; i < 6; i++ {
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
		time.Sleep(20 * time.Millisecond)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); meta.Unblock() }()
		go func() { defer wg.Done(); _, _ = e.clone(time.Millisecond).ResolveStaleClaims(context.Background()) }()
		wg.Wait()
		<-done

		got := e.rec(t)
		msgs := e.messages(t)
		require.Len(t, msgs, 1)
		switch got.Status {
		case models.MessageStatusSent:
			assert.Empty(t, got.TemplateParams, "sent: the code is gone")
			assert.Equal(t, models.MessageStatusSent, msgs[0].Status)
		case models.MessageStatusFailed:
			assert.True(t, templateutil.IsSealed(got.TemplateParams), "uncertain: the code is kept")
			assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
			assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		default:
			t.Fatalf("unexpected final status %q", got.Status)
		}
		assert.EqualValues(t, 1, meta.calls.Load())
	}
}

// States that are not a confirmed send never clear the code.
func TestB4_OnlyAConfirmedSendClearsTheCode(t *testing.T) {
	t.Run("uncertain (5xx)", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(502, `{"error":{"code":1,"message":"temporarily unavailable"}}`))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		assert.True(t, templateutil.IsSealed(e.rec(t).TemplateParams))
	})
	t.Run("refused by Meta (kept for the retry)", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(400, `{"error":{"code":131008,"message":"Required parameter is missing"}}`))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.Nil(t, got.MessageID)
		assert.True(t, templateutil.IsSealed(got.TemplateParams))
	})
	t.Run("a crashed claim that is still pending", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(200, claimOKBody))
		e.crashedClaim(t, 0)
		assert.True(t, templateutil.IsSealed(e.rec(t).TemplateParams))
	})
	t.Run("otp_unavailable (wrong key) keeps the ciphertext", func(t *testing.T) {
		e := newClaimEnv(t, newGateMeta(200, claimOKBody))
		e.w.Config.App.EncryptionKey = "another-key-that-does-not-open-it-0123456789"
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.Contains(t, got.ErrorMessage, "otp_unavailable")
		assert.True(t, templateutil.IsSealed(got.TemplateParams))
	})
}

// A code dropped because it can never be sent (expired) is dropped by the same write
// that fails the recipient, and a later retry state is never touched by a stray clear.
func TestB4_ADroppedExpiredCodeIsClearedByTheFailureItself(t *testing.T) {
	e := newClaimEnv(t, newGateMeta(200, claimOKBody))
	past := time.Now().Add(-time.Minute)
	reseal(t, e.w, e.r, time.Now().Add(-time.Hour), &past)
	e.r.TemplateParams = e.rec(t).TemplateParams

	require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))

	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Empty(t, got.TemplateParams)
	assert.Nil(t, got.MessageID)
}
