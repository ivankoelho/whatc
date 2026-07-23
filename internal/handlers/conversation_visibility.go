package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
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
	// sees all, otherwise only own/assigned (the old assigned-contact scope).
	if settings == nil || !settings.AgentAssignment.StrictConversationVisibility {
		if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
			return conversationAccess{canView: true, canInteract: true}
		}
		ok := a.userOwnsContact(userID, orgID, contact)
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// Strict mode.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return conversationAccess{canView: true, canInteract: true}
	}

	// Active transfer is the primary authority.
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
			// Active general-queue transfer (no agent, no team): fall back to
			// the account default team, else view_all only.
			if team := a.accountDefaultTeamID(orgID, contact); team != nil {
				ok := a.userInTeam(userID, *team)
				return conversationAccess{canView: ok, canInteract: ok}
			}
			return conversationAccess{canView: false, canInteract: false}
		}
	}

	// No active transfer: carteira governs (more specific than any team).
	if contact.AssignedUserID != nil {
		ok := *contact.AssignedUserID == userID
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No carteira: effective team = flow-set team, else account default team.
	effTeam := contact.TeamID
	if effTeam == nil {
		effTeam = a.accountDefaultTeamID(orgID, contact)
	}
	if effTeam != nil {
		ok := a.userInTeam(userID, *effTeam)
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No transfer, no carteira, no team: view_all only.
	return conversationAccess{canView: false, canInteract: false}
}

func (a *App) canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.authorizeConversation(userID, orgID, contact).canView
}

// CanViewConversationByID loads the contact org-scoped and reports whether the
// user may view its conversation. Used to authorize WebSocket delivery.
func (a *App) CanViewConversationByID(userID, orgID, contactID uuid.UUID) bool {
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).First(&contact).Error; err != nil {
		return false
	}
	return a.canViewConversation(userID, orgID, &contact)
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

// accountDefaultTeamID returns the default team configured on the contact's
// WhatsApp account, or nil. Used only in strict mode as the last team signal
// before falling back to view_all-only.
func (a *App) accountDefaultTeamID(orgID uuid.UUID, contact *models.Contact) *uuid.UUID {
	if contact == nil || contact.WhatsAppAccount == "" {
		return nil
	}
	var acct models.WhatsAppAccount
	if err := a.DB.Select("default_team_id").
		Where("organization_id = ? AND name = ?", orgID, contact.WhatsAppAccount).
		First(&acct).Error; err != nil {
		return nil
	}
	return acct.DefaultTeamID
}

// scopeVisibleConversations is the SQL translation of authorizeConversation.canView
// (see spec §"A função central"). It must return exactly the contacts for which
// canViewConversation is true — TestVisibilityScopeMatchesFunction guards that.
// It is the single scope now used at every listing/read/action site.
func (a *App) scopeVisibleConversations(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	settings, _ := a.getChatbotSettingsCached(orgID, "")

	// Flag off: preserve the old assigned-contact scope exactly.
	if settings == nil || !settings.AgentAssignment.StrictConversationVisibility {
		if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
			return query
		}
		return query.Where("assigned_user_id = ? OR id IN (?)",
			userID,
			a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
				Where("agent_id = ? AND organization_id = ? AND status = ?",
					userID, orgID, models.TransferStatusActive),
		)
	}

	// Strict: view_all sees everything.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return query
	}

	// A contact is visible when, considering only its LATEST active transfer:
	//   - that transfer is assigned to the user, OR
	//   - that transfer is a team queue whose team the user belongs to, OR
	//   - that transfer is the general queue (no team), OR
	//   - there is NO active transfer and (carteira == user OR no carteira).
	//
	// "latest active transfer" mirrors activeTransferFor's Order(transferred_at DESC).
	// Expressed as: the contact has NO active transfer OTHER than ones the user
	// may see, is a delicate SQL. Simpler and provably equivalent to the function:
	// a contact is visible iff it has an active transfer the user may see, OR it
	// has no active transfer and the carteira rule passes.

	activeSub := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive)

	// contact_ids with an active transfer the user MAY see.
	visibleTransferSub := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive).
		Where(`
			agent_id = ?
			OR (agent_id IS NULL AND team_id IS NULL)
			OR (agent_id IS NULL AND team_id IN (?))
		`,
			userID,
			a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID),
		)

	return query.Where(`
		id IN (?)
		OR (id NOT IN (?) AND (assigned_user_id IS NULL OR assigned_user_id = ?))
	`,
		visibleTransferSub,
		activeSub,
		userID,
	)
}

// userOwnsContact mirrors the old assigned-contact "mine" condition, for
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
