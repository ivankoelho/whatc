package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Authorization for the internal chat (docs/superpowers/specs/
// 2026-10-16-chat-interno-nativo-design.md). Every chat operation goes through
// these functions; no channel/team/message id from the client is authority.
// Permissions (team_chat:*) grant an ACTION; only canAccessChatChannel grants
// access to the RESOURCE, and manage/delete never replace it.

// loadChatChannel returns the channel only if it belongs to orgID. A channel of
// another organization is indistinguishable from a missing one (callers answer
// 404), so ids cannot be probed across tenants.
func (a *App) loadChatChannel(orgID, channelID uuid.UUID) (*models.ChatChannel, bool) {
	var ch models.ChatChannel
	if err := a.DB.Where("id = ? AND organization_id = ?", channelID, orgID).First(&ch).Error; err != nil {
		return nil, false
	}
	return &ch, true
}

// canAccessChatChannel reports whether the user may see and post in the channel:
//   - team channel: the user is a member of the channel's team, and that team
//     belongs to the same organization;
//   - free channel / DM: the user has a chat_channel_members row.
//
// Leaving a team therefore removes access at once (history stays in the
// database), and rejoining restores it.
func (a *App) canAccessChatChannel(userID, orgID uuid.UUID, ch *models.ChatChannel) bool {
	return a.canAccessChatChannelDB(a.DB, userID, orgID, ch)
}

// canAccessChatChannelDB is canAccessChatChannel against a given connection, so a
// write can re-check access inside the transaction that holds the channel lock.
func (a *App) canAccessChatChannelDB(db *gorm.DB, userID, orgID uuid.UUID, ch *models.ChatChannel) bool {
	if ch == nil || ch.OrganizationID != orgID {
		return false
	}
	var count int64
	switch ch.Kind {
	case models.ChatChannelKindTeam:
		if ch.TeamID == nil {
			return false
		}
		db.Model(&models.TeamMember{}).
			Joins("JOIN teams ON teams.id = team_members.team_id AND teams.deleted_at IS NULL").
			Where("team_members.team_id = ? AND team_members.user_id = ? AND teams.organization_id = ?",
				*ch.TeamID, userID, orgID).
			Count(&count)
	case models.ChatChannelKindChannel, models.ChatChannelKindDM:
		db.Model(&models.ChatChannelMember{}).
			Where("channel_id = ? AND user_id = ? AND organization_id = ?", ch.ID, userID, orgID).
			Count(&count)
	default:
		return false
	}
	return count > 0
}

// canDeleteChatMessage: the author may delete their own message, and a holder of
// team_chat:delete may delete anyone's. Either way the message must be in the
// caller's organization and the caller must have access to its channel.
func (a *App) canDeleteChatMessage(userID, orgID uuid.UUID, msg *models.ChatMessage, ch *models.ChatChannel) bool {
	if msg == nil || ch == nil || msg.OrganizationID != orgID || msg.ChannelID != ch.ID {
		return false
	}
	// A system message is the channel's own record (who was added, who entered and
	// why): nobody deletes it, moderators included.
	if msg.Kind == models.ChatMessageKindSystem {
		return false
	}
	if !a.canAccessChatChannel(userID, orgID, ch) {
		return false
	}
	return msg.UserID == userID || a.HasPermission(userID, models.ResourceTeamChat, models.ActionDelete, orgID)
}
