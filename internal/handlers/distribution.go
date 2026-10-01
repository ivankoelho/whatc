package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
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

// ErrTransferAlreadyActive means the contact already has an active attendance:
// either it existed before, or a concurrent request created it first and the
// partial unique index rejected this insert. It is a condition, not a failure.
var ErrTransferAlreadyActive = errors.New("contact already has an active transfer")

// ErrConversationOwned means the conversation belongs to another agent and the
// caller may not act on it.
var ErrConversationOwned = errors.New("conversation is assigned to another agent")

// activeTransferIndex mirrors database.ActiveTransferIndexName; duplicated as a
// literal to avoid importing the database package from handlers.
const activeTransferIndex = "idx_agent_transfers_one_active_per_contact"

// isActiveTransferConflict reports whether err is the unique violation raised
// by the one-active-transfer-per-contact index.
func isActiveTransferConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == activeTransferIndex
}

// createTransferRow inserts a new active transfer, translating the unique-index
// violation into ErrTransferAlreadyActive.
func (a *App) createTransferRow(transfer *models.AgentTransfer) error {
	if err := a.DB.Create(transfer).Error; err != nil {
		if isActiveTransferConflict(err) {
			return ErrTransferAlreadyActive
		}
		return err
	}
	return nil
}

// claimTransfer atomically assigns an unassigned, active transfer to agentID.
// The conditional UPDATE is the lock: of any number of concurrent callers
// exactly one sees RowsAffected == 1; everyone else gets claimed == false and
// must treat the transfer as taken. On success t is updated in place.
func (a *App) claimTransfer(db *gorm.DB, t *models.AgentTransfer, agentID uuid.UUID) (claimed bool, err error) {
	updates := map[string]any{"agent_id": agentID}
	sla := *t
	if sla.SLA.PickedUpAt == nil {
		a.UpdateSLAOnPickup(&sla)
		updates["picked_up_at"] = sla.SLA.PickedUpAt
		if sla.SLA.Breached && !t.SLA.Breached {
			updates["sla_breached"] = true
			updates["sla_breached_at"] = sla.SLA.BreachedAt
		}
	}

	res := db.Model(&models.AgentTransfer{}).
		Where("id = ? AND status = ? AND agent_id IS NULL", t.ID, models.TransferStatusActive).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected != 1 {
		return false, nil
	}
	t.AgentID = &agentID
	t.SLA = sla.SLA
	return true, nil
}

// moveTransfer is a compare-and-set on the responsible agent: the update only
// applies if the transfer is still active AND still assigned to `from` (nil =
// unassigned). A concurrent change makes it affect zero rows, reported as
// moved == false so the caller can answer 409 instead of silently overwriting.
func (a *App) moveTransfer(db *gorm.DB, id uuid.UUID, from *uuid.UUID, updates map[string]any) (moved bool, err error) {
	res := db.Model(&models.AgentTransfer{}).
		Where("id = ? AND status = ? AND agent_id IS NOT DISTINCT FROM ?", id, models.TransferStatusActive, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// canOverrideConversationOwner reports whether the user may act on a
// conversation owned by someone else: supervisors/admins only.
func (a *App) canOverrideConversationOwner(userID, orgID uuid.UUID) bool {
	return a.HasPermission(userID, models.ResourceTransfers, models.ActionWrite, orgID) ||
		a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID)
}

// ensureAgentOwnsConversation is the backend gate in front of every agent send.
// It runs BEFORE the message is created, so a loser of a race never sends:
//
//   - no active attendance: opens one for the sender (unique index decides races)
//   - unassigned attendance: the sender claims it atomically
//   - owned by the sender: proceeds
//   - owned by another agent: ErrConversationOwned, unless the sender holds
//     transfers:write or conversations:view_all (or allowOtherOwner, used by
//     protocol replies, which never take over the conversation)
func (a *App) ensureAgentOwnsConversation(account *models.WhatsAppAccount, contact *models.Contact, userID uuid.UUID, allowOtherOwner bool) error {
	orgID := contact.OrganizationID

	// Two passes: the second only runs when the first lost a race and the
	// state it read is stale.
	for range 2 {
		t, has := a.activeTransferFor(orgID, contact.ID)
		if !has {
			err := a.openAgentInitiatedTransfer(account, contact, userID)
			if errors.Is(err, ErrTransferAlreadyActive) {
				continue // someone else opened it first; re-read
			}
			return err
		}

		switch {
		case t.AgentID == nil:
			claimed, err := a.claimTransfer(a.DB, &t, userID)
			if err != nil {
				return err
			}
			if !claimed {
				a.auditDistribution(orgID, &userID, t.ID, distEventConflict, nil, nil, "claim on send lost the race")
				continue // taken or closed meanwhile; re-read
			}
			a.auditDistribution(orgID, &userID, t.ID, distEventClaimed, nil, &userID, "claimed by sending a message")
			a.pinCarteiraOnClaim(contact, userID)
			a.broadcastTransferAssigned(&t)
			return nil
		case *t.AgentID == userID:
			return nil
		default:
			if allowOtherOwner || a.canOverrideConversationOwner(userID, orgID) {
				return nil
			}
			a.auditDistribution(orgID, &userID, t.ID, distEventConflict, t.AgentID, nil, "send refused: conversation owned by another agent")
			return ErrConversationOwned
		}
	}
	return ErrConversationOwned
}

// pinCarteiraOnClaim applies the same relationship-manager rule as pickup and
// assignment: only when the org opted into AssignToSameAgent and the contact
// has no manager yet.
func (a *App) pinCarteiraOnClaim(contact *models.Contact, agentID uuid.UUID) {
	settings, _ := a.getChatbotSettingsCached(contact.OrganizationID, "")
	if settings == nil || !settings.AgentAssignment.AssignToSameAgent || contact.AssignedUserID != nil {
		return
	}
	a.DB.Model(contact).Update("assigned_user_id", agentID)
	a.assignOpenSalesOpportunityToAgent(a.DB, contact, agentID)
}

// sendTransferConflict answers 409 with the transfer's current owner so the
// client can refresh its state instead of showing an assignment that no longer
// exists. The body carries only ids; names come from the normal listing.
func (a *App) sendTransferConflict(r *fastglue.Request, orgID, userID, transferID uuid.UUID) error {
	a.auditDistribution(orgID, &userID, transferID, distEventConflict, nil, nil, "assignment changed concurrently")
	data := map[string]any{"transfer_id": transferID.String()}
	var t models.AgentTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).First(&t).Error; err == nil {
		data["status"] = t.Status
		if t.AgentID != nil {
			data["agent_id"] = t.AgentID.String()
		} else {
			data["agent_id"] = nil
		}
	}
	return r.SendErrorEnvelope(fasthttp.StatusConflict, "Transfer was changed by someone else", data, "")
}

// Distribution audit events, written to the existing audit log (resource type
// "transfers", resource id = the transfer) so there is a single trail.
const (
	distEventClaimed         = "claimed"          // an agent took an unassigned attendance (reply, self-assign, queue pick)
	distEventAssigned        = "assigned"         // a supervisor or the system set/changed the responsible agent
	distEventReleased        = "released"         // returned to the queue by a person
	distEventReturnedAway    = "returned_away"    // returned because the agent went away
	distEventReturnedOffline = "returned_offline" // returned because the agent stayed disconnected past the grace
	distEventAutoAssigned    = "auto_assigned"    // chosen by the distribution strategy
	distEventQueued          = "queued"           // no eligible agent: left in the queue
	distEventConflict        = "conflict"         // an attempt lost a race or was refused ownership
)

// auditDistribution records a distribution event. actor == nil means the system
// (strategy, reaper). from/to are the responsible agent before/after.
func (a *App) auditDistribution(orgID uuid.UUID, actor *uuid.UUID, transferID uuid.UUID, event string, from, to *uuid.UUID, detail string) {
	changes := []map[string]any{
		{"field": "event", "old_value": nil, "new_value": event},
		{"field": "agent_id", "old_value": uuidString(from), "new_value": uuidString(to)},
	}
	if detail != "" {
		changes = append(changes, map[string]any{"field": "detail", "old_value": nil, "new_value": detail})
	}
	actorID, actorName := uuid.Nil, "System"
	if actor != nil {
		actorID, actorName = *actor, audit.GetUserName(a.DB, *actor)
	}
	audit.LogAudit(a.DB, orgID, actorID, actorName, models.ResourceTransfers, transferID,
		models.AuditActionUpdated, nil, nil, changes...)
}

func uuidString(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// BroadcastAgentPresence tells the organization an agent connected or
// disconnected. Called from the hub listener, so it must not block the hub.
func (a *App) BroadcastAgentPresence(orgID, userID uuid.UUID, online bool) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{
		Type:    websocket.TypeAgentPresence,
		Payload: map[string]any{"user_id": userID.String(), "online": online},
	})
}

// broadcastAgentAvailability tells the organization an agent toggled
// available/away.
func (a *App) broadcastAgentAvailability(orgID, userID uuid.UUID, available bool) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{
		Type:    websocket.TypeAgentAvailability,
		Payload: map[string]any{"user_id": userID.String(), "is_available": available},
	})
}

// auditNewTransfer records how a freshly created attendance was first routed:
// opened by the agent who messaged first, chosen by the strategy, or left in
// the queue because no eligible agent was found.
func (a *App) auditNewTransfer(t *models.AgentTransfer) {
	switch {
	case t.Source == models.TransferSourceAgentInitiated && t.AgentID != nil:
		a.auditDistribution(t.OrganizationID, t.AgentID, t.ID, distEventClaimed, nil, t.AgentID, "agent-initiated attendance")
	case t.AgentID != nil && t.TransferredByUserID != nil:
		a.auditDistribution(t.OrganizationID, t.TransferredByUserID, t.ID, distEventAssigned, nil, t.AgentID, "assigned at creation")
	case t.AgentID != nil:
		a.auditDistribution(t.OrganizationID, nil, t.ID, distEventAutoAssigned, nil, t.AgentID, "chosen by the distribution strategy")
	default:
		a.auditDistribution(t.OrganizationID, nil, t.ID, distEventQueued, nil, nil, "no agent assigned; waiting in the queue")
	}
}
