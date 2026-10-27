package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reseal replaces the recipient's params with an envelope sealed at `at` whose
// issuer validity is `until` (nil = issuer gave none).
func reseal(t *testing.T, w *Worker, r *models.BulkMessageRecipient, at time.Time, until *time.Time) {
	t.Helper()
	sealed, err := templateutil.SealParamsUntil(map[string]any{"1": otpCode}, encKey, at, until)
	require.NoError(t, err)
	require.NoError(t, w.DB.Model(r).UpdateColumn("template_params", models.JSONB(sealed)).Error)
	require.NoError(t, w.DB.First(r, r.ID).Error)
}

func timePtr(t time.Time) *time.Time { return &t }

type otpRun struct {
	w    *Worker
	meta *metaMock
}

// setupOTP returns a worker whose clock is `now` and whose Meta mock answers `status`.
func setupOTP(t *testing.T, now time.Time, status int, cfg config.OTPConfig) (*otpRun, *models.Organization, *models.BulkMessageCampaign, *models.BulkMessageRecipient) {
	t.Helper()
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, now)
	w.Config.OTP = cfg
	w.Now = func() time.Time { return now }
	body := `{"messages":[{"id":"wamid.v"}]}`
	if status != 200 {
		body = `{"error":{"code":100,"message":"bad"}}`
	}
	meta := newMetaMock(status, body)
	t.Cleanup(meta.Close)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)
	return &otpRun{w, meta}, org, campaign, recipient
}

func (o *otpRun) recipient(t *testing.T, id uuid.UUID) models.BulkMessageRecipient {
	t.Helper()
	var r models.BulkMessageRecipient
	require.NoError(t, o.w.DB.Where("id = ?", id).First(&r).Error)
	return r
}

func TestOTPValidity_ValidCodeIsSent(t *testing.T) {
	now := time.Now()
	o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
	reseal(t, o.w, r, now, timePtr(now.Add(3*time.Minute)))

	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))

	require.Len(t, o.meta.bodies, 1)
	assert.Equal(t, models.MessageStatusSent, o.recipient(t, r.ID).Status)
}

func TestOTPValidity_ExpiredCodeIsNotSentFailsWithReasonAndIsScrubbed(t *testing.T) {
	now := time.Now()
	o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
	reseal(t, o.w, r, now.Add(-10*time.Minute), timePtr(now.Add(-time.Second)))

	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))

	assert.Empty(t, o.meta.bodies, "an expired code never reaches Meta")
	got := o.recipient(t, r.ID)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, templateutil.ReasonOTPExpired)
	assert.Empty(t, got.TemplateParams, "the secret is dropped on expiry")
}

func TestOTPValidity_ExpiresWhileWaitingInTheQueue(t *testing.T) {
	sealedAt := time.Now()
	until := sealedAt.Add(2 * time.Minute)

	// Picked up while still valid: sent.
	o, org, c, r := setupOTP(t, sealedAt.Add(time.Minute), 200, config.OTPConfig{})
	reseal(t, o.w, r, sealedAt, &until)
	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))
	assert.Len(t, o.meta.bodies, 1)

	// The same job picked up after the deadline: not sent.
	o2, org2, c2, r2 := setupOTP(t, sealedAt.Add(3*time.Minute), 200, config.OTPConfig{})
	reseal(t, o2.w, r2, sealedAt, &until)
	require.NoError(t, o2.w.HandleRecipientJob(context.Background(), jobFor(org2, c2, r2)))
	assert.Empty(t, o2.meta.bodies)
	assert.Contains(t, o2.recipient(t, r2.ID).ErrorMessage, templateutil.ReasonOTPExpired)
}

func TestOTPValidity_SmallerOfIssuerDeadlineAndSendWindowWins(t *testing.T) {
	sealedAt := time.Now()
	farFuture := sealedAt.Add(time.Hour) // the issuer says an hour...

	// ...but the default 5-minute window caps it.
	o, org, c, r := setupOTP(t, sealedAt.Add(6*time.Minute), 200, config.OTPConfig{})
	reseal(t, o.w, r, sealedAt, &farFuture)
	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))
	assert.Empty(t, o.meta.bodies, "send window of 5 minutes applies")

	// A configured 10-minute window lets the same code through at 6 minutes.
	o2, org2, c2, r2 := setupOTP(t, sealedAt.Add(6*time.Minute), 200, config.OTPConfig{MaxValidityMinutes: 10})
	reseal(t, o2.w, r2, sealedAt, &farFuture)
	require.NoError(t, o2.w.HandleRecipientJob(context.Background(), jobFor(org2, c2, r2)))
	assert.Len(t, o2.meta.bodies, 1)

	// An issuer deadline shorter than the window is respected.
	short := sealedAt.Add(time.Minute)
	o3, org3, c3, r3 := setupOTP(t, sealedAt.Add(2*time.Minute), 200, config.OTPConfig{MaxValidityMinutes: 10})
	reseal(t, o3.w, r3, sealedAt, &short)
	require.NoError(t, o3.w.HandleRecipientJob(context.Background(), jobFor(org3, c3, r3)))
	assert.Empty(t, o3.meta.bodies)
}

func TestOTPValidity_RetryBeforeExpiryWorksAfterExpiryDoesNot(t *testing.T) {
	sealedAt := time.Now()
	until := sealedAt.Add(4 * time.Minute)
	o, org, c, r := setupOTP(t, sealedAt.Add(time.Minute), 400, config.OTPConfig{})
	reseal(t, o.w, r, sealedAt, &until)

	// First attempt: Meta refuses. The recipient fails but keeps its sealed code.
	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))
	failed := o.recipient(t, r.ID)
	require.Equal(t, models.MessageStatusFailed, failed.Status)
	require.True(t, templateutil.IsSealed(failed.TemplateParams))
	assert.Equal(t, until.Unix(), int64(failed.TemplateParams[templateutil.SealedValidUntilKey].(float64)), "failure does not touch validity")

	// Retry (RetryFailed resets to pending and re-enqueues the stored envelope) while valid.
	o.meta.mu.Lock()
	o.meta.status, o.meta.respBody = 200, `{"messages":[{"id":"wamid.retry"}]}`
	o.meta.mu.Unlock()
	require.NoError(t, o.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", r.ID).Update("status", models.MessageStatusPending).Error)
	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, &failed)))
	assert.Equal(t, models.MessageStatusSent, o.recipient(t, r.ID).Status)

	// Another recipient: the same retry after the deadline is refused without
	// any call to Meta, and the earlier failure did not extend the validity.
	o2, org2, c2, r2 := setupOTP(t, sealedAt.Add(time.Minute), 400, config.OTPConfig{})
	reseal(t, o2.w, r2, sealedAt, &until)
	require.NoError(t, o2.w.HandleRecipientJob(context.Background(), jobFor(org2, c2, r2)))
	failed2 := o2.recipient(t, r2.ID)
	calls := len(o2.meta.bodies)
	o2.w.Now = func() time.Time { return sealedAt.Add(5 * time.Minute) }
	require.NoError(t, o2.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", r2.ID).Update("status", models.MessageStatusPending).Error)
	require.NoError(t, o2.w.HandleRecipientJob(context.Background(), jobFor(org2, c2, &failed2)))
	assert.Equal(t, calls, len(o2.meta.bodies), "no Meta call after expiry")
	final := o2.recipient(t, r2.ID)
	assert.Contains(t, final.ErrorMessage, templateutil.ReasonOTPExpired)
	assert.Empty(t, final.TemplateParams)
}

func TestOTPValidity_ScheduledCampaignNeverSendsAnExpiredCode(t *testing.T) {
	sealedAt := time.Now()
	until := sealedAt.Add(time.Minute)
	// The campaign was scheduled two hours after the code was issued.
	o, org, c, r := setupOTP(t, sealedAt.Add(2*time.Hour), 200, config.OTPConfig{})
	reseal(t, o.w, r, sealedAt, &until)

	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))

	assert.Empty(t, o.meta.bodies)
	assert.Contains(t, o.recipient(t, r.ID).ErrorMessage, templateutil.ReasonOTPExpired)
}

func TestOTPValidity_MissingValidUntil(t *testing.T) {
	now := time.Now()
	// Transition mode (default): legacy codes without validity keep working.
	o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
	reseal(t, o.w, r, now, nil)
	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))
	assert.Len(t, o.meta.bodies, 1, "not invalidated silently while enforcement is off")

	// Enforcement on: no confirmed validity, no send.
	o2, org2, c2, r2 := setupOTP(t, now, 200, config.OTPConfig{RequireValidUntil: true})
	reseal(t, o2.w, r2, now, nil)
	require.NoError(t, o2.w.HandleRecipientJob(context.Background(), jobFor(org2, c2, r2)))
	assert.Empty(t, o2.meta.bodies)
	got := o2.recipient(t, r2.ID)
	assert.Contains(t, got.ErrorMessage, templateutil.ReasonOTPValidityMissing)
	assert.Empty(t, got.TemplateParams)
}

func TestOTPValidity_SweeperDropsOnlyWhatCannotBeSentAnymore(t *testing.T) {
	now := time.Now()
	w := testWorker(t)
	w.Config = &config.Config{App: config.AppConfig{EncryptionKey: encKey}}
	_, campaign, _ := authCampaign(t, w, now)
	mk := func(sealedAt time.Time, until *time.Time) *models.BulkMessageRecipient {
		r := &models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: "9", Status: models.MessageStatusPending}
		require.NoError(t, w.DB.Create(r).Error)
		reseal(t, w, r, sealedAt, until)
		return r
	}
	expired := mk(now.Add(-time.Hour), timePtr(now.Add(-time.Minute)))
	capped := mk(now.Add(-10*time.Minute), timePtr(now.Add(time.Hour))) // issuer ok, but past the 5-minute window
	valid := mk(now, timePtr(now.Add(3*time.Minute)))
	legacyFresh := mk(now.Add(-2*time.Hour), nil)
	legacyOld := mk(now.Add(-25*time.Hour), nil)

	_, err := ScrubExpiredSecrets(w.DB, now, templateutil.OTPPolicy{RequireValidUntil: true})
	require.NoError(t, err)

	sealed := func(r *models.BulkMessageRecipient) bool {
		var got models.BulkMessageRecipient
		require.NoError(t, w.DB.First(&got, r.ID).Error)
		return templateutil.IsSealed(got.TemplateParams)
	}
	assert.False(t, sealed(expired), "past valid_until: dropped")
	assert.False(t, sealed(capped), "past the send window: dropped")
	assert.True(t, sealed(valid), "still valid: kept")
	assert.True(t, sealed(legacyFresh), "legacy without validity is never dropped before retention, even when enforcement is on")
	assert.False(t, sealed(legacyOld), "retention limit applies to legacy codes")
}

func TestOTPValidity_NormalCampaignsIgnoreThePolicy(t *testing.T) {
	w := testWorker(t)
	org, account, _, campaign, recipient := createTestCampaignData(t, w)
	w.Config = &config.Config{App: config.AppConfig{EncryptionKey: encKey}, OTP: config.OTPConfig{RequireValidUntil: true}}
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.n"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	assert.Len(t, meta.bodies, 1)
	var got models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&got, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, got.Status)
}
