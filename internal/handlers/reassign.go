package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/models"
)

// reassignAwayTransfer takes an attendance away from an agent who has been away
// past the organization's tolerance and hands it to the team's own distribution.
//
// It does not choose anyone itself: the team's strategy (round robin, load
// balanced, manual) picks among the agents who are eligible right now, through the
// same Assigner every other assignment uses, so away, offline, inactive and the
// agent being replaced are all excluded by the one rule. When nobody is eligible,
// or the strategy is manual, or the attendance has no team, the attendance goes
// back to the queue of the SAME team; it keeps its original place in line
// (the queue is FIFO by when it entered), so it is picked before newer ones.
//
// The move is a compare-and-set on the agent being replaced (moveTransfer): if a
// supervisor reassigned it, the agent came back and took it, or another sweep got
// there first, nothing is overwritten and false is returned. The contact's carteira
// is not touched: the conversation is being redistributed, the relationship stays.
// toAgentEvent is the audit reason used when a new agent receives it.
//
// awayAgent is the agent this attendance was selected for. The transfer is loaded
// after the selection, so it may already belong to someone else (another sweep
// moved it a moment ago); acting on whoever owns it now would take it from a
// colleague who is not away. It is only moved when its owner still is awayAgent.
func (a *App) reassignAwayTransfer(t *models.AgentTransfer, awayAgent uuid.UUID, toAgentEvent string) bool {
	if t.AgentID == nil || *t.AgentID != awayAgent {
		return false
	}
	from := awayAgent

	var to *uuid.UUID
	if t.TeamID != nil && a.Assigner != nil {
		to = a.Assigner.AssignToTeam(*t.TeamID, t.OrganizationID, []uuid.UUID{from}, assignment.ChatLoadCounter)
	}

	moved, err := a.moveTransfer(a.DB, t.ID, &from, map[string]any{"agent_id": to})
	if err != nil {
		a.Log.Error("Failed to reassign attendance of an away agent", "error", err, "transfer_id", t.ID)
		return false
	}
	if !moved {
		return false // changed by someone else in the meantime
	}

	event := distEventReturnedAwayTimeout
	if to != nil {
		event = toAgentEvent
	}
	a.auditDistribution(t.OrganizationID, nil, t.ID, event, &from, to, "")
	t.AgentID = to
	a.broadcastTransferAssigned(t)
	if to != nil {
		a.dispatchTransferAssignedWebhook(t, *to)
	}
	return true
}

// dispatchTransferAssignedWebhook tells integrations an attendance now has a new
// responsible agent, the same event a manual assignment sends. A return to the
// queue sends none, as before.
func (a *App) dispatchTransferAssignedWebhook(t *models.AgentTransfer, agentID uuid.UUID) {
	idStr := agentID.String()
	var agentName *string
	var agent models.User
	if a.DB.Where("id = ?", agentID).First(&agent).Error == nil {
		agentName = &agent.FullName
	}
	var contactPhone, contactName string
	if t.Contact != nil {
		contactPhone, contactName = t.Contact.PhoneNumber, t.Contact.ProfileName
	}
	a.DispatchWebhook(t.OrganizationID, models.WebhookEventTransferAssigned, TransferEventData{
		TransferID:      t.ID.String(),
		ContactID:       t.ContactID.String(),
		ContactPhone:    contactPhone,
		ContactName:     contactName,
		Source:          t.Source,
		AgentID:         &idStr,
		AgentName:       agentName,
		WhatsAppAccount: t.WhatsAppAccount,
	})
}

// awayLongerThan reports whether the agent has been away (an open away interval)
// for at least the given time at the given instant.
func (a *App) awayLongerThan(orgID, agentID uuid.UUID, tolerance time.Duration, now time.Time) bool {
	var n int64
	if err := a.DB.Table("user_availability_logs AS l").
		Joins("JOIN users u ON u.id = l.user_id AND u.is_available = false").
		Where("l.organization_id = ? AND l.user_id = ? AND l.is_available = false AND l.ended_at IS NULL AND l.started_at <= ?",
			orgID, agentID, now.Add(-tolerance)).
		Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// ReevaluateOnCustomerReply is the other half of the away tolerance: an
// attendance that stayed with an away agent because the customer was not waiting
// becomes urgent the moment the customer writes. If the agent has already been
// away longer than the organization's tolerance, it is handed to the team's
// distribution right then instead of waiting for the next sweep. Before the
// deadline nothing happens: the ordinary sweep will pick it up if the agent is
// still away when it expires.
//
// It is called after the inbound message is saved, so "the customer is waiting"
// is read from the same definition the sweep uses, and it is cheap for the common
// case: an organization that did not opt in costs one cached settings read.
func (a *App) ReevaluateOnCustomerReply(orgID, contactID uuid.UUID) {
	tolerant, tolerance := a.awayTolerance(orgID)
	if !tolerant {
		return
	}
	t, has := a.activeTransferFor(orgID, contactID)
	if !has || t.AgentID == nil {
		return
	}
	if !a.awayLongerThan(orgID, *t.AgentID, tolerance, time.Now()) {
		return
	}
	if !a.isCustomerWaiting(t.ID) {
		return
	}
	if a.reassignAwayTransfer(&t, *t.AgentID, distEventReassignedCustomerWaiting) {
		a.Log.Info("Reassigned attendance after the customer wrote to an away agent",
			"transfer_id", t.ID, "contact_id", contactID)
	}
}
