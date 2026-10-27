package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Review of the merge with development: (1) header media with no scan row is not "clean"; (2) unknown 4xx
// answers from Meta are recorded, redacted, never resent on their own and never leave a recipient pending.

type gateEnv struct {
	w    *Worker
	meta *metaMock
	org  *models.Organization
	c    *models.BulkMessageCampaign
	r    *models.BulkMessageRecipient
}

func normalCampaign(t *testing.T, mediaID, local string) *gateEnv {
	t.Helper()
	w := testWorker(t)
	org, account, _, campaign, recipient := createTestCampaignData(t, w)
	campaign.HeaderMediaID, campaign.HeaderMediaLocalPath = mediaID, local
	require.NoError(t, w.DB.Save(campaign).Error)
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	meta := newMetaMock(200, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.N"}]}`)
	t.Cleanup(meta.Close)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)
	return &gateEnv{w, meta, org, campaign, recipient}
}

func otpCampaign(t *testing.T, mediaID, local string) *gateEnv {
	t.Helper()
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	campaign.HeaderMediaID, campaign.HeaderMediaLocalPath = mediaID, local
	require.NoError(t, w.DB.Save(campaign).Error)
	meta := newMetaMock(200, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.O"}]}`)
	t.Cleanup(meta.Close)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)
	return &gateEnv{w, meta, org, campaign, recipient}
}

func (e *gateEnv) scan(t *testing.T, status string, age time.Duration) {
	t.Helper()
	s := models.FileScan{ID: uuid.New(), OrganizationID: e.org.ID, Source: models.ScanSourceCampaignMedia, ResourceID: e.c.ID,
		StoragePath: "campaigns/h.pdf", Status: status}
	require.NoError(t, e.w.DB.Create(&s).Error)
	require.NoError(t, e.w.DB.Exec("UPDATE file_scans SET created_at = ? WHERE id = ?", time.Now().Add(-age), s.ID).Error)
}

func (e *gateEnv) run() error { return e.w.HandleRecipientJob(context.Background(), jobFor(e.org, e.c, e.r)) }

func (e *gateEnv) rec(t *testing.T) models.BulkMessageRecipient {
	t.Helper()
	var r models.BulkMessageRecipient
	require.NoError(t, e.w.DB.First(&r, e.r.ID).Error)
	return r
}

// (1) A LOCAL copy is WhatC-controlled content: a missing scan row is not a clean verdict, for ordinary and
// one-time-code campaigns alike. The job is held (retried), not sent and not failed while the grace lasts.
func TestMediaGate_LocalCopyWithoutAScanRowIsHeldNotSent(t *testing.T) {
	for name, mk := range map[string]func(*testing.T, string, string) *gateEnv{"normal": normalCampaign, "otp": otpCampaign} {
		t.Run(name, func(t *testing.T) {
			e := mk(t, "media-1", "campaigns/h.pdf")
			err := e.run()
			require.Error(t, err, "held: the queue will hand the job back")
			assert.Contains(t, err.Error(), "pending", "a pending row was registered for the missing verdict")
			assert.Empty(t, e.meta.bodies, "nothing reached Meta")
			assert.NotEqual(t, models.MessageStatusFailed, e.rec(t).Status)
		})
	}
}

// ...and it cannot wait forever: a verdict that never comes fails the recipient with a clear reason.
func TestMediaGate_AVerdictThatNeverComesFailsTheRecipientAfterTheGrace(t *testing.T) {
	for name, mk := range map[string]func(*testing.T, string, string) *gateEnv{"normal": normalCampaign, "otp": otpCampaign} {
		for _, st := range []string{models.ScanPending, models.ScanError} {
			t.Run(name+"/"+st, func(t *testing.T) {
				e := mk(t, "media-1", "campaigns/h.pdf")
				e.scan(t, st, mediaVerdictGrace+time.Minute)
				require.NoError(t, e.run(), "settled: not handed back to the queue forever")
				got := e.rec(t)
				assert.Equal(t, models.MessageStatusFailed, got.Status)
				assert.Contains(t, got.ErrorMessage, "could not be verified")
				assert.Empty(t, e.meta.bodies)
				assert.NotContains(t, got.ErrorMessage, otpCode)
			})
		}
	}
}

// A Meta-hosted id WITHOUT a local copy: proven if this server uploaded it; otherwise it is historical and
// uninspectable. Normal campaigns keep sending it; one-time codes never do.
func TestMediaGate_MetaHostedMediaWithoutALocalCopy(t *testing.T) {
	t.Run("uploaded by this server: proven, sends", func(t *testing.T) {
		for name, mk := range map[string]func(*testing.T, string, string) *gateEnv{"normal": normalCampaign, "otp": otpCampaign} {
			e := mk(t, "media-ours", "")
			require.NoError(t, e.w.DB.Create(&models.MetaMedia{ID: uuid.New(), OrganizationID: e.org.ID, MediaID: "media-ours", Source: "campaign_media"}).Error)
			require.NoError(t, e.run(), name)
			assert.Len(t, e.meta.bodies, 1, name)
		}
	})
	t.Run("historical and uninspectable: normal sends, a one-time code does not", func(t *testing.T) {
		n := normalCampaign(t, "media-historical", "")
		require.NoError(t, n.run())
		assert.Len(t, n.meta.bodies, 1, "previous behaviour for ordinary campaigns")

		o := otpCampaign(t, "media-historical", "")
		require.NoError(t, o.run())
		assert.Empty(t, o.meta.bodies, "no silent exception for a one-time code")
		got := o.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status)
		assert.Contains(t, got.ErrorMessage, "only send media this server scanned")
		assert.True(t, templateutil.IsSealed(got.TemplateParams), "the code stays sealed")
	})
}

// (2) An undocumented 4xx proves nothing: it is recorded as such, sent ONCE, redacted, and the recipient is
// settled (not pending) so the campaign can complete; the operator finds it by the reason prefix.
func TestUnknownProviderAnswers_AreRecordedRedactedAndNeverResent(t *testing.T) {
	t.Run("one-time code path", func(t *testing.T) {
		meta := newGateMeta(400, `{"error":{"code":999999,"message":"weird refusal for 739104"}}`)
		e := newClaimEnv(t, meta)
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()), "a redelivered job does nothing")

		assert.EqualValues(t, 1, meta.calls.Load(), "no automatic resend")
		got := e.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status, "settled, not pending")
		assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain, "identifiable for the operator")
		assert.NotNil(t, got.MessageID, "the claim stays so nothing resends it")
		assert.NotContains(t, got.ErrorMessage, otpCode, "the code is redacted even when Meta echoes it")
		assert.True(t, templateutil.IsSealed(got.TemplateParams))
	})
	t.Run("one-time code path, answer that is not a Meta error object", func(t *testing.T) {
		meta := newGateMeta(418, `<html>proxy says secret=abc123 code 739104</html>`)
		e := newClaimEnv(t, meta)
		require.NoError(t, e.w.HandleRecipientJob(context.Background(), e.job()))
		got := e.rec(t)
		assert.Contains(t, got.ErrorMessage, ReasonDeliveryUncertain)
		assert.NotContains(t, got.ErrorMessage, "abc123", "a raw body is never stored")
		assert.NotContains(t, got.ErrorMessage, otpCode)
		assert.Contains(t, got.ErrorMessage, "API returned status 418")
	})
	t.Run("ordinary campaign path", func(t *testing.T) {
		e := normalCampaign(t, "", "")
		e.meta.status, e.meta.respBody = 418, `<html>proxy says secret=abc123</html>`
		require.NoError(t, e.run())
		assert.Len(t, e.meta.bodies, 1, "sent once")
		got := e.rec(t)
		assert.Equal(t, models.MessageStatusFailed, got.Status, "settled")
		assert.NotContains(t, got.ErrorMessage, "abc123", "no raw provider body in the stored error")
		assert.Contains(t, got.ErrorMessage, "API returned status 418")
	})
}

// The grace has a persisted start: with no scan row at all the gate registers a pending one, the wait is held,
// and once that row is older than the grace the recipient fails, with no clock that a retry or restart resets.
func TestMediaGate_NoRowIsRegisteredAndTheGraceRunsFromThePersistedRow(t *testing.T) {
	e := normalCampaign(t, "media-1", "campaigns/h.pdf")

	require.Error(t, e.run(), "held")
	var rows []models.FileScan
	require.NoError(t, e.w.DB.Where("source = ? AND resource_id = ?", models.ScanSourceCampaignMedia, e.c.ID).Find(&rows).Error)
	require.Len(t, rows, 1, "a pending row was registered so the scan worker can reach the file")
	assert.Equal(t, models.ScanPending, rows[0].Status)
	require.Error(t, e.run(), "a retry is held again and does not create a second row")
	e.w.DB.Model(&models.FileScan{}).Where("source = ? AND resource_id = ?", models.ScanSourceCampaignMedia, e.c.ID).Count(new(int64))

	// time passes (the stored start ages), the worker "restarts" (a fresh call): the recipient fails for good
	require.NoError(t, e.w.DB.Exec("UPDATE file_scans SET created_at = ? WHERE id = ?", time.Now().Add(-mediaVerdictGrace-time.Minute), rows[0].ID).Error)
	require.NoError(t, e.run())
	got := e.rec(t)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, "could not be verified")
	assert.Empty(t, e.meta.bodies, "nothing was ever sent")
}
