package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// The single definition of "who is waiting for whom" in an attendance, used by
// the reassignment and by the agent analytics so the two can never disagree.
//
// It looks at two instants of the contact's messages, not at where the attendance
// began: the customer's last incoming message, and the last reply sent by a PERSON
// (outgoing with sent_by_user_id; the chatbot does not answer for an agent). The
// customer is waiting for the agent when the last incoming message is newer than
// the last agent reply, or there is no agent reply at all. Not bounded by the start
// of the attendance on purpose: the message that made the customer ask for a human
// is saved just before the transfer exists, and that customer is waiting.
//
// All three fragments need the attendance aliased as t and the lateral joined.
const (
	waitingLateralSQL = `LEFT JOIN LATERAL (
    SELECT
        (SELECT m.created_at FROM messages m
          WHERE m.organization_id = t.organization_id AND m.contact_id = t.contact_id
            AND m.direction = 'incoming' AND m.deleted_at IS NULL
          ORDER BY m.created_at DESC LIMIT 1) AS last_in,
        (SELECT m.created_at FROM messages m
          WHERE m.organization_id = t.organization_id AND m.contact_id = t.contact_id
            AND m.direction = 'outgoing' AND m.sent_by_user_id IS NOT NULL AND m.deleted_at IS NULL
          ORDER BY m.created_at DESC LIMIT 1) AS last_agent
) lm ON true`

	// The customer waits for the agent.
	customerWaitingCond = `(lm.last_in IS NOT NULL AND (lm.last_agent IS NULL OR lm.last_in > lm.last_agent))`
	// The agent waits for the customer: the agent replied last.
	agentWaitingCond = `(lm.last_agent IS NOT NULL AND (lm.last_in IS NULL OR lm.last_agent >= lm.last_in))`
)

// customerWaitingTransferIDs returns the agent's active attendances in which the
// customer is waiting for a reply.
func (a *App) customerWaitingTransferIDs(orgID, agentID uuid.UUID) []uuid.UUID {
	var ids []uuid.UUID
	if err := a.DB.Table("agent_transfers AS t").
		Joins(waitingLateralSQL).
		Where("t.organization_id = ? AND t.agent_id = ? AND t.status = ? AND t.deleted_at IS NULL AND "+customerWaitingCond,
			orgID, agentID, models.TransferStatusActive).
		Pluck("t.id", &ids).Error; err != nil {
		a.Log.Error("Failed to list attendances waiting for the agent", "error", err, "agent_id", agentID)
		return nil
	}
	return ids
}

// isCustomerWaiting reports whether the customer is waiting for a reply in one attendance.
func (a *App) isCustomerWaiting(transferID uuid.UUID) bool {
	var n int64
	if err := a.DB.Table("agent_transfers AS t").
		Joins(waitingLateralSQL).
		Where("t.id = ? AND "+customerWaitingCond, transferID).
		Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}
