package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration of the one-time-code path with the antimalware gate: a code is never sent together with header
// media that is infected or has no clean verdict, and the code is never exposed in the process.

func otpCampaignWithMedia(t *testing.T, w *Worker, verdict string) (*metaMock, *models.Organization, *models.BulkMessageCampaign, *models.BulkMessageRecipient) {
	t.Helper()
	org, campaign, recipient := authCampaign(t, w, time.Now())
	campaign.HeaderMediaID = "media-1"
	campaign.HeaderMediaLocalPath = "campaigns/header.pdf"
	require.NoError(t, w.DB.Save(campaign).Error)
	if verdict != "" {
		require.NoError(t, w.DB.Create(&models.FileScan{ID: uuid.New(), OrganizationID: org.ID, Source: models.ScanSourceCampaignMedia,
			ResourceID: campaign.ID, StoragePath: "campaigns/header.pdf", Status: verdict}).Error)
	}
	meta := newMetaMock(200, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.X"}]}`)
	t.Cleanup(meta.Close)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)
	return meta, org, campaign, recipient
}

func TestWorker_OTPCampaign_InfectedHeaderMediaIsNotSentAndTheCodeStaysSealed(t *testing.T) {
	w := testWorker(t)
	meta, org, campaign, recipient := otpCampaignWithMedia(t, w, models.ScanInfected)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	assert.Empty(t, meta.bodies, "nothing reached Meta")
	var got models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&got, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, "malware")
	for _, col := range []string{"error_message", "template_params"} {
		assert.NotContains(t, rawRow(t, w, "bulk_message_recipients", col, recipient.ID.String()), otpCode, "the code is not readable in "+col)
	}
}

func TestWorker_OTPCampaign_MediaWithoutACleanVerdictIsHeldNotSentNorFailed(t *testing.T) {
	for _, verdict := range []string{models.ScanPending, models.ScanError} {
		t.Run(verdict, func(t *testing.T) {
			w := testWorker(t)
			meta, org, campaign, recipient := otpCampaignWithMedia(t, w, verdict)

			err := w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient))

			require.Error(t, err, "the job is retried later instead of sending")
			assert.Contains(t, err.Error(), "antimalware")
			assert.NotContains(t, err.Error(), otpCode)
			assert.Empty(t, meta.bodies, "nothing reached Meta")
			var got models.BulkMessageRecipient
			require.NoError(t, w.DB.First(&got, recipient.ID).Error)
			assert.NotEqual(t, models.MessageStatusFailed, got.Status, "a scan that is only running does not fail the recipient for good")
		})
	}
}

func TestWorker_OTPCampaign_CleanMediaStillSendsTheCode(t *testing.T) {
	w := testWorker(t)
	meta, org, campaign, recipient := otpCampaignWithMedia(t, w, models.ScanClean)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	require.Len(t, meta.bodies, 1, "a clean verdict releases the send")
	assert.Contains(t, meta.bodies[0], otpCode, "the code reaches Meta, and only Meta")
}

// A normal (non-OTP) campaign is unaffected by the OTP path and keeps its previous behaviour: no scan row at all
// (a Meta-hosted id with no local copy) sends as before, a clean row sends, infected fails.
func TestWorker_NormalCampaign_MediaGateKeepsItsBehaviour(t *testing.T) {
	for name, verdict := range map[string]string{"no row": "", "clean": models.ScanClean} {
		t.Run(name, func(t *testing.T) {
			w := testWorker(t)
			org, account, _, campaign, recipient := createTestCampaignData(t, w)
			campaign.HeaderMediaID = "media-1"
			if verdict != "" { // a local copy exists only when a scan row does; a bare Meta-hosted id has no local copy
				campaign.HeaderMediaLocalPath = "campaigns/h.pdf"
			}
			require.NoError(t, w.DB.Save(campaign).Error)
			if verdict != "" {
				require.NoError(t, w.DB.Create(&models.FileScan{ID: uuid.New(), OrganizationID: org.ID, Source: models.ScanSourceCampaignMedia,
					ResourceID: campaign.ID, StoragePath: "campaigns/h.pdf", Status: verdict}).Error)
			}
			require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
			meta := newMetaMock(200, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.N"}]}`)
			defer meta.Close()
			w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

			require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))
			assert.Len(t, meta.bodies, 1)
		})
	}
}
