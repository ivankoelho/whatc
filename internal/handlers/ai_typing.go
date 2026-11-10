package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// The "typing…" indicator shown to the customer while an AI agent writes its answer.
//
// What was validated on a real test number (docs/homologacao/spike-digitacao/relatorio-2026-11-10.md):
// the call is accepted and renewable, the customer sees it, it goes away when the answer arrives or
// the customer writes again (the next renewal brings it back), and only the type "text" exists.
//
// The rules this file keeps:
//   - it is a courtesy and never part of the answer's path: it runs on its own goroutine, every call
//     has a short deadline, and a failure is counted and forgotten. stop() never waits for it.
//   - it starts only when the agent has already decided to answer (the caller starts it right before
//     the model call) and is shown for the customer's latest message.
//   - it stops when the answer is ready, when the generation fails, and as soon as a human takes over.
//     The Meta indicator cannot be recalled, so what is left on the phone expires by itself in at
//     most ~25 s after the last renewal; no renewal is sent after the stop.

var (
	// aiTypingRenewEvery: how often the indicator is shown again. The API's own limit is ~25 s; the spike
	// renewed every 10 s and the customer kept seeing it.
	aiTypingRenewEvery = 10 * time.Second
	// aiTypingCheckEvery: how often the loop looks for a human takeover (and for being stopped).
	aiTypingCheckEvery = 2 * time.Second
	// aiTypingCallTimeout: deadline of one call to the API.
	aiTypingCallTimeout = 5 * time.Second
)

// aiTypingMaxDuration bounds a run whose owner never stops it: the longest a model call may take.
func aiTypingMaxDuration() time.Duration { return aiCallDeadline + 30*time.Second }

// aiTypingStats is what the audit trail keeps of one run: counts and the short, redacted class of the
// last failure. Never a token, an id or any message text.
type aiTypingStats struct {
	Starts, Renewals, Failures int
	LastError                  string
	StoppedByHuman             bool
}

// startAITyping shows the indicator for wamid (the customer's latest message) and keeps it alive.
// The returned stop ends the run and returns its counters. It is safe to call stop more than once.
func (a *App) startAITyping(account *models.WhatsAppAccount, contactID uuid.UUID, wamid string) (stop func() aiTypingStats) {
	ctx, cancel := context.WithTimeout(context.Background(), aiTypingMaxDuration())
	var (
		mu    sync.Mutex
		stats aiTypingStats
	)
	record := func(first bool, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			stats.Failures++
			msg := err.Error() // already redacted by the WhatsApp client
			if len(msg) > 200 {
				msg = msg[:200]
			}
			stats.LastError = msg
			return
		}
		if first {
			stats.Starts++
		} else {
			stats.Renewals++
		}
	}
	waAccount := a.toWhatsAppAccount(account)
	send := func(first bool) {
		callCtx, cancelCall := context.WithTimeout(ctx, aiTypingCallTimeout)
		defer cancelCall()
		if ctx.Err() != nil {
			return
		}
		err := a.WhatsApp.SendTypingIndicator(callCtx, waAccount, wamid)
		if ctx.Err() != nil { // stopped while the call was in flight: not a failure worth counting
			return
		}
		if err != nil {
			a.Log.Warn("AI typing indicator failed", "error", err, "contact_id", contactID)
		}
		record(first, err)
	}

	go func() {
		send(true)
		last := time.Now()
		tick := time.NewTicker(aiTypingCheckEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if a.humanTookOver(account.OrganizationID, contactID) {
					mu.Lock()
					stats.StoppedByHuman = true
					mu.Unlock()
					cancel()
					return
				}
				if time.Since(last) >= aiTypingRenewEvery {
					send(false)
					last = time.Now()
				}
			}
		}
	}()

	var once sync.Once
	return func() aiTypingStats {
		once.Do(cancel)
		mu.Lock()
		defer mu.Unlock()
		return stats
	}
}
