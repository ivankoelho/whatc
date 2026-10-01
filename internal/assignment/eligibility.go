package assignment

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// PresenceFunc reports whether a user currently has a live realtime
// connection (WebSocket) in the organization. In production it is backed by
// websocket.Hub.IsUserOnline, whose ping/pong deadline already drops stale
// connections, so a lost session stops counting as present on its own.
type PresenceFunc func(orgID, userID uuid.UUID) bool

// SetPresence injects the presence source. It must be called once at startup,
// before the Assigner serves requests. With no presence source (unit tests,
// tools) every user counts as connected and only the persisted rules apply.
func (a *Assigner) SetPresence(fn PresenceFunc) {
	a.presence = fn
}

// IsConnected reports whether the user is currently connected. It is
// presence only: availability (the manual away toggle) is a separate concept.
func (a *Assigner) IsConnected(orgID, userID uuid.UUID) bool {
	if a.presence == nil {
		return true
	}
	return a.presence(orgID, userID)
}

// IsAgentEligible is the single rule that decides whether a user may receive a
// new attendance or call: active, marked available AND connected. Chat and
// call distribution both go through it so the two cannot drift apart.
func (a *Assigner) IsAgentEligible(orgID, userID uuid.UUID) bool {
	return len(a.FilterEligible(orgID, []uuid.UUID{userID})) == 1
}

// FilterEligible returns the subset of userIDs that satisfy the eligibility
// rule. It is the one place the rule is evaluated (IsAgentEligible and the
// team strategies both use it), so the persisted conditions and the presence
// condition can never be changed in one path and forgotten in another.
func (a *Assigner) FilterEligible(orgID uuid.UUID, userIDs []uuid.UUID) []uuid.UUID {
	if len(userIDs) == 0 {
		return nil
	}

	var eligible []uuid.UUID
	a.db.Model(&models.User{}).
		Where("id IN ? AND is_available = ? AND is_active = ?", userIDs, true, true).
		Pluck("id", &eligible)

	if a.presence == nil {
		return eligible
	}
	connected := eligible[:0:0]
	for _, id := range eligible {
		if a.presence(orgID, id) {
			connected = append(connected, id)
		}
	}
	return connected
}
