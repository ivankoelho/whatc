package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	otpCode = "739104"
	encKey  = "test-encryption-key-for-worker-longer-than-32-chars"
)

type metaMock struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []string
	status   int
	respBody string
}

func newMetaMock(status int, respBody string) *metaMock {
	m := &metaMock{status: status, respBody: respBody}
	m.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(b))
		m.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(m.status)
		_, _ = rw.Write([]byte(m.respBody))
	}))
	return m
}

// authCampaign turns the shared fixture into an AUTHENTICATION campaign whose
// recipient holds the code sealed, as the API stores it.
func authCampaign(t *testing.T, w *Worker, sealedAt time.Time) (*models.Organization, *models.BulkMessageCampaign, *models.BulkMessageRecipient) {
	t.Helper()
	w.Config = &config.Config{App: config.AppConfig{EncryptionKey: encKey}}
	org, account, template, campaign, recipient := createTestCampaignData(t, w)
	require.NoError(t, w.DB.Model(account).Update("api_version", "v21.0").Error)
	require.NoError(t, w.DB.Model(template).Updates(map[string]any{
		"category": "AUTHENTICATION", "body_content": "Seu código é {{1}}. Não compartilhe.",
		"buttons": models.JSONBArray{map[string]any{"type": "OTP", "text": "Copy code", "otp_type": "COPY_CODE"}},
	}).Error)
	sealed, err := templateutil.SealParams(map[string]any{"1": otpCode}, encKey, sealedAt)
	require.NoError(t, err)
	require.NoError(t, w.DB.Model(recipient).UpdateColumn("template_params", models.JSONB(sealed)).Error)
	require.NoError(t, w.DB.First(recipient, recipient.ID).Error)
	return org, campaign, recipient
}

func jobFor(org *models.Organization, c *models.BulkMessageCampaign, r *models.BulkMessageRecipient) *queue.RecipientJob {
	return &queue.RecipientJob{
		CampaignID: c.ID, RecipientID: r.ID, OrganizationID: org.ID,
		PhoneNumber: r.PhoneNumber, RecipientName: r.RecipientName, TemplateParams: r.TemplateParams,
	}
}

// rawRow returns the text of a column so a leak anywhere in it is caught.
func rawRow(t *testing.T, w *Worker, table, col, id string) string {
	t.Helper()
	var s string
	require.NoError(t, w.DB.Raw("SELECT COALESCE(CAST("+col+" AS text),'') FROM "+table+" WHERE id = ?", id).Scan(&s).Error)
	return s
}

func TestWorker_AuthCampaign_CodeOnlyReachesMetaAndIsScrubbedAfterSend(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.otp1"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	// What is persisted and what goes through Redis holds no clear code.
	assert.NotContains(t, rawRow(t, w, "bulk_message_recipients", "template_params", recipient.ID.String()), otpCode)
	payload, _ := json.Marshal(jobFor(org, campaign, recipient))
	assert.NotContains(t, string(payload), otpCode)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	require.Len(t, meta.bodies, 1)
	assert.Equal(t, expectedOTPTemplate(t, w, campaign), sentTemplate(t, meta.bodies[0]), "exact payload: body and OTP button carry the same code")
	assert.Equal(t, 2, strings.Count(meta.bodies[0], otpCode), "code appears only in body and button")

	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, r.Status)
	assert.Empty(t, r.TemplateParams, "code dropped once sent")

	var msg models.Message
	require.NoError(t, w.DB.Where("whats_app_message_id = ?", "wamid.otp1").First(&msg).Error)
	assert.Equal(t, "Seu código é ••••••. Não compartilhe.", msg.Content)
	assert.Empty(t, msg.TemplateParams)
	for _, col := range []string{"content", "template_params", "metadata", "interactive_data", "error_message"} {
		assert.NotContains(t, rawRow(t, w, "messages", col, msg.ID.String()), otpCode, col)
	}
}

func TestWorker_AuthCampaign_FailureKeepsSealedCodeForRetryAndRedactsErrors(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	meta := newMetaMock(400, `{"error":{"code":131008,"message":"Invalid parameter `+otpCode+`"}}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, r.Status)
	assert.NotContains(t, r.ErrorMessage, otpCode)
	assert.Contains(t, r.ErrorMessage, templateutil.AuthCodeMask)

	// Retry still possible: the code is still there, sealed.
	assert.True(t, templateutil.IsSealed(r.TemplateParams))
	opened, err := templateutil.OpenParams(r.TemplateParams, encKey, time.Now())
	require.NoError(t, err)
	assert.Equal(t, otpCode, opened["1"])
	assert.NotContains(t, rawRow(t, w, "bulk_message_recipients", "template_params", r.ID.String()), otpCode)

	var msg models.Message
	require.NoError(t, w.DB.Where("contact_id IS NOT NULL AND message_type = ? AND organization_id = ?", models.MessageTypeTemplate, org.ID).First(&msg).Error)
	assert.NotContains(t, msg.ErrorMessage, otpCode)
	assert.NotContains(t, rawRow(t, w, "messages", "metadata", msg.ID.String()), otpCode)
}

func TestWorker_AuthCampaign_ExpiredCodeIsNotSentAndIsScrubbed(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now().Add(-templateutil.SealedTTL-time.Hour))
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.never"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	assert.Empty(t, meta.bodies, "an expired code is never sent")
	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, r.Status)
	assert.Empty(t, r.TemplateParams)
}

func TestWorker_AuthCampaign_RedeliveredJobAfterSendDoesNotResendOrFail(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.once"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	job := jobFor(org, campaign, recipient)
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	// Crash before ACK: the same job (with its sealed payload) is delivered again.
	require.NoError(t, w.HandleRecipientJob(context.Background(), job))
	// ...or arrives with an already-empty payload.
	empty := *job
	empty.TemplateParams = models.JSONB{}
	require.NoError(t, w.HandleRecipientJob(context.Background(), &empty))

	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusSent, r.Status)
	assert.Len(t, meta.bodies, 1, "redelivery must not send the code twice")
}

func TestScrubExpiredSecrets_OnlyDropsOldSealedCodes(t *testing.T) {
	w := testWorker(t)
	_, _, old := authCampaign(t, w, time.Now().Add(-templateutil.SealedTTL-time.Minute))
	_, _, fresh := authCampaign(t, w, time.Now())
	legacy := &models.BulkMessageRecipient{CampaignID: fresh.CampaignID, PhoneNumber: "999", Status: models.MessageStatusPending,
		TemplateParams: models.JSONB{"1": "keep"}}
	require.NoError(t, w.DB.Create(legacy).Error)

	n, err := ScrubExpiredSecrets(w.DB, time.Now(), templateutil.OTPPolicy{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	var o, f, l models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&o, old.ID).Error)
	require.NoError(t, w.DB.First(&f, fresh.ID).Error)
	require.NoError(t, w.DB.First(&l, legacy.ID).Error)
	assert.Empty(t, o.TemplateParams)
	assert.True(t, templateutil.IsSealed(f.TemplateParams))
	assert.Equal(t, "keep", l.TemplateParams["1"], "non-sealed rows are not touched by the TTL sweep")
	assert.False(t, strings.Contains(rawRow(t, w, "bulk_message_recipients", "template_params", f.ID.String()), otpCode))
}

// sentTemplate returns the "template" object of a Meta /messages request.
func sentTemplate(t *testing.T, body string) map[string]any {
	t.Helper()
	var req map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	tpl, ok := req["template"].(map[string]any)
	require.True(t, ok, "request has no template: %s", body)
	return tpl
}

// expectedOTPTemplate is the full structure Meta requires for an
// authentication template with a copy-code button (error 131008 otherwise):
// the code in the body AND in a url-type button parameter, same value.
func expectedOTPTemplate(t *testing.T, w *Worker, c *models.BulkMessageCampaign) map[string]any {
	t.Helper()
	var tpl models.Template
	require.NoError(t, w.DB.First(&tpl, c.TemplateID).Error)
	return map[string]any{
		"name":     tpl.Name,
		"language": map[string]any{"code": tpl.Language},
		"components": []any{
			map[string]any{"type": "body", "parameters": []any{map[string]any{"type": "text", "text": otpCode}}},
			map[string]any{"type": "button", "sub_type": "url", "index": "0", "parameters": []any{map[string]any{"type": "text", "text": otpCode}}},
		},
	}
}

func TestWorker_AuthCampaign_TemplateWithoutOTPButtonSendsBodyOnly(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	require.NoError(t, w.DB.Model(&models.Template{}).Where("id = ?", campaign.TemplateID).
		Update("buttons", models.JSONBArray{}).Error)
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.nobtn"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	require.Len(t, meta.bodies, 1)
	comps := sentTemplate(t, meta.bodies[0])["components"].([]any)
	require.Len(t, comps, 1, "no OTP button on the template: no button component")
	assert.Equal(t, "body", comps[0].(map[string]any)["type"])
}

func TestWorker_NormalCampaign_NeverGetsAnOTPButtonComponent(t *testing.T) {
	w := testWorker(t)
	org, _, template, campaign, recipient := createTestCampaignData(t, w)
	// Even a button list that contains an OTP entry is ignored outside AUTHENTICATION.
	require.NoError(t, w.DB.Model(template).Update("buttons", models.JSONBArray{map[string]any{"type": "OTP", "otp_type": "COPY_CODE"}}).Error)
	require.NoError(t, w.DB.Model(&models.WhatsAppAccount{}).Where("organization_id = ?", org.ID).Update("api_version", "v21.0").Error)
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.plain"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	require.Len(t, meta.bodies, 1)
	comps := sentTemplate(t, meta.bodies[0])["components"].([]any)
	require.Len(t, comps, 1)
	assert.Equal(t, "body", comps[0].(map[string]any)["type"])
	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, "John", r.TemplateParams["1"], "normal campaigns keep their params as before")
}

func TestWorker_AuthCampaign_MissingCodeIsNotSentAndFails(t *testing.T) {
	w := testWorker(t)
	org, campaign, recipient := authCampaign(t, w, time.Now())
	// A sealed envelope that opens to nothing.
	empty, err := templateutil.SealParams(map[string]any{}, encKey, time.Now())
	require.NoError(t, err)
	require.NoError(t, w.DB.Model(recipient).UpdateColumn("template_params", models.JSONB(empty)).Error)
	require.NoError(t, w.DB.First(recipient, recipient.ID).Error)
	meta := newMetaMock(200, `{"messages":[{"id":"wamid.never"}]}`)
	defer meta.Close()
	w.WhatsApp = whatsapp.NewWithBaseURL(w.Log, meta.URL)

	require.NoError(t, w.HandleRecipientJob(context.Background(), jobFor(org, campaign, recipient)))

	assert.Empty(t, meta.bodies)
	var r models.BulkMessageRecipient
	require.NoError(t, w.DB.First(&r, recipient.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, r.Status)
}
