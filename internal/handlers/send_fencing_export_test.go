package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
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
	_, ok, err := a.dispatchSendIntent(&models.OccurrenceSendIntent{ID: id})
	return ok, err
}

// SetSendGuardTTLForTest changes how long a good / bad check of the open-send index is remembered.
func SetSendGuardTTLForTest(ok, bad time.Duration) (restore func()) {
	o, b := sendGuardOKFor, sendGuardBadFor
	sendGuardOKFor, sendGuardBadFor = ok, bad
	return func() { sendGuardOKFor, sendGuardBadFor = o, b }
}

// ResetSendGuardForTest forgets the remembered check.
func (a *App) ResetSendGuardForTest() {
	a.sendGuard.mu.Lock()
	a.sendGuard.checkedAt, a.sendGuard.err = time.Time{}, nil
	a.sendGuard.mu.Unlock()
}
