package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Real-time events for the internal chat. The hub only filters by
// organization/user/contact and drops events when a client's buffer is full, so
// delivery is best-effort and REST is the source of truth: clients re-sync on
// reconnect. The recipient list is computed here, on the server, from the
// database at the moment of the event; nothing the client sends decides who
// receives it.

// chatRecipients returns the users who may currently access the channel and may
// read the chat (team_chat:read). Anyone who left the team, was removed, or lost
// the permission is not notified.
func (a *App) chatRecipients(orgID uuid.UUID, ch *models.ChatChannel) []uuid.UUID {
	var ids []uuid.UUID
	switch ch.Kind {
	case models.ChatChannelKindTeam:
		if ch.TeamID == nil {
			return nil
		}
		a.DB.Model(&models.TeamMember{}).
			Joins("JOIN teams ON teams.id = team_members.team_id AND teams.deleted_at IS NULL").
			Where("team_members.team_id = ? AND teams.organization_id = ?", *ch.TeamID, orgID).
			Pluck("team_members.user_id", &ids)
	case models.ChatChannelKindChannel, models.ChatChannelKindDM:
		a.DB.Model(&models.ChatChannelMember{}).
			Where("channel_id = ? AND organization_id = ?", ch.ID, orgID).
			Pluck("user_id", &ids)
	}

	out := ids[:0]
	for _, id := range ids {
		if a.HasPermission(id, models.ResourceTeamChat, models.ActionRead, orgID) {
			out = append(out, id)
		}
	}
	return out
}

// notifyChat sends a team_chat_* event to the channel's current recipients,
// optionally leaving one user out (the typist does not need their own typing).
func (a *App) notifyChat(orgID uuid.UUID, ch *models.ChatChannel, eventType string, actorID uuid.UUID, messageID *uuid.UUID, exclude uuid.UUID) {
	if a.WSHub == nil {
		return
	}
	recipients := a.chatRecipients(orgID, ch)
	to := recipients[:0]
	for _, id := range recipients {
		if id != exclude {
			to = append(to, id)
		}
	}
	if len(to) == 0 {
		return
	}
	a.WSHub.BroadcastToUsers(orgID, to, websocket.WSMessage{
		Type: eventType,
		Payload: websocket.TeamChatEventPayload{
			ChannelID: ch.ID, MessageID: messageID, UserID: actorID, At: time.Now(),
		},
	})
}

// notifyChatRead tells only the reader's own connections that the channel was
// read, so their other tabs clear the unread badge. Nobody else learns whether
// or when someone read a channel.
func (a *App) notifyChatRead(orgID, userID uuid.UUID, ch *models.ChatChannel) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.BroadcastToUser(orgID, userID, websocket.WSMessage{
		Type: websocket.TypeTeamChatRead,
		Payload: websocket.TeamChatEventPayload{
			ChannelID: ch.ID, UserID: userID, At: time.Now(),
		},
	})
}

// NotifyChatTyping tells the channel's other members that the caller is typing.
// Nothing is persisted; the client expires the indicator after a few seconds.
// Sent over REST (like the conversation typing event) so the same access check
// as every other chat route applies; the channel is never taken on trust.
func (a *App) NotifyChatTyping(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	if ch.ArchivedAt != nil {
		return a.sendChatError(r, errChatArchived, "send typing")
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatTyping, userID, nil, userID)
	r.RequestCtx.SetStatusCode(fasthttp.StatusNoContent)
	return nil
}
