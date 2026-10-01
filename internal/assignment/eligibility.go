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
	if !a.IsConnected(orgID, userID) {
		return false
	}
	var count int64
	a.db.Model(&models.User{}).
		Where("id = ? AND is_available = ? AND is_active = ?", userID, true, true).
		Count(&count)
	return count > 0
}
