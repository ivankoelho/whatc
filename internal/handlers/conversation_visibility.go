package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// conversationAccess is the single authorization decision for a conversation,
// computed once from the contact's state. canView and canInteract are separate
// concepts even though cycle 2 derives both from the same rule — see the spec
// section "A função central".
type conversationAccess struct {
	canView     bool
	canInteract bool
}

// authorizeConversation is the ONLY place the visibility rule lives.
//
// Precedence invariant: Contact.AssignedUserID (carteira) is consulted only
// when there is no active AgentTransfer for the contact. An active transfer
// always wins, so a queued/closed/transferred conversation is never governed
// by a stale carteira pointer.
func (a *App) authorizeConversation(userID, orgID uuid.UUID, contact *models.Contact) conversationAccess {
	settings, _ := a.getChatbotSettingsCached(orgID, "")

	// Flag off (default): preserve today's behaviour exactly — contacts:read
	// sees all, otherwise only own/assigned. Mirror scopeAssignedContact.
	if settings == nil || !settings.AgentAssignment.StrictConversationVisibility {
		if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
			return conversationAccess{canView: true, canInteract: true}
		}
		ok := a.userOwnsContact(userID, orgID, contact)
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// Strict mode.
	// Supervisors/managers/admins with view_all always pass.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return conversationAccess{canView: true, canInteract: true}
	}

	// Is there an active transfer? It is the primary authority.
	transfer, hasActive := a.activeTransferFor(orgID, contact.ID)
	if hasActive {
		switch {
		case transfer.AgentID != nil:
			ok := *transfer.AgentID == userID
			return conversationAccess{canView: ok, canInteract: ok}
		case transfer.TeamID != nil:
			ok := a.userInTeam(userID, *transfer.TeamID)
			return conversationAccess{canView: ok, canInteract: ok}
		default:
			// General queue (no team): any authorized agent.
			return conversationAccess{canView: true, canInteract: true}
		}
	}

	// No active transfer: carteira governs, if set.
	if contact.AssignedUserID != nil {
		ok := *contact.AssignedUserID == userID
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No transfer, no carteira: general pool, authorized agents.
	return conversationAccess{canView: true, canInteract: true}
}

func (a *App) canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.authorizeConversation(userID, orgID, contact).canView
}

func (a *App) canInteractWithConversation(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.authorizeConversation(userID, orgID, contact).canInteract
}

// activeTransferFor returns the contact's active transfer, if any.
func (a *App) activeTransferFor(orgID, contactID uuid.UUID) (models.AgentTransfer, bool) {
	var t models.AgentTransfer
	err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		orgID, contactID, models.TransferStatusActive).
		Order("transferred_at DESC").First(&t).Error
	if err != nil {
		return models.AgentTransfer{}, false
	}
	return t, true
}

// userInTeam reports whether the user is a member of the team.
func (a *App) userInTeam(userID, teamID uuid.UUID) bool {
	var count int64
	a.DB.Model(&models.TeamMember{}).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		Count(&count)
	return count > 0
}

// userOwnsContact mirrors the old scopeAssignedContact "mine" condition, for
// the flag-off path: the contact is assigned to the user, or an active transfer
// is assigned to them.
func (a *App) userOwnsContact(userID, orgID uuid.UUID, contact *models.Contact) bool {
	if contact.AssignedUserID != nil && *contact.AssignedUserID == userID {
		return true
	}
	var count int64
	a.DB.Model(&models.AgentTransfer{}).
		Where("organization_id = ? AND contact_id = ? AND agent_id = ? AND status = ?",
			orgID, contact.ID, userID, models.TransferStatusActive).
		Count(&count)
	return count > 0
}
