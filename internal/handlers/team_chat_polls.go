package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Polls in a channel. A poll is a message (Body = the question) whose Meta carries
// {"poll": {"multiple": bool, "options": [{"id": "1", "label": "..."}]}}; votes are
// rows in chat_poll_votes. Everything that governs a message (who may read the
// channel, who may post, archiving) governs the poll, and a vote is a write like a
// reaction: team_chat:write, channel access, an unarchived channel.

const (
	chatPollMinOptions  = 2
	chatPollMaxOptions  = 10
	chatPollQuestionMax = 300
	chatPollOptionMax   = 100
	chatPollVoterNames  = 5
)

var (
	errChatBadPoll  = &chatHTTPError{http.StatusBadRequest, "A poll needs a question and 2 to 10 different options", "bad_poll"}
	errChatNotAPoll = &chatHTTPError{http.StatusBadRequest, "This message is not a poll", "not_a_poll"}
	errChatBadVote  = &chatHTTPError{http.StatusBadRequest, "Unknown option, or more than one option on a single-choice poll", "bad_vote"}
)

type chatPollRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Multiple bool     `json:"multiple"`
}

type chatVoteRequest struct {
	OptionIDs []string `json:"option_ids"`
}

// ChatPollOption is one option with its tally; Mine says whether the viewer chose it.
type ChatPollOption struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Count  int      `json:"count"`
	Mine   bool     `json:"mine"`
	Voters []string `json:"voters"`
}

// ChatPollResponse is the poll as the viewer sees it.
type ChatPollResponse struct {
	Multiple bool             `json:"multiple"`
	Options  []ChatPollOption `json:"options"`
	Voters   int              `json:"voters"` // distinct people who voted
}

// pollOf reads the poll definition out of a message's Meta; ok is false when the
// message is not a poll.
func pollOf(m models.ChatMessage) (multiple bool, options []map[string]any, ok bool) {
	raw, has := m.Meta["poll"].(map[string]any)
	if !has {
		return false, nil, false
	}
	multiple, _ = raw["multiple"].(bool)
	list, _ := raw["options"].([]any)
	for _, o := range list {
		if om, isMap := o.(map[string]any); isMap {
			options = append(options, om)
		}
	}
	return multiple, options, len(options) > 0
}

// chatPollResponses aggregates the votes of the polls among msgs, in one query.
func (a *App) chatPollResponses(viewerID uuid.UUID, msgs []models.ChatMessage, names func([]uuid.UUID) map[uuid.UUID]string) map[uuid.UUID]*ChatPollResponse {
	out := map[uuid.UUID]*ChatPollResponse{}
	var ids []uuid.UUID
	for _, m := range msgs {
		if _, _, ok := pollOf(m); ok {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return out
	}
	var votes []models.ChatPollVote
	a.DB.Where("message_id IN ?", ids).Order("created_at ASC, id ASC").Find(&votes)
	userIDs := make([]uuid.UUID, 0, len(votes))
	for _, v := range votes {
		userIDs = append(userIDs, v.UserID)
	}
	who := names(userIDs)
	for _, m := range msgs {
		multiple, options, ok := pollOf(m)
		if !ok {
			continue
		}
		p := &ChatPollResponse{Multiple: multiple, Options: make([]ChatPollOption, 0, len(options))}
		idx := map[string]int{}
		for _, o := range options {
			id, _ := o["id"].(string)
			label, _ := o["label"].(string)
			idx[id] = len(p.Options)
			p.Options = append(p.Options, ChatPollOption{ID: id, Label: label, Voters: []string{}})
		}
		seen := map[uuid.UUID]bool{}
		for _, v := range votes {
			if v.MessageID != m.ID {
				continue
			}
			i, known := idx[v.OptionID]
			if !known {
				continue
			}
			p.Options[i].Count++
			if v.UserID == viewerID {
				p.Options[i].Mine = true
			}
			if len(p.Options[i].Voters) < chatPollVoterNames {
				p.Options[i].Voters = append(p.Options[i].Voters, who[v.UserID])
			}
			seen[v.UserID] = true
		}
		p.Voters = len(seen)
		out[m.ID] = p
	}
	return out
}

// CreateChatPoll posts a poll in a channel.
func (a *App) CreateChatPoll(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	var req chatPollRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	question := strings.TrimSpace(req.Question)
	if n := utf8.RuneCountInString(question); n < 1 || n > chatPollQuestionMax {
		return a.sendChatError(r, errChatBadPoll, "create the poll")
	}
	options := make([]any, 0, len(req.Options))
	seen := map[string]bool{}
	for _, raw := range req.Options {
		label := strings.TrimSpace(raw)
		key := strings.ToLower(label)
		if n := utf8.RuneCountInString(label); n < 1 || n > chatPollOptionMax || seen[key] {
			return a.sendChatError(r, errChatBadPoll, "create the poll")
		}
		seen[key] = true
		options = append(options, map[string]any{"id": strconv.Itoa(len(options) + 1), "label": label})
	}
	if len(options) < chatPollMinOptions || len(options) > chatPollMaxOptions {
		return a.sendChatError(r, errChatBadPoll, "create the poll")
	}

	var msg models.ChatMessage
	gate := a.chatGateFor(userID, orgID, ch)
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessLocked(tx, gate, userID, orgID, locked) {
			return errChatNotFound
		}
		if !a.canPostLocked(tx, gate, userID, orgID, locked) {
			return errChatForbidden
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		msg = models.ChatMessage{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
			ChannelID: locked.ID, UserID: userID, Body: question, Kind: models.ChatMessageKindUser,
			Meta: models.JSONB{"poll": map[string]any{"multiple": req.Multiple, "options": options}},
		}
		return tx.Create(&msg).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "create the poll")
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessage, userID, &msg.ID, uuid.Nil)
	return r.SendEnvelope(a.chatMessageResponses(userID, []models.ChatMessage{msg})[0])
}

// VoteChatPoll replaces the caller's choice on a poll with option_ids (empty = take
// the vote back). Idempotent: repeating it changes nothing.
func (a *App) VoteChatPoll(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	msg, ch, ok := a.chatMessageFor(r, userID, orgID)
	if !ok {
		return nil
	}
	var req chatVoteRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	gate := a.chatGateFor(userID, orgID, ch)
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessLocked(tx, gate, userID, orgID, locked) {
			return errChatNotFound
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		var cur models.ChatMessage
		if err := tx.Where("id = ? AND channel_id = ?", msg.ID, locked.ID).First(&cur).Error; err != nil ||
			cur.Kind == models.ChatMessageKindSystem {
			return errChatNotFound
		}
		multiple, options, isPoll := pollOf(cur)
		if !isPoll {
			return errChatNotAPoll
		}
		valid := map[string]bool{}
		for _, o := range options {
			id, _ := o["id"].(string)
			valid[id] = true
		}
		chosen := map[string]bool{}
		for _, id := range req.OptionIDs {
			if !valid[id] {
				return errChatBadVote
			}
			chosen[id] = true
		}
		if !multiple && len(chosen) > 1 {
			return errChatBadVote
		}
		// Serialize this voter on this poll: delete-then-insert must not interleave.
		if err := lockChatKey(tx, "chat_vote:"+msg.ID.String()+":"+userID.String()); err != nil {
			return err
		}
		if err := tx.Where("message_id = ? AND user_id = ?", msg.ID, userID).Delete(&models.ChatPollVote{}).Error; err != nil {
			return err
		}
		for id := range chosen {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.ChatPollVote{
				ID: uuid.New(), OrganizationID: orgID, ChannelID: locked.ID, MessageID: msg.ID, UserID: userID, OptionID: id,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "vote")
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessageReactions, userID, &msg.ID, uuid.Nil)
	resp := a.chatMessageResponses(userID, []models.ChatMessage{*msg})[0]
	return r.SendEnvelope(map[string]any{"poll": resp.Poll})
}
