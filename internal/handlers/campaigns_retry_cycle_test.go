package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/worker"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// cycleEnv runs the REAL import, the REAL worker (against a Meta mock) and the REAL
// RetryFailed handler on one authentication campaign.
type cycleEnv struct {
	*authCampaignFixture
	w      *worker.Worker
	status atomic.Int32
	body   atomic.Value
	calls  atomic.Int32
	srv    *httptest.Server
}

func newCycleEnv(t *testing.T) *cycleEnv {
	t.Helper()
	f := newAuthCampaign(t, models.CampaignStatusDraft) // recipients can only be imported into a draft
	var tpl models.Template
	require.NoError(t, f.app.DB.First(&tpl, f.campaign.TemplateID).Error)
	require.NoError(t, f.app.DB.Model(&models.WhatsAppAccount{}).Where("name = ? AND organization_id = ?", f.campaign.WhatsAppAccount, f.org.ID).Update("api_version", "v21.0").Error)
	require.NoError(t, f.app.DB.Model(&tpl).Updates(map[string]any{
		"body_content": "Seu código é {{1}}. Não compartilhe.",
		"buttons":      models.JSONBArray{map[string]any{"type": "OTP", "text": "Copy code", "otp_type": "COPY_CODE"}},
	}).Error)

	e := &cycleEnv{authCampaignFixture: f}
	e.status.Store(200)
	e.body.Store(`{"messages":[{"id":"wamid.cycle"}]}`)
	e.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		e.calls.Add(1)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(int(e.status.Load()))
		_, _ = rw.Write([]byte(e.body.Load().(string)))
	}))
	t.Cleanup(e.srv.Close)

	rdb := testutil.SetupTestRedis(t)
	e.w = &worker.Worker{
		Config: f.app.Config, DB: f.app.DB, Redis: rdb, Log: f.app.Log,
		WhatsApp: whatsapp.NewWithBaseURL(f.app.Log, e.srv.URL),
	}
	if rdb != nil {
		e.w.Publisher = queue.NewPublisher(rdb, f.app.Log)
	}
	return e
}

func (e *cycleEnv) metaAnswers(status int, body string) { e.status.Store(int32(status)); e.body.Store(body) }

func (e *cycleEnv) recipient(t *testing.T) models.BulkMessageRecipient {
	t.Helper()
	var r models.BulkMessageRecipient
	require.NoError(t, e.app.DB.Where("campaign_id = ?", e.campaign.ID).First(&r).Error)
	return r
}

func (e *cycleEnv) job(t *testing.T) *queue.RecipientJob {
	r := e.recipient(t)
	return &queue.RecipientJob{
		CampaignID: e.campaign.ID, RecipientID: r.ID, OrganizationID: e.org.ID,
		PhoneNumber: r.PhoneNumber, RecipientName: r.RecipientName, TemplateParams: r.TemplateParams,
	}
}

func (e *cycleEnv) attempts(t *testing.T) []models.Message {
	t.Helper()
	var ms []models.Message
	require.NoError(t, e.app.DB.Where("metadata->>'campaign_id' = ?", e.campaign.ID.String()).Order("created_at").Find(&ms).Error)
	return ms
}

func (e *cycleEnv) markCompleted(t *testing.T) {
	t.Helper()
	require.NoError(t, e.app.DB.Model(e.campaign).Update("status", models.CampaignStatusCompleted).Error)
}

func (e *cycleEnv) counts(t *testing.T) (sent, failed int) {
	t.Helper()
	var c models.BulkMessageCampaign
	require.NoError(t, e.app.DB.First(&c, e.campaign.ID).Error)
	return c.SentCount, c.FailedCount
}

// Confirmed failure -> retry -> new claim -> sent. The first attempt stays in the history
// as failed, the second is its own record, and the code is gone once it is sent.
func TestRetryCycle_ConfirmedFailureThenRetryThenSent(t *testing.T) {
	e := newCycleEnv(t)
	e.importThenProcess(t)

	e.metaAnswers(400, `{"error":{"code":131008,"message":"Required parameter is missing"}}`)
	require.NoError(t, e.w.HandleRecipientJob(t.Context(), e.job(t)))
	first := e.recipient(t)
	require.Equal(t, models.MessageStatusFailed, first.Status)
	require.Nil(t, first.MessageID, "provably not sent: no claim kept")
	require.True(t, templateutil.IsSealed(first.TemplateParams), "the code is kept for the retry")
	e.markCompleted(t)

	status, data := e.retry(t)
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	require.Len(t, e.queue.Jobs, 1)
	msgs := e.attempts(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status, "the first attempt stays in the history")
	assert.NotEmpty(t, msgs[0].ErrorMessage)
	_, failed := e.counts(t)
	assert.Zero(t, failed, "the retried recipient is not counted as failed any more")

	e.metaAnswers(200, `{"messages":[{"id":"wamid.cycle"}]}`)
	require.NoError(t, e.w.HandleRecipientJob(t.Context(), e.queue.Jobs[0]))

	got := e.recipient(t)
	assert.Equal(t, models.MessageStatusSent, got.Status)
	assert.Empty(t, got.TemplateParams, "sent: the code is cleared by the same write")
	msgs = e.attempts(t)
	require.Len(t, msgs, 2, "one record per attempt")
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
	assert.Equal(t, models.MessageStatusSent, msgs[1].Status)
	assert.EqualValues(t, 2, e.calls.Load())
	sent, failed := e.counts(t)
	assert.Equal(t, 1, sent)
	assert.Zero(t, failed)
}

// Uncertain delivery -> retry is refused -> a person releases it -> retry -> new claim ->
// sent. The uncertain attempt is history, never reset, never counted as pending.
func TestRetryCycle_UncertainThenReleasedThenRetriedAndSent(t *testing.T) {
	e := newCycleEnv(t)
	e.importThenProcess(t)

	e.metaAnswers(502, `{"error":{"code":1,"message":"temporarily unavailable"}}`)
	require.NoError(t, e.w.HandleRecipientJob(t.Context(), e.job(t)))
	r := e.recipient(t)
	require.NotNil(t, r.MessageID, "uncertain: the claim is kept")
	e.markCompleted(t)

	status, _ := e.retry(t)
	assert.Equal(t, fasthttp.StatusConflict, status, "an uncertain delivery is never retried automatically")
	assert.Empty(t, e.queue.Jobs)
	assert.Equal(t, models.MessageStatusFailed, e.attempts(t)[0].Status)

	op := e.adminOperator(t)
	require.NoError(t, authmask.ReleaseUncertain(e.app.DB, authmask.ReleaseRequest{
		RecipientID: r.ID, OperatorEmail: op.Email, Reason: "Meta log shows the message was never accepted",
	}, templateutil.OTPPolicy{}, time.Now(), false))
	assert.Equal(t, models.MessageStatusFailed, e.attempts(t)[0].Status, "releasing changes no message")

	status, data := e.retry(t)
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	require.Len(t, e.queue.Jobs, 1)
	msgs := e.attempts(t)
	require.Len(t, msgs, 1)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status, "the released attempt is not turned into a pending message")

	e.metaAnswers(200, `{"messages":[{"id":"wamid.cycle2"}]}`)
	require.NoError(t, e.w.HandleRecipientJob(t.Context(), e.queue.Jobs[0]))

	assert.Equal(t, models.MessageStatusSent, e.recipient(t).Status)
	msgs = e.attempts(t)
	require.Len(t, msgs, 2)
	assert.Equal(t, models.MessageStatusFailed, msgs[0].Status)
	assert.Equal(t, models.MessageStatusSent, msgs[1].Status)
	sent, failed := e.counts(t)
	assert.Equal(t, 1, sent)
	assert.Zero(t, failed)
}

func (e *cycleEnv) importThenProcess(t *testing.T) {
	t.Helper()
	require.Equal(t, fasthttp.StatusOK, e.importOne(t).Response.StatusCode())
	require.NoError(t, e.app.DB.Model(e.campaign).Update("status", models.CampaignStatusProcessing).Error)
}
