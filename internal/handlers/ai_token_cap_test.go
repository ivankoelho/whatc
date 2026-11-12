package handlers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usageAt(t *testing.T, app *App, orgID uuid.UUID, feature string, tokens int, at time.Time) {
	t.Helper()
	require.NoError(t, app.DB.Create(&models.AIUsageLog{
		OrganizationID: orgID, Feature: feature, TotalTokens: tokens, Success: true, CreatedAt: at,
	}).Error)
}

func used(t *testing.T, app *App, orgID uuid.UUID, now time.Time) int64 {
	t.Helper()
	n, err := aiTokensUsed(app.DB, orgID, now)
	require.NoError(t, err)
	return n
}

func TestReserveAITokens_BelowAndAboveLimit(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	now := time.Now()
	usageAt(t, app, org.ID, aiFeatureChatbotReply, 600, now)

	r, err := app.reserveAITokens(org.ID, 1000, 400, now)
	require.NoError(t, err, "600 used + 400 reserved fits a 1000 cap exactly")
	require.NotNil(t, r)
	assert.EqualValues(t, 1000, used(t, app, org.ID, now), "the reservation counts")

	_, err = app.reserveAITokens(org.ID, 1000, 1, now)
	assert.ErrorIs(t, err, ErrAICapReached, "nothing is left")
	assert.EqualValues(t, 1000, used(t, app, org.ID, now), "a refused reservation leaves no row")
}

// The core guarantee: many calls at once cannot all pass the check.
func TestReserveAITokens_ConcurrentNeverExceeds(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	now := time.Now()

	const goroutines, per, limit = 40, 100, 1000
	var ok, refused atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := app.reserveAITokens(org.ID, limit, per, now); err == nil {
				ok.Add(1)
			} else {
				assert.ErrorIs(t, err, ErrAICapReached)
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, limit/per, ok.Load(), "exactly as many calls as the cap allows")
	assert.EqualValues(t, goroutines-limit/per, refused.Load())
	assert.LessOrEqual(t, used(t, app, org.ID, now), int64(limit))
}

// A call that is still running keeps its reservation: another call that would not fit stays refused
// for as long as the first can still be alive, and only a presumed-dead reservation expires.
func TestReserveAITokens_LongCallKeepsReservation(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	t0 := time.Now()

	_, err := app.reserveAITokens(org.ID, 1000, 900, t0)
	require.NoError(t, err)

	// 90 s later (the longest a call may run is aiCallDeadline = 2 min): still reserved
	_, err = app.reserveAITokens(org.ID, 1000, 500, t0.Add(90*time.Second))
	assert.ErrorIs(t, err, ErrAICapReached, "the in-flight call's reservation must still count")
	_, err = app.reserveAITokens(org.ID, 1000, 500, t0.Add(aiCallDeadline))
	assert.ErrorIs(t, err, ErrAICapReached, "still counted when the call reaches its deadline")

	// past the stale window the process that made it is presumed dead
	_, err = app.reserveAITokens(org.ID, 1000, 500, t0.Add(aiReservationStale+time.Second))
	assert.NoError(t, err)

	assert.Greater(t, aiReservationStale, 2*aiCallDeadline, "the stale window must stay far above the call deadline")
}

func TestReserveAITokens_IgnoresAbandonedReservationOtherMonthsAndOrgs(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	// a fixed instant inside a month, in the cap's own time zone
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, aiCapZone)

	usageAt(t, app, org.ID, aiFeatureChatbotReply, 100, now)                           // counts
	usageAt(t, app, org.ID, aiFeatureChatbotNode, 20, now)                             // counts
	usageAt(t, app, org.ID, aiFeatureQualification, 3, now)                            // counts
	usageAt(t, app, org.ID, aiFeatureReservationUnreconciled, 1, now)                  // counts
	usageAt(t, app, org.ID, aiFeatureReservation, 500, now.Add(-aiReservationStale-1)) // abandoned
	usageAt(t, app, org.ID, aiFeatureReservationClosed, 0, now)                        // closed
	usageAt(t, app, org.ID, "model_list", 9999, now)                                   // not chatbot consumption
	usageAt(t, app, org.ID, aiFeatureReplyDiscarded, 9999, now)                        // a discard is not consumption
	usageAt(t, app, org.ID, aiFeatureChatbotReply, 7777, now.AddDate(0, -1, 0))        // last month
	usageAt(t, app, org.ID, aiFeatureChatbotReply, 8888, now.AddDate(0, 1, 0))         // next month
	usageAt(t, app, other.ID, aiFeatureChatbotReply, 5555, now)                        // another organization

	assert.EqualValues(t, 100+20+3+1, used(t, app, org.ID, now))

	// the month follows São Paulo's calendar, not UTC: 22:00 on Oct 31 in São Paulo is already Nov 1 in UTC
	edge := time.Date(2026, 10, 31, 22, 0, 0, 0, aiCapZone)
	assert.Equal(t, time.UTC, edge.UTC().Location())
	usageAt(t, app, org.ID, aiFeatureChatbotReply, 50, edge)
	assert.EqualValues(t, 100+20+3+1, used(t, app, org.ID, now), "belongs to October in São Paulo")
}

// The real usage rows are written BEFORE the reservation is closed, and consumption is never
// momentarily lower than the real usage in between.
func TestSettle_UsageRecordedBeforeClose(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	now := time.Now()

	r, err := app.reserveAITokens(org.ID, 10000, 400, now)
	require.NoError(t, err)

	usageAt(t, app, org.ID, aiFeatureChatbotReply, 150, now) // the call's real usage
	// both counted for a moment: conservative, never zero
	assert.EqualValues(t, 550, used(t, app, org.ID, now))

	stop := make(chan struct{})
	var minSeen atomic.Int64
	minSeen.Store(1 << 40)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := aiTokensUsed(app.DB, org.ID, now)
			if err == nil && n < minSeen.Load() {
				minSeen.Store(n)
			}
		}
	}()
	r.Settle()
	close(stop)
	wg.Wait()

	assert.GreaterOrEqual(t, minSeen.Load(), int64(150), "consumption never dropped below the real usage")
	assert.EqualValues(t, 150, used(t, app, org.ID, now), "after Settle only the real usage counts")
	var row models.AIUsageLog
	require.NoError(t, app.DB.First(&row, "id = ?", r.id).Error)
	assert.Equal(t, aiFeatureReservationClosed, row.Feature)
	assert.Zero(t, row.TotalTokens)

	r.Settle() // idempotent
	assert.EqualValues(t, 150, used(t, app, org.ID, now))
}

// If the real usage could not be written, the reserved tokens remain consumption.
func TestSettle_UsageRecordFailureBecomesUnreconciled(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	now := time.Now()

	r, err := app.reserveAITokens(org.ID, 10000, 400, now)
	require.NoError(t, err)
	r.noteUsageFailure()
	r.Settle()

	assert.EqualValues(t, 400, used(t, app, org.ID, now), "over-counts instead of forgetting the call")
	var row models.AIUsageLog
	require.NoError(t, app.DB.First(&row, "id = ?", r.id).Error)
	assert.Equal(t, aiFeatureReservationUnreconciled, row.Feature)
}

// --- through completeAI --------------------------------------------------------------------------

func capRequest() ai.Request {
	return ai.Request{Model: "gpt-test", System: "sys", Messages: []ai.Message{{Role: ai.RoleUser, Content: "oi"}}, MaxTokens: 50}
}

func capMeta(orgID uuid.UUID) aiCallMeta {
	return aiCallMeta{Feature: aiFeatureChatbotReply, OrgID: orgID, Agent: true}
}

func openReservations(t *testing.T, app *App, orgID uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, app.DB.Model(&models.AIUsageLog{}).Where("organization_id = ? AND feature = ?", orgID, aiFeatureReservation).Count(&n).Error)
	return n
}

func TestCompleteAI_ReservesRecordsThenCloses(t *testing.T) {
	w := newWiring(t, "pronto")
	w.f.settings.AI.MonthlyTokenLimit = 100000
	req := capRequest()

	resp, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), req)
	require.NoError(t, err)
	assert.Equal(t, "pronto", resp.Text)

	assert.Zero(t, openReservations(t, w.app, w.f.org.ID), "no reservation is left open")
	var rows []models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.f.org.ID).Find(&rows).Error)
	byFeature := map[string]int{}
	for _, r := range rows {
		byFeature[r.Feature] += r.TotalTokens
	}
	assert.Equal(t, 15, byFeature[aiFeatureChatbotReply], "the real usage of the call (15 tokens from the provider)")
	assert.Zero(t, byFeature[aiFeatureReservationClosed])
	assert.EqualValues(t, 15, used(t, w.app, w.f.org.ID, time.Now()))
}

func TestCompleteAI_CapReachedDoesNotCallProvider(t *testing.T) {
	w := newWiring(t, "nunca deve ser chamado")
	w.f.settings.AI.MonthlyTokenLimit = 10 // smaller than any reservation
	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	require.ErrorIs(t, err, ErrAICapReached)
	assert.Empty(t, w.ai.bodies, "the provider must not be called")
	assert.Zero(t, openReservations(t, w.app, w.f.org.ID))

	var row models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ? AND feature = ?", w.f.org.ID, aiFeatureChatbotReply).First(&row).Error)
	assert.Equal(t, "cap_reached", row.ErrorKind)
	assert.Zero(t, row.TotalTokens)
}

func TestCompleteAI_DeadlineCancelsAndSettles(t *testing.T) {
	old := aiCallDeadline
	aiCallDeadline = 200 * time.Millisecond
	t.Cleanup(func() { aiCallDeadline = old })

	w := newWiring(t, "tarde demais")
	w.f.settings.AI.MonthlyTokenLimit = 100000
	w.ai.onCall = func(int) { time.Sleep(700 * time.Millisecond) } // a provider slower than the deadline

	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	require.Error(t, err)
	assert.Zero(t, openReservations(t, w.app, w.f.org.ID), "a cancelled call releases its reservation")
	assert.EqualValues(t, 0, used(t, w.app, w.f.org.ID, time.Now()), "nothing was spent")
}

func TestCompleteAI_ProviderErrorReleasesReservation(t *testing.T) {
	w := newWiring(t) // no scripted reply: make the provider fail instead
	w.f.settings.AI.MonthlyTokenLimit = 100000
	w.f.settings.AI.APIKey = "enc:not-decryptable" // credentials error before any provider call
	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	require.Error(t, err)
	assert.Zero(t, openReservations(t, w.app, w.f.org.ID))
}

func TestCompleteAI_NoLimitNoExtraRows(t *testing.T) {
	w := newWiring(t, "ok")
	w.f.settings.AI.MonthlyTokenLimit = 0
	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	require.NoError(t, err)

	var n int64
	require.NoError(t, w.app.DB.Model(&models.AIUsageLog{}).
		Where("organization_id = ? AND feature LIKE 'ai_reservation%'", w.f.org.ID).Count(&n).Error)
	assert.Zero(t, n, "without a cap nothing is reserved")
}

func TestCompleteAI_NonAgentCallIsNotCapped(t *testing.T) {
	w := newWiring(t, "ok")
	w.f.settings.AI.MonthlyTokenLimit = 10
	meta := capMeta(w.f.org.ID)
	meta.Agent = false // the legacy chatbot path
	_, err := w.app.completeAI(t.Context(), w.f.settings, meta, capRequest())
	require.NoError(t, err, "organizations that do not use agents behave exactly as before")
	assert.Zero(t, openReservations(t, w.app, w.f.org.ID))
}
