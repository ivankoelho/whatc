package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Distribution events that change who is responsible for an attendance. The
// others (conflict) are refusals and do not touch ownership.
var ownershipEvents = map[string]bool{
	distEventClaimed:                   true,
	distEventAssigned:                  true,
	distEventReleased:                  true,
	distEventReturnedAway:              true,
	distEventReturnedOffline:           true,
	distEventReturnedAwayTimeout:       true,
	distEventReassignedAwayTimeout:     true,
	distEventReassignedCustomerWaiting: true,
	distEventAutoAssigned:              true,
	distEventQueued:                    true,
}

// ownershipReasonClosed ends the last interval: the attendance was closed.
const ownershipReasonClosed = "closed"

// recordOwnership appends an ownership event with the team and SLA state of the
// attendance at that moment. It is called from auditDistribution, which already
// runs at every ownership change (creation, claim, assignment, release, return,
// pick, reassignment), and from closeAttendance for the end; so there is one
// place, not one per write site. Like the audit trail it follows, a failure is
// logged and never breaks the operation that caused it.
func (a *App) recordOwnership(orgID uuid.UUID, actor *uuid.UUID, transferID uuid.UUID, reason string, from, to *uuid.UUID) {
	row := models.AgentTransferAssignment{
		OrganizationID: orgID,
		TransferID:     transferID,
		FromAgentID:    from,
		ToAgentID:      to,
		Reason:         reason,
		ActorUserID:    actor,
		OccurredAt:     time.Now(),
	}
	var t models.AgentTransfer
	if err := a.DB.Select("team_id", "escalation_level", "sla_breached").
		Where("id = ? AND organization_id = ?", transferID, orgID).First(&t).Error; err == nil {
		row.TeamID = t.TeamID
		row.EscalationLevel = t.SLA.EscalationLevel
		row.SLABreached = t.SLA.Breached
	}
	if err := a.DB.Create(&row).Error; err != nil {
		a.Log.Error("Failed to record ownership change", "error", err, "transfer_id", transferID, "reason", reason)
	}
}
