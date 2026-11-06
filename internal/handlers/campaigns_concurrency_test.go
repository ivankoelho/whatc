package handlers_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// M2: concurrent Start / Retry requests must enqueue each recipient exactly once.

func runConcurrently(t *testing.T, n int, call func() int) (statuses []int) {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code := call()
			mu.Lock()
			statuses = append(statuses, code)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}

func countStatus(statuses []int, code int) int {
	n := 0
	for _, s := range statuses {
		if s == code {
			n++
		}
	}
	return n
}

func jobRecipientIDs(f *authCampaignFixture) map[uuid.UUID]int {
	seen := map[uuid.UUID]int{}
	for _, j := range f.queue.Jobs {
		seen[j.RecipientID]++
	}
	return seen
}

func TestStartCampaign_ConcurrentStartsEnqueueEachRecipientOnce(t *testing.T) {
	for _, auth := range []bool{false, true} {
		name := "normal"
		if auth {
			name = "authentication"
		}
		t.Run(name, func(t *testing.T) {
			var f *authCampaignFixture
			if auth {
				f = newAuthCampaign(t, models.CampaignStatusDraft)
			} else {
				f = newNormalCampaign(t, models.CampaignStatusDraft)
			}
			for i := 0; i < 3; i++ {
				createTestRecipient(t, f.app, f.campaign.ID, "+55119000000"+string(rune('1'+i)), models.MessageStatusPending)
			}

			statuses := runConcurrently(t, 8, func() int {
				req := testutil.NewJSONRequest(t, map[string]any{})
				testutil.SetAuthContext(req, f.org.ID, f.user.ID)
				testutil.SetPathParam(req, "id", f.campaign.ID.String())
				_ = f.app.StartCampaign(req)
				return testutil.GetResponseStatusCode(req)
			})

			assert.Equal(t, 1, countStatus(statuses, fasthttp.StatusOK), "exactly one request starts the campaign: %v", statuses)
			assert.Equal(t, 7, countStatus(statuses, fasthttp.StatusConflict)+countStatus(statuses, fasthttp.StatusBadRequest),
				"the others are refused (409 when they lose the race, 400 when they see the new status): %v", statuses)
			ids := jobRecipientIDs(f)
			assert.Len(t, ids, 3)
			for id, n := range ids {
				assert.Equal(t, 1, n, "recipient %s queued %d times", id, n)
			}
		})
	}
}

func TestRetryFailed_ConcurrentRetriesEnqueueEachFailedRecipientOnce(t *testing.T) {
	f := newNormalCampaign(t, models.CampaignStatusCompleted)
	for i := 0; i < 2; i++ {
		createTestRecipient(t, f.app, f.campaign.ID, "+55119100000"+string(rune('1'+i)), models.MessageStatusFailed)
	}

	statuses := runConcurrently(t, 8, func() int {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		_ = f.app.RetryFailed(req)
		return testutil.GetResponseStatusCode(req)
	})

	assert.Equal(t, 1, countStatus(statuses, fasthttp.StatusOK), "one retry wins: %v", statuses)
	ids := jobRecipientIDs(f)
	require.Len(t, ids, 2)
	for id, n := range ids {
		assert.Equal(t, 1, n, "recipient %s queued %d times", id, n)
	}
	var pending int64
	require.NoError(t, f.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ? AND status = ?", f.campaign.ID, models.MessageStatusPending).Count(&pending).Error)
	assert.Equal(t, int64(2), pending)
}

func TestRetryFailed_AuthenticationKeepsSealedParamsAndRetriesOnce(t *testing.T) {
	f := newAuthCampaign(t, models.CampaignStatusCompleted)
	r := createTestRecipient(t, f.app, f.campaign.ID, "+5511920000001", models.MessageStatusFailed)
	require.NoError(t, f.app.DB.Model(r).UpdateColumn("template_params", models.JSONB{"1": campaignOTP}).Error)

	statuses := runConcurrently(t, 4, func() int {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, f.org.ID, f.user.ID)
		testutil.SetPathParam(req, "id", f.campaign.ID.String())
		_ = f.app.RetryFailed(req)
		return testutil.GetResponseStatusCode(req)
	})

	assert.Equal(t, 1, countStatus(statuses, fasthttp.StatusOK), "%v", statuses)
	require.Len(t, f.queue.Jobs, 1)
	assert.NotContains(t, string(mustJSON(t, f.queue.Jobs[0])), campaignOTP, "the job carries the sealed value, not the code")
	assert.NotContains(t, f.raw(t, r.ID), campaignOTP)
}
