package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// DefaultPresenceGrace is how long an agent may stay disconnected before the
// attendances assigned to them go back to the queue. Long enough to survive a
// page refresh, a network blip or a reconnect; short enough that a closed
// laptop does not hold customers hostage.
const DefaultPresenceGrace = 60 * time.Second

type agentKey struct{ org, user uuid.UUID }

// PresenceReaper returns an agent's attendances to the queue once they have
// been disconnected for longer than the grace period.
//
// Disconnected agents stop being eligible for NEW distribution immediately
// (see assignment.IsAgentEligible); the reaper only decides when ATTENDANCES
// ALREADY ASSIGNED to them are released. Reconnecting inside the window keeps
// everything. It works by periodic sweep rather than per-event timers so it
// also covers a server restart: after a restart nobody is connected yet, so
// the first sweep starts the clock for every assigned agent and anyone who
// reconnects within the grace keeps their attendances.
type PresenceReaper struct {
	app   *App
	grace time.Duration
	every time.Duration

	// isConnected is injectable for tests; defaults to the Assigner rule.
	isConnected func(orgID, userID uuid.UUID) bool

	mu           sync.Mutex
	disconnected map[agentKey]time.Time // when we first saw the agent disconnected
	stopCh       chan struct{}
}

func NewPresenceReaper(app *App, grace, every time.Duration) *PresenceReaper {
	r := &PresenceReaper{
		app:          app,
		grace:        grace,
		every:        every,
		disconnected: make(map[agentKey]time.Time),
		stopCh:       make(chan struct{}),
	}
	r.isConnected = func(orgID, userID uuid.UUID) bool {
		if app.Assigner != nil {
			return app.Assigner.IsConnected(orgID, userID)
		}
		return app.WSHub != nil && app.WSHub.IsUserOnline(orgID, userID)
	}
	return r
}

// OnPresenceChange is the hub listener. It only keeps the disconnect clock
// accurate; releasing is the sweep's job. Reconnecting clears the clock.
func (r *PresenceReaper) OnPresenceChange(orgID, userID uuid.UUID, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := agentKey{orgID, userID}
	if online {
		delete(r.disconnected, k)
		return
	}
	// Confirm: a quick reconnect may already have re-registered.
	if !r.isConnected(orgID, userID) {
		if _, seen := r.disconnected[k]; !seen {
			r.disconnected[k] = time.Now()
		}
	}
}

func (r *PresenceReaper) Start(ctx context.Context) {
	r.app.Log.Info("Presence reaper started", "grace", r.grace, "interval", r.every)
	t := time.NewTicker(r.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case now := <-t.C:
			r.Sweep(now)
		}
	}
}

func (r *PresenceReaper) Stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
}

// Sweep releases attendances of agents disconnected for at least the grace
// period and returns how many transfers it released. Safe to call repeatedly:
// release is idempotent (see ReturnAgentTransfersToQueue).
func (r *PresenceReaper) Sweep(now time.Time) int {
	var assigned []struct {
		OrganizationID uuid.UUID
		AgentID        uuid.UUID
	}
	if err := r.app.DB.Model(&models.AgentTransfer{}).
		Select("DISTINCT organization_id, agent_id").
		Where("status = ? AND agent_id IS NOT NULL", models.TransferStatusActive).
		Scan(&assigned).Error; err != nil {
		r.app.Log.Error("Presence sweep failed to list assigned agents", "error", err)
		return 0
	}

	released := 0
	live := make(map[agentKey]struct{}, len(assigned))
	for _, row := range assigned {
		k := agentKey{row.OrganizationID, row.AgentID}
		live[k] = struct{}{}

		if r.isConnected(k.org, k.user) {
			r.mu.Lock()
			delete(r.disconnected, k)
			r.mu.Unlock()
			continue
		}

		r.mu.Lock()
		since, seen := r.disconnected[k]
		if !seen {
			r.disconnected[k] = now
			since = now
		}
		r.mu.Unlock()

		if now.Sub(since) < r.grace {
			continue
		}
		// Re-check right before acting: the agent may have just reconnected.
		if r.isConnected(k.org, k.user) {
			continue
		}
		released += r.app.ReturnAgentTransfersToQueue(k.user, k.org)
		r.mu.Lock()
		delete(r.disconnected, k)
		r.mu.Unlock()
	}

	// Forget agents with nothing assigned: nothing left to release.
	r.mu.Lock()
	for k := range r.disconnected {
		if _, ok := live[k]; !ok {
			delete(r.disconnected, k)
		}
	}
	r.mu.Unlock()

	return released
}
