package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// isAgentEligible is the backend's single answer to "may this user receive a
// new attendance right now?": active, marked available AND connected. It
// delegates to the shared Assigner rule so chat and call distribution cannot
// diverge; the frontend never decides this.
func (a *App) isAgentEligible(orgID, agentID uuid.UUID) bool {
	if a.Assigner != nil {
		return a.Assigner.IsAgentEligible(orgID, agentID)
	}
	// No Assigner wired (tests, tooling): persisted rules only.
	var count int64
	a.DB.Model(&models.User{}).
		Where("id = ? AND is_available = ? AND is_active = ?", agentID, true, true).
		Count(&count)
	return count > 0
}

// agentIneligibleReason explains why an explicitly chosen agent cannot receive
// an attendance, or returns "" when they can. Away and offline are reported
// separately because they are different states with different remedies.
func (a *App) agentIneligibleReason(orgID uuid.UUID, agent *models.User) string {
	if !agent.IsActive || !agent.IsAvailable {
		return "Agent is currently away"
	}
	if a.Assigner != nil && !a.Assigner.IsConnected(orgID, agent.ID) {
		return "Agent is currently offline"
	}
	return ""
}
