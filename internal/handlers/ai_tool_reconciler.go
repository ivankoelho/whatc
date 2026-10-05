package handlers

import (
	"context"
	"time"
)

// AIToolReconciler closes the AI confirmations a crash left in "confirmed": the customer's tap was
// consumed but the action's outcome was never recorded. It is recovery infrastructure, not a second
// way to authorize anything: the Confirmer re-authorizes each request, refuses an authorization older
// than the confirmation validity and looks at what already exists before it acts, and the customer's
// button is never involved. It sweeps periodically, like the PresenceReaper, so it also covers a
// server restart. An interval of 0 turns it off.
type AIToolReconciler struct {
	app    *App
	stopCh chan struct{}
}

func NewAIToolReconciler(app *App) *AIToolReconciler {
	return &AIToolReconciler{app: app, stopCh: make(chan struct{})}
}

// Interval is how often it sweeps (ai_tools.reconcile_interval_seconds, default 30 s; 0 = off).
func (r *AIToolReconciler) Interval() time.Duration {
	if r.app.Config == nil {
		return 0
	}
	return r.app.Config.AITools.ReconcileInterval()
}

func (r *AIToolReconciler) Start(ctx context.Context) {
	every := r.Interval()
	if every <= 0 {
		r.app.Log.Info("AI tool reconciler is off (reconcile_interval_seconds = 0)")
		return
	}
	r.app.Log.Info("AI tool reconciler started", "interval", every)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-t.C:
			r.Sweep(ctx)
		}
	}
}

func (r *AIToolReconciler) Stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
}

// Sweep runs one pass and returns how many confirmations it worked on. It needs the server secret
// (without it no confirmation can exist) and nothing else: it must also run with the switches off,
// because a request left "confirmed" has to be closed, and the re-authorization then denies it.
func (r *AIToolReconciler) Sweep(ctx context.Context) int {
	if !r.app.aiToolConfirmationsReady() {
		return 0
	}
	return r.app.aiToolConfirmer().Reconcile(ctx)
}
