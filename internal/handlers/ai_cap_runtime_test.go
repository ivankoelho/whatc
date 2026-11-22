package handlers

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The monthly cap is enforced by the RUNTIME, not only by the API and the screen: an agent call with no limit
// and no explicit acceptance of none never reaches the provider, however the agent was activated.

func (w *wiring) setCapState(t *testing.T, limit int64, ack bool) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", w.f.org.ID).
		Updates(map[string]any{"ai_monthly_token_limit": limit, "ai_unlimited_ack": ack}).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	w.f.settings.AI.MonthlyTokenLimit, w.f.settings.AI.UnlimitedAck = limit, ack
}

// Organization whose agent was already active before the rule: limit 0, no acceptance persisted.
func TestCapRuntime_AgentActiveWithoutACapDecisionIsRefusedBeforeTheProvider(t *testing.T) {
	w := newWiring(t, "nunca deve ser pedida")
	w.setCapState(t, 0, false)

	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAICapNotConfigured)
	assert.ErrorIs(t, err, ErrAICapReached, "every caller that already hands over on 'cap reached' does the same")
	assert.Empty(t, w.ai.bodies, "the provider was never called")
	var row models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.f.org.ID).Order("created_at DESC").First(&row).Error)
	assert.Equal(t, "cap_not_configured", row.ErrorKind, "the audit says why")
	assert.False(t, row.Success)
	assert.Zero(t, row.TotalTokens)
}

// Through the whole pipeline: the customer is handed to people with a clear note, nothing is spent.
func TestCapRuntime_ThroughTheProcessorTheCustomerGoesToPeopleAndNothingIsSpent(t *testing.T) {
	w := newWiring(t, "nunca deve ser pedida", qualHappy)
	w.setCapState(t, 0, false)
	const from = "5511900006001"

	w.inbound(t, from, "oi, queria ver pisos")

	assert.Empty(t, w.ai.bodies, "no AI call at all")
	c := w.contactFor(t, from)
	tr := w.aiTransfers(t, c.ID)
	require.Len(t, tr, 1, "the customer is not left alone")
	assert.Contains(t, tr[0].Notes, "não configurado")
	sent := w.textSends()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "Vou chamar um atendente")
	assert.NotContains(t, sent[0], "Já passei", "nothing was qualified: the short text, no false promise")
}

// An explicit acceptance, persisted, allows running without a limit (and nothing is reserved).
func TestCapRuntime_ExplicitAcceptanceAllowsNoLimit(t *testing.T) {
	w := newWiring(t, "ok")
	w.setCapState(t, 0, true)

	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())

	require.NoError(t, err)
	assert.Len(t, w.ai.bodies, 1)
	var n int64
	w.app.DB.Model(&models.AIUsageLog{}).Where("organization_id = ? AND feature LIKE 'ai_reservation%'", w.f.org.ID).Count(&n)
	assert.Zero(t, n)
}

// Invalid configuration (a negative limit written behind the API's back) is "not configured", not "unlimited".
func TestCapRuntime_InvalidLimitIsNotUnlimited(t *testing.T) {
	w := newWiring(t, "x", "ok")
	w.setCapState(t, -5, false)
	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	assert.ErrorIs(t, err, ErrAICapNotConfigured)
	assert.Empty(t, w.ai.bodies)

	w.setCapState(t, -5, true) // only the explicit acceptance opens it
	_, err = w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	assert.NoError(t, err)
}

// A real limit still behaves as before (and an acceptance next to it does not widen it).
func TestCapRuntime_ALimitStillCapsEvenWithTheAcceptance(t *testing.T) {
	w := newWiring(t, "ok")
	w.setCapState(t, 1, true) // far below the estimate of one call
	_, err := w.app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), capRequest())
	require.ErrorIs(t, err, ErrAICapReached)
	assert.NotErrorIs(t, err, ErrAICapNotConfigured, "this is the cap itself")
	assert.Empty(t, w.ai.bodies)
}

// No agent, no change: calls that are not an agent's (the organization's free-text AI) are never subject to the
// rule, whatever the cap state, exactly as before agents existed.
func TestCapRuntime_OrganizationsWithoutAnAgentAreUnchanged(t *testing.T) {
	w := newWiring(t, "ok")
	w.setCapState(t, 0, false)
	meta := capMeta(w.f.org.ID)
	meta.Agent = false

	_, err := w.app.completeAI(t.Context(), w.f.settings, meta, capRequest())

	require.NoError(t, err)
	assert.Len(t, w.ai.bodies, 1)
}

// Two application instances (two database connections) calling at the same time: the reservations live in the
// database under an advisory lock, so together they never get past the limit. The provider is held open until
// every call has either passed the check or been refused, so every passing call still holds its reservation.
func TestCapRuntime_TwoInstancesNeverGetPastTheLimit(t *testing.T) {
	w := newWiring(t, "ok")
	req := capRequest()
	est := estimateAITokens(req)
	const fit, callers = 5, 24
	w.setCapState(t, est*fit+est/2, false) // room for exactly `fit` reservations

	db2, err := gorm.Open(postgres.Open(os.Getenv("TEST_DATABASE_URL")), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, _ := db2.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	// a second instance: same configuration and providers, its own connection pool
	app2 := &App{Config: w.app.Config, DB: db2, Log: w.app.Log, WhatsApp: w.app.WhatsApp, HTTPClient: w.app.HTTPClient, Redis: w.app.Redis}
	instances := []*App{w.app, app2}

	release := make(chan struct{})
	var inflight, refused atomic.Int64
	w.ai.replies = make([]string, callers)
	for i := range w.ai.replies {
		w.ai.replies[i] = "ok"
	}
	w.ai.onCall = func(int) {
		inflight.Add(1)
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(app *App) {
			defer wg.Done()
			_, err := app.completeAI(t.Context(), w.f.settings, capMeta(w.f.org.ID), req)
			if errors.Is(err, ErrAICapReached) {
				refused.Add(1)
			}
		}(instances[i%2])
	}
	deadline := time.Now().Add(30 * time.Second)
	for inflight.Load()+refused.Load() < callers && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.EqualValues(t, fit, inflight.Load(), "exactly the calls that fit were let through, across both instances")
	assert.EqualValues(t, callers-fit, refused.Load())
	assert.LessOrEqual(t, used(t, w.app, w.f.org.ID, time.Now()), est*fit+est/2, "the counted usage never passes the limit")
	close(release)
	wg.Wait()
}
