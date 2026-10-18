package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// isUserConnected is the live-connection answer used for presence: the shared
// Assigner rule when wired (so chat, calls and the log can never disagree), else
// the hub, else "connected" (tests and tools without a presence source).
func (a *App) isUserConnected(orgID, userID uuid.UUID) bool {
	if a.Assigner != nil {
		return a.Assigner.IsConnected(orgID, userID)
	}
	if a.WSHub != nil {
		return a.WSHub.IsUserOnline(orgID, userID)
	}
	return true
}

// liveAgentState computes the agent's state from live data: the connection and
// the persisted away toggle. The result is never taken from an event.
func (a *App) liveAgentState(orgID, userID uuid.UUID) (models.AgentState, bool) {
	var u models.User
	if err := a.DB.Select("id", "is_available").First(&u, "id = ?", userID).Error; err != nil {
		return "", false
	}
	return assignment.AgentStateOf(a.isUserConnected(orgID, userID), u.IsAvailable), true
}

// RecordPresence brings the presence log in line with the agent's live state. It
// is called after every change that can move it (a connection opening or
// closing, the away toggle) and is idempotent: repeating it with nothing changed
// writes nothing.
//
// It deliberately re-reads the state instead of trusting the event that called
// it. The hub reports each transition from its own goroutine, so an "offline"
// and the "online" right after it can run in either order; whichever runs last
// sees the truth and leaves the log correct.
func (a *App) RecordPresence(orgID, userID uuid.UUID) {
	state, ok := a.liveAgentState(orgID, userID)
	if !ok {
		return
	}
	if err := a.recordPresenceState(orgID, userID, state, time.Now()); err != nil {
		a.Log.Error("Failed to record presence", "error", err, "org_id", orgID, "user_id", userID)
	}
}

// recordPresenceState closes the open interval and opens a new one when the
// state differs. One open interval per (organization, user) is enforced by a
// partial unique index; the advisory lock only avoids the failed insert when two
// callers race.
func (a *App) recordPresenceState(orgID, userID uuid.UUID, state models.AgentState, at time.Time) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
			"presence:"+orgID.String()+":"+userID.String()).Error; err != nil {
			return err
		}

		var open models.AgentPresenceLog
		err := tx.Where("organization_id = ? AND user_id = ? AND ended_at IS NULL", orgID, userID).
			Order("started_at DESC").First(&open).Error
		switch {
		case err == nil:
			if open.State == state {
				return nil
			}
			end := at
			if end.Before(open.StartedAt) {
				end = open.StartedAt // never a negative interval
			}
			if err := tx.Model(&models.AgentPresenceLog{}).Where("id = ?", open.ID).
				Updates(map[string]any{"ended_at": end, "closed_reason": models.PresenceClosedTransition}).Error; err != nil {
				return err
			}
			at = end
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		return tx.Create(&models.AgentPresenceLog{
			OrganizationID: orgID, UserID: userID, State: state, StartedAt: at,
		}).Error
	})
}

// ResetPresenceLogOnBoot closes every open interval when the process starts. A
// restart drops every connection at once; recording that as everybody going
// offline would be false, and nobody is connected yet. Agents who reconnect open
// a fresh interval. It must run before the hub accepts connections.
func (a *App) ResetPresenceLogOnBoot() {
	res := a.DB.Model(&models.AgentPresenceLog{}).Where("ended_at IS NULL").
		Updates(map[string]any{"ended_at": time.Now(), "closed_reason": models.PresenceClosedRestart})
	if res.Error != nil {
		a.Log.Error("Failed to close presence intervals on boot", "error", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		a.Log.Info("Closed open presence intervals after restart", "count", res.RowsAffected)
	}
}

// PresenceInfo is an agent's current state and the instant it began.
type PresenceInfo struct {
	State models.AgentState
	// Since is when the current state really began. It is nil when it cannot be
	// told honestly: the log has no matching interval (the agent was in this state
	// before the log existed).
	Since *time.Time
}

// CurrentPresence returns the live state with the instant it began.
func (a *App) CurrentPresence(orgID, userID uuid.UUID) PresenceInfo {
	state, ok := a.liveAgentState(orgID, userID)
	if !ok {
		return PresenceInfo{}
	}
	return PresenceInfo{State: state, Since: a.presenceSince(orgID, map[uuid.UUID]models.AgentState{userID: state})[userID]}
}

// presenceSince finds, for each user, when their given state began, in two
// queries however many users there are. A state only has a start when the log
// holds a matching open interval; an offline agent with none falls back to the
// end of their last interval (when they were last seen, or the restart).
func (a *App) presenceSince(orgID uuid.UUID, states map[uuid.UUID]models.AgentState) map[uuid.UUID]*time.Time {
	out := make(map[uuid.UUID]*time.Time, len(states))
	if len(states) == 0 {
		return out
	}
	ids := make([]uuid.UUID, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}

	var open []models.AgentPresenceLog
	if err := a.DB.Where("organization_id = ? AND user_id IN ? AND ended_at IS NULL", orgID, ids).
		Find(&open).Error; err != nil {
		a.Log.Error("Failed to read open presence intervals", "error", err, "org_id", orgID)
		return out
	}
	needLast := make([]uuid.UUID, 0)
	hasOpen := make(map[uuid.UUID]bool, len(open))
	for _, o := range open {
		hasOpen[o.UserID] = true
		if states[o.UserID] == o.State {
			t := o.StartedAt
			out[o.UserID] = &t
		}
	}
	for id, st := range states {
		if st == models.AgentStateOffline && out[id] == nil {
			needLast = append(needLast, id)
		}
	}
	if len(needLast) > 0 {
		var last []struct {
			UserID  uuid.UUID
			EndedAt *time.Time
		}
		if err := a.DB.Model(&models.AgentPresenceLog{}).
			Select("user_id, MAX(ended_at) AS ended_at").
			Where("organization_id = ? AND user_id IN ? AND ended_at IS NOT NULL", orgID, needLast).
			Group("user_id").Scan(&last).Error; err == nil {
			for _, l := range last {
				if l.EndedAt != nil && !hasOpen[l.UserID] {
					out[l.UserID] = l.EndedAt
				}
			}
		}
	}
	return out
}
