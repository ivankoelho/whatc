package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Bounds and default of the away tolerance, in minutes.
const (
	minAwayReassignMins     = 1
	maxAwayReassignMins     = 1440
	defaultAwayReassignMins = 10
)

// awayTolerance reports whether the organization keeps an away agent's
// attendances for a while instead of returning them to the queue at once, and
// for how long. It is the organization's opt-in: no default settings row, or a
// row that never turned it on, means the original behaviour (return at once).
func (a *App) awayTolerance(orgID uuid.UUID) (enabled bool, after time.Duration) {
	settings, err := a.getChatbotSettingsCached(orgID, "")
	if err != nil || settings == nil || !settings.AgentAssignment.AwayReassignEnabled {
		return false, 0
	}
	return true, awayToleranceDuration(settings.AgentAssignment.AwayReassignAfterMins)
}

// awayToleranceDuration turns the stored minutes into a duration, falling back
// to the default when the value is missing or out of range.
func awayToleranceDuration(mins int) time.Duration {
	if mins < minAwayReassignMins || mins > maxAwayReassignMins {
		mins = defaultAwayReassignMins
	}
	return time.Duration(mins) * time.Minute
}

// SweepAway releases the attendances of agents who have been away for longer
// than their organization's tolerance, and returns how many it released.
//
// The clock is the persisted away interval (user_availability_logs): it starts
// when the agent marked away, survives a restart and does not depend on whether
// the agent is still connected. Only organizations that turned the tolerance on
// are looked at, and an agent who came back before the deadline has no open away
// interval, so nothing is released. Releasing is the same idempotent move used
// everywhere else, so running it repeatedly, or beside the offline reaper or a
// supervisor reassigning by hand, cannot release a transfer twice.
//
// Only attendances in which the customer is waiting are redistributed, through the
// team's own strategy (reassignAwayTransfer). The contact's carteira is kept: the
// conversation is being redistributed, not taken from the agent for good.
func (r *PresenceReaper) SweepAway(now time.Time) int {
	var orgs []struct {
		OrganizationID        uuid.UUID
		AwayReassignAfterMins int
	}
	if err := r.app.DB.Model(&models.ChatbotSettings{}).
		Select("organization_id, away_reassign_after_mins").
		Where("whats_app_account = ? AND away_reassign_enabled = ?", "", true).
		Scan(&orgs).Error; err != nil {
		r.app.Log.Error("Away sweep failed to list organizations", "error", err)
		return 0
	}

	released := 0
	for _, org := range orgs {
		released += r.sweepAwayOrganization(org.OrganizationID, org.AwayReassignAfterMins, now)
	}
	return released
}

// SweepAwayOrganization is SweepAway for one organization: it reads that
// organization's tolerance and does nothing when it has not turned it on.
func (r *PresenceReaper) SweepAwayOrganization(orgID uuid.UUID, now time.Time) int {
	var s models.ChatbotSettings
	if err := r.app.DB.Where("organization_id = ? AND whats_app_account = ? AND away_reassign_enabled = ?",
		orgID, "", true).First(&s).Error; err != nil {
		return 0
	}
	return r.sweepAwayOrganization(orgID, s.AgentAssignment.AwayReassignAfterMins, now)
}

func (r *PresenceReaper) sweepAwayOrganization(orgID uuid.UUID, mins int, now time.Time) int {
	cutoff := now.Add(-awayToleranceDuration(mins))

	var agents []uuid.UUID
	if err := r.app.DB.Table("user_availability_logs AS l").
		Joins("JOIN users u ON u.id = l.user_id AND u.is_available = false").
		Where("l.organization_id = ? AND l.is_available = false AND l.ended_at IS NULL AND l.started_at <= ?",
			orgID, cutoff).
		Where(`EXISTS (SELECT 1 FROM agent_transfers t
			WHERE t.agent_id = l.user_id AND t.organization_id = l.organization_id
			AND t.status = ? AND t.deleted_at IS NULL)`, models.TransferStatusActive).
		Distinct().Pluck("l.user_id", &agents).Error; err != nil {
		r.app.Log.Error("Away sweep failed to list agents", "error", err, "org_id", orgID)
		return 0
	}
	released := 0
	for _, agentID := range agents {
		// Only the attendances in which the customer is waiting are redistributed. If
		// the agent is the one waiting for the customer there is nothing urgent: it
		// stays with them, and if the customer writes later it is handed over then
		// (ReevaluateOnCustomerReply).
		for _, id := range r.app.customerWaitingTransferIDs(orgID, agentID) {
			var t models.AgentTransfer
			if err := r.app.DB.Preload("Contact").First(&t, "id = ?", id).Error; err != nil {
				continue
			}
			if r.app.reassignAwayTransfer(&t, agentID, distEventReassignedAwayTimeout) {
				released++
			}
		}
	}
	return released
}
