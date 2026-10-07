package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func runCampaignJob(t *testing.T, w *Worker, status int, body map[string]any) (campaign *models.BulkMessageCampaign, recipient *models.BulkMessageRecipient, org *models.Organization) {
	t.Helper()
	org, account, _, campaign, recipient := createTestCampaignData(t, w)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_ = json.NewEncoder(rw).Encode(body)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, server.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), &queue.RecipientJob{
		CampaignID: campaign.ID, RecipientID: recipient.ID, OrganizationID: org.ID,
		PhoneNumber: recipient.PhoneNumber, RecipientName: recipient.RecipientName, TemplateParams: recipient.TemplateParams,
	}))
	return campaign, recipient, org
}

func usageRows(t *testing.T, w *Worker, orgID any) []models.MessageUsage {
	t.Helper()
	var rows []models.MessageUsage
	require.NoError(t, w.DB.Where("organization_id = ?", orgID).Find(&rows).Error)
	return rows
}

var okBody = map[string]any{"messages": []map[string]any{{"id": "wamid.campaign1"}}}

func TestWorker_CampaignUsage_RowIsLinkedAndCarriesTheCampaignAndTemplate(t *testing.T) {
	w := testWorker(t)
	w.Usage = usage.New(w.DB, config.UsageConfig{})
	campaign, _, org := runCampaignJob(t, w, http.StatusOK, okBody)

	rows := usageRows(t, w, org.ID)
	require.Len(t, rows, 1, "one campaign message, one row")
	r := rows[0]
	assert.Equal(t, "campaign", r.ActorType)
	assert.Equal(t, &campaign.ID, r.CampaignID)
	assert.Equal(t, "wamid.campaign1", r.Wamid)
	assert.Equal(t, "linked", r.LinkState, "the Message is created after the send and still ends up linked")
	assert.NotNil(t, r.MessageID)
	assert.Equal(t, "MARKETING", r.DeclaredCategory)
	assert.NotEmpty(t, r.TemplateName)
	assert.Equal(t, "pending", r.BillingState)
}

func TestWorker_CampaignUsage_RefusedSendIsSendFailed(t *testing.T) {
	w := testWorker(t)
	w.Usage = usage.New(w.DB, config.UsageConfig{})
	_, _, org := runCampaignJob(t, w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "bad", "code": 100}})

	rows := usageRows(t, w, org.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, "send_failed", rows[0].BillingState)
	assert.Empty(t, rows[0].Wamid)
}

func TestWorker_CampaignUsage_DisabledChangesNothing(t *testing.T) {
	off := false
	w := testWorker(t)
	w.Usage = usage.New(w.DB, config.UsageConfig{RecordEnabled: &off})
	_, recipient, org := runCampaignJob(t, w, http.StatusOK, okBody)

	assert.Empty(t, usageRows(t, w, org.ID))
	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, r.Status)
	var n int64
	w.DB.Model(&models.Message{}).Where("organization_id = ?", org.ID).Count(&n)
	assert.EqualValues(t, 1, n)
}

// F1: a broken recorder must not affect the campaign.
func TestWorker_CampaignUsage_BrokenRecorderIsFailOpen(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	require.NotEmpty(t, dsn)
	broken, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := broken.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	w := testWorker(t)
	w.Usage = usage.New(broken, config.UsageConfig{})
	campaign, recipient, org := runCampaignJob(t, w, http.StatusOK, okBody)

	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, r.Status)
	var c models.BulkMessageCampaign
	require.NoError(t, w.DB.First(&c, campaign.ID).Error)
	assert.Equal(t, 1, c.SentCount)
	var n int64
	w.DB.Model(&models.Message{}).Where("organization_id = ?", org.ID).Count(&n)
	assert.EqualValues(t, 1, n, "the message record was saved")
	assert.Empty(t, usageRows(t, w, org.ID))
}

func TestWorker_CampaignUsage_PanelSwitchOffRecordsNothingAndTheCampaignRuns(t *testing.T) {
	w := testWorker(t)
	w.Usage = usage.New(w.DB, config.UsageConfig{})
	org, account, _, campaign, recipient := createTestCampaignData(t, w)
	_, err := w.Usage.SetRecording(context.Background(), org.ID, false, time.Now())
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(okBody)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, server.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), &queue.RecipientJob{
		CampaignID: campaign.ID, RecipientID: recipient.ID, OrganizationID: org.ID,
		PhoneNumber: recipient.PhoneNumber, RecipientName: recipient.RecipientName, TemplateParams: recipient.TemplateParams,
	}))

	assert.Empty(t, usageRows(t, w, org.ID))
	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, r.Status)
}
