package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Reactions and replies to chat messages. Reactions use a fixed palette of
// ASCII codes (the frontend draws the glyphs): no free text reaches the column.

const (
	chatMaxReactionKinds = 20  // distinct emojis on one message
	chatSnippetRunes     = 140 // length of the quoted excerpt of a replied message
	chatReactionNames    = 5   // names listed per emoji
)

var chatReactionPalette = map[string]bool{
	"thumbs_up": true, "heart": true, "laugh": true, "wow": true, "sad": true, "pray": true,
	"check": true, "eyes": true, "fire": true, "party": true, "thinking": true, "clap": true,
}

var (
	errChatReplyGone   = &chatHTTPError{http.StatusBadRequest, "The message you are replying to is gone", "reply_target_gone"}
	errChatBadReaction = &chatHTTPError{http.StatusBadRequest, "Unknown reaction", "bad_reaction"}
	errChatTooManyKind = &chatHTTPError{http.StatusConflict, "Too many different reactions on this message", "too_many_reactions"}
)

// ChatReplyTo is the quote shown above a reply. A deleted original carries no
// author and no text.
type ChatReplyTo struct {
	ID       uuid.UUID `json:"id"`
	// AttachmentKind/Name describe the first file of the original, so a quote of a
	// message without text can still say what it was ("image", "audio", "file").
	AttachmentKind string `json:"attachment_kind,omitempty"`
	AttachmentName string `json:"attachment_name,omitempty"`
	UserID   uuid.UUID `json:"user_id,omitempty"`
	UserName string    `json:"user_name"`
	Snippet  string    `json:"snippet"`
	Deleted  bool      `json:"deleted"`
}

// ChatReactionSummary is one emoji on one message.
type ChatReactionSummary struct {
	Code  string   `json:"code"`
	Count int      `json:"count"`
	Mine  bool     `json:"mine"`
	Names []string `json:"names"`
}

func chatSnippet(body string) string {
	s := strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(s) <= chatSnippetRunes {
		return s
	}
	return string([]rune(s)[:chatSnippetRunes]) + "…"
}

// chatMessageResponses turns a page of messages into responses with author names,
// reactions and reply quotes, using a constant number of queries however many
// messages there are (names, reactions, quoted messages).
func (a *App) chatMessageResponses(viewerID uuid.UUID, msgs []models.ChatMessage) []ChatMessageResponse {
	out := make([]ChatMessageResponse, len(msgs))
	if len(msgs) == 0 {
		return out
	}
	msgIDs := make([]uuid.UUID, len(msgs))
	nameIDs := make([]uuid.UUID, 0, len(msgs))
	var parentIDs []uuid.UUID
	for i, m := range msgs {
		msgIDs[i] = m.ID
		nameIDs = append(nameIDs, m.UserID)
		if m.ParentID != nil {
			parentIDs = append(parentIDs, *m.ParentID)
		}
	}

	var reactions []models.ChatMessageReaction
	a.DB.Where("message_id IN ?", msgIDs).Order("created_at ASC, id ASC").Find(&reactions)
	for _, rc := range reactions {
		nameIDs = append(nameIDs, rc.UserID)
	}

	parents := map[uuid.UUID]models.ChatMessage{}
	if len(parentIDs) > 0 {
		var ps []models.ChatMessage
		// Unscoped: a deleted original is still known, as "removed".
		a.DB.Unscoped().Where("id IN ?", parentIDs).Find(&ps)
		for _, p := range ps {
			parents[p.ID] = p
			if !p.DeletedAt.Valid {
				nameIDs = append(nameIDs, p.UserID)
			}
		}
	}
	names := a.chatUserNames(nameIDs)

	// Files of these messages and of the quoted ones: one query for both.
	fileOwners := append(append([]uuid.UUID{}, msgIDs...), parentIDs...)
	files := a.chatAttachmentsFor(fileOwners)

	type agg struct {
		order []string
		by    map[string]*ChatReactionSummary
	}
	perMsg := map[uuid.UUID]*agg{}
	for _, rc := range reactions {
		g := perMsg[rc.MessageID]
		if g == nil {
			g = &agg{by: map[string]*ChatReactionSummary{}}
			perMsg[rc.MessageID] = g
		}
		s := g.by[rc.Code]
		if s == nil {
			s = &ChatReactionSummary{Code: rc.Code, Names: []string{}}
			g.by[rc.Code] = s
			g.order = append(g.order, rc.Code)
		}
		s.Count++
		if rc.UserID == viewerID {
			s.Mine = true
		}
		if len(s.Names) < chatReactionNames {
			s.Names = append(s.Names, names[rc.UserID])
		}
	}

	for i, m := range msgs {
		out[i] = chatMessageToResponse(m, names)
		out[i].Reactions = []ChatReactionSummary{}
		out[i].Attachments = []ChatAttachmentResponse{}
		for _, f := range files[m.ID] {
			out[i].Attachments = append(out[i].Attachments, chatAttachmentResponse(f))
		}
		if g := perMsg[m.ID]; g != nil {
			for _, code := range g.order {
				out[i].Reactions = append(out[i].Reactions, *g.by[code])
			}
		}
		if m.ParentID != nil {
			rt := &ChatReplyTo{ID: *m.ParentID, Deleted: true}
			if p, ok := parents[*m.ParentID]; ok && !p.DeletedAt.Valid && p.ChannelID == m.ChannelID {
				rt = &ChatReplyTo{ID: p.ID, UserID: p.UserID, UserName: names[p.UserID], Snippet: chatSnippet(p.Body)}
				if pf := files[p.ID]; len(pf) > 0 {
					rt.AttachmentKind, rt.AttachmentName = pf[0].Kind, pf[0].Name
				}
			}
			out[i].ReplyTo = rt
		}
	}
	return out
}

// GetChatMessage returns one message in the list format; clients call it after a
// reaction event, which carries ids only.
func (a *App) GetChatMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	msg, _, ok := a.chatMessageFor(r, userID, orgID)
	if !ok {
		return nil
	}
	return r.SendEnvelope(a.chatMessageResponses(userID, []models.ChatMessage{*msg})[0])
}

func (a *App) PutChatReaction(r *fastglue.Request) error    { return a.changeChatReaction(r, true) }
func (a *App) DeleteChatReaction(r *fastglue.Request) error { return a.changeChatReaction(r, false) }

// changeChatReaction adds (idempotent) or removes (idempotent) the caller's
// reaction. Reacting is a write like sending: it needs team_chat:write, access to
// the channel, and an unarchived channel, all re-checked under the channel's
// FOR SHARE lock so archiving cannot slip in between.
func (a *App) changeChatReaction(r *fastglue.Request, add bool) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	code, _ := r.RequestCtx.UserValue("code").(string)
	if !chatReactionPalette[code] {
		return a.sendChatError(r, errChatBadReaction, "react")
	}
	msg, ch, ok := a.chatMessageFor(r, userID, orgID)
	if !ok {
		return nil
	}
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessChatChannelDB(tx, userID, orgID, locked) {
			return errChatNotFound
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		// Still there, and not a system record (re-read: it may have been deleted meanwhile).
		var cur models.ChatMessage
		if err := tx.Where("id = ? AND channel_id = ?", msg.ID, locked.ID).First(&cur).Error; err != nil ||
			cur.Kind == models.ChatMessageKindSystem {
			return errChatNotFound
		}
		if !add {
			return tx.Where("message_id = ? AND user_id = ? AND code = ?", msg.ID, userID, code).
				Delete(&models.ChatMessageReaction{}).Error
		}
		var kinds int64
		tx.Model(&models.ChatMessageReaction{}).Where("message_id = ?", msg.ID).Distinct("code").Count(&kinds)
		var same int64
		tx.Model(&models.ChatMessageReaction{}).Where("message_id = ? AND code = ?", msg.ID, code).Count(&same)
		if same == 0 && kinds >= chatMaxReactionKinds {
			return errChatTooManyKind
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.ChatMessageReaction{
			ID: uuid.New(), OrganizationID: orgID, ChannelID: locked.ID, MessageID: msg.ID, UserID: userID, Code: code,
		}).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "react")
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessageReactions, userID, &msg.ID, uuid.Nil)
	resp := a.chatMessageResponses(userID, []models.ChatMessage{*msg})[0]
	return r.SendEnvelope(map[string]any{"reactions": resp.Reactions})
}

