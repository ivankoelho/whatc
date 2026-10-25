package worker

import (
	"context"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M1: a key that is missing or wrong on a worker, or an envelope that does not
// open, must not destroy the stored code. The recipient fails visibly, the job
// is acknowledged (no endless redelivery) and everything recovers once the
// correct key is back.
func TestOTPKey_WrongOrMissingKeyKeepsTheCodeAndRecoversAfterRestore(t *testing.T) {
	for _, tc := range []struct {
		name, key, reason string
	}{
		{"wrong key", "another-encryption-key-another-encryption-key", "wrong encryption key"},
		{"missing key", "", "encryption key not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			until := now.Add(3 * time.Minute)
			o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
			reseal(t, o.w, r, now, &until)
			sealedBefore := rawRow(t, o.w, "bulk_message_recipients", "template_params", r.ID.String())

			// A misconfigured worker picks the job up.
			o.w.Config.App.EncryptionKey = tc.key
			require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)), "acknowledged: no endless redelivery")

			assert.Empty(t, o.meta.bodies, "nothing is sent")
			got := o.recipient(t, r.ID)
			assert.Equal(t, models.MessageStatusFailed, got.Status)
			assert.Contains(t, got.ErrorMessage, "otp_unavailable")
			assert.NotContains(t, got.ErrorMessage, "otp_expired", "not mistaken for an expired code")
			assert.Equal(t, sealedBefore, rawRow(t, o.w, "bulk_message_recipients", "template_params", r.ID.String()),
				"the ciphertext is untouched")

			// The key is restored; RetryFailed would reset the recipient to pending and re-enqueue it.
			o.w.Config.App.EncryptionKey = encKey
			require.NoError(t, o.w.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", r.ID).Update("status", models.MessageStatusPending).Error)
			require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, &got)))

			assert.Len(t, o.meta.bodies, 1, "the code is sent after the key is restored")
			assert.Equal(t, models.MessageStatusSent, o.recipient(t, r.ID).Status)
			assert.Empty(t, o.recipient(t, r.ID).TemplateParams, "and dropped once sent")
		})
	}
}

func TestOTPKey_InvalidEnvelopeIsNotDestroyed(t *testing.T) {
	now := time.Now()
	o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
	// An envelope whose ciphertext is not a ciphertext (corrupt or forged).
	bad := models.JSONB{templateutil.SealedKey: "not-a-ciphertext", templateutil.SealedAtKey: now.Unix()}
	require.NoError(t, o.w.DB.Model(r).UpdateColumn("template_params", bad).Error)
	require.NoError(t, o.w.DB.First(r, r.ID).Error)

	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))

	assert.Empty(t, o.meta.bodies)
	got := o.recipient(t, r.ID)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, "otp_unavailable")
	assert.Equal(t, "not-a-ciphertext", got.TemplateParams[templateutil.SealedKey], "kept for diagnosis, not wiped")
}

// What is genuinely over is still final: the secret goes.
func TestOTPKey_RetentionExpiredIsStillFinalAndScrubbed(t *testing.T) {
	now := time.Now()
	o, org, c, r := setupOTP(t, now, 200, config.OTPConfig{})
	reseal(t, o.w, r, now.Add(-templateutil.SealedTTL-time.Hour), nil)

	require.NoError(t, o.w.HandleRecipientJob(context.Background(), jobFor(org, c, r)))

	got := o.recipient(t, r.ID)
	assert.Contains(t, got.ErrorMessage, templateutil.ReasonOTPExpired)
	assert.Empty(t, got.TemplateParams)
}
