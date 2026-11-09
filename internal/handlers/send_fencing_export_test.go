package handlers

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Test-only access to the fencing clock and to the two instants at which a test may stop the owner of a send.

// SetSendFencingForTest shortens the window the owner may call the provider and the skew margin, so a test can
// cross them for real instead of waiting 70 seconds. It returns what restores the defaults.
func SetSendFencingForTest(window, margin time.Duration) (restore func()) {
	w, m := sendIntentDispatchWindow, sendIntentSkewMargin
	sendIntentDispatchWindow, sendIntentSkewMargin = window, margin
	return func() { sendIntentDispatchWindow, sendIntentSkewMargin = w, m }
}

// SetSendHooksForTest installs the hooks that run just before and just after the owner's point of no return.
func SetSendHooksForTest(before, after func()) (restore func()) {
	sendHooks.beforeDispatch, sendHooks.afterDispatch = before, after
	return func() { sendHooks.beforeDispatch, sendHooks.afterDispatch = nil, nil }
}

// DispatchSendIntentForTest is the owner's point of no return, called on its own.
func (a *App) DispatchSendIntentForTest(id uuid.UUID) (bool, error) {
	_, _, ok, err := a.dispatchSendIntent(&models.OccurrenceSendIntent{ID: id})
	return ok, err
}

// ReconcileCopyForTest runs the reconciliation of ONE row on the copy the caller holds (which may be stale).
func (a *App) ReconcileCopyForTest(intent *models.OccurrenceSendIntent, now time.Time) bool {
	return a.reconcileSendIntent(intent, now)
}

// PreflightForTest is the check that runs right before the provider request is handed to the HTTP client.
func (a *App) PreflightForTest(id uuid.UUID, started time.Time) error {
	return a.preflightSendIntent(context.Background(), id, started)
}

// SetOpenSendGuardVerifierForTest replaces the check of the open-send index (nil restores the real one), so a test can
// break the protection on a database of its own without touching the shared test database.
func (a *App) SetOpenSendGuardVerifierForTest(f func(tx *gorm.DB) error) { a.verifyOpenSendGuard = f }

// SetDBClockForTest replaces the read of the database clock (nil restores the real one).
func (a *App) SetDBClockForTest(f func() (time.Time, error)) { a.dbClockForTest = f }

// RunReconcileCycleForTest is one pass of the background loop; it reports whether it ran (false = skipped).
func (a *App) RunReconcileCycleForTest() bool {
	var last time.Time
	return a.reconcileCycle(&last)
}
