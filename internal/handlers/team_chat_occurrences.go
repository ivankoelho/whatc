package handlers

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// The discussions of cases, inside the Chat Interno module.
//
// A case discussion is a chat channel of kind "occurrence" (see occurrence_chat.go); here it
// is made findable from the chat itself. Who sees what is NOT decided here: the list only
// ever contains channels whose case passes visibleOccurrences (the same scope as the case
// list), and opening one goes through the usual channel access (case access + occurrences:read).
//
// The list in the sidebar is the discussions the person is INVOLVED in (assignee, opener,
// participant, wrote or read there) and that have at least one message; everything else
// they may see is reachable through search.

const (
	chatOccurrenceListLimit   = 100
	chatOccurrenceSearchLimit = 20
	chatOccurrenceSearchMin   = 2
)

// chatOccurrenceChannels returns the case discussions visible to the user. involvedOnly
// narrows it to those they take part in; q (protocol, title or customer) narrows by text.
func (a *App) chatOccurrenceChannels(orgID, userID uuid.UUID, involvedOnly bool, q string, limit int) []models.ChatChannel {
	if !a.HasPermission(userID, models.ResourceOccurrences, models.ActionRead, orgID) {
		return nil
	}
	visible := a.visibleOccurrences(
		a.DB.Model(&models.Occurrence{}).Select("occurrences.id").Where("occurrences.organization_id = ?", orgID), userID, orgID)
	if q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		visible = visible.Joins("LEFT JOIN contacts ON contacts.id = occurrences.contact_id").
			Where("(LOWER(occurrences.protocol_number) LIKE ? OR LOWER(occurrences.title) LIKE ? OR LOWER(contacts.profile_name) LIKE ?)", like, like, like)
	}
	tx := a.DB.Where("organization_id = ? AND kind = ? AND occurrence_id IN (?)", orgID, models.ChatChannelKindOccurrence, visible).
		Where("id IN (?)", a.DB.Model(&models.ChatMessage{}).Select("channel_id").Where("kind = ?", models.ChatMessageKindUser))
	if involvedOnly {
		tx = tx.Where(`(occurrence_id IN (?) OR id IN (?) OR id IN (?))`,
			a.DB.Model(&models.Occurrence{}).Select("id").
				Where("organization_id = ? AND (assigned_user_id = ? OR opened_by_user_id = ? OR id IN (?))", orgID, userID, userID,
					a.DB.Model(&models.OccurrenceParticipant{}).Select("occurrence_id").Where("user_id = ?", userID)),
			a.DB.Model(&models.ChatMessage{}).Select("channel_id").Where("user_id = ?", userID),
			a.DB.Model(&models.ChatRead{}).Select("channel_id").Where("user_id = ?", userID))
	}
	var out []models.ChatChannel
	tx.Order("updated_at DESC").Limit(limit).Find(&out)
	return out
}

// chatOccurrenceInfo maps channel -> (case id, "protocol · title") for display.
func (a *App) chatOccurrenceInfo(channels []models.ChatChannel) map[uuid.UUID]struct {
	ID    uuid.UUID
	Label string
} {
	out := map[uuid.UUID]struct {
		ID    uuid.UUID
		Label string
	}{}
	var ids []uuid.UUID
	for _, c := range channels {
		if c.OccurrenceID != nil {
			ids = append(ids, *c.OccurrenceID)
		}
	}
	if len(ids) == 0 {
		return out
	}
	var occs []models.Occurrence
	a.DB.Select("id", "protocol_number", "title").Where("id IN ?", ids).Find(&occs)
	byID := map[uuid.UUID]models.Occurrence{}
	for _, o := range occs {
		byID[o.ID] = o
	}
	for _, c := range channels {
		if c.OccurrenceID == nil {
			continue
		}
		o := byID[*c.OccurrenceID]
		label := o.ProtocolNumber
		if strings.TrimSpace(o.Title) != "" {
			label += " · " + o.Title
		}
		out[c.ID] = struct {
			ID    uuid.UUID
			Label string
		}{ID: o.ID, Label: label}
	}
	return out
}

// chatOccurrenceResponses builds the sidebar entries of case discussions, with unread counts.
func (a *App) chatOccurrenceResponses(userID uuid.UUID, channels []models.ChatChannel) []ChatChannelResponse {
	if len(channels) == 0 {
		return []ChatChannelResponse{}
	}
	joined := make(map[uuid.UUID]time.Time, len(channels))
	ids := make([]uuid.UUID, len(channels))
	for i, c := range channels {
		joined[c.ID] = c.CreatedAt
		ids[i] = c.ID
	}
	unread := a.chatUnreadCounts(userID, channels, joined)
	info := a.chatOccurrenceInfo(channels)
	type lastRow struct {
		ChannelID uuid.UUID
		Last      time.Time
	}
	var rows []lastRow
	a.DB.Model(&models.ChatMessage{}).Select("channel_id, MAX(created_at) AS last").
		Where("channel_id IN ?", ids).Group("channel_id").Scan(&rows)
	last := map[uuid.UUID]time.Time{}
	for _, rw := range rows {
		last[rw.ChannelID] = rw.Last
	}
	out := make([]ChatChannelResponse, 0, len(channels))
	for _, c := range channels {
		resp := ChatChannelResponse{
			ID: c.ID, Kind: c.Kind, Name: info[c.ID].Label, UnreadCount: unread[c.ID], CreatedAt: c.CreatedAt,
		}
		if resp.Name == "" {
			resp.Name = c.Name
		}
		if oid := info[c.ID].ID; oid != uuid.Nil {
			resp.OccurrenceID = &oid
		}
		if t, ok := last[c.ID]; ok {
			t := t
			resp.LastMessageAt = &t
		}
		out = append(out, resp)
	}
	return out
}

// SearchChatOccurrences finds case discussions by protocol, title or customer, among the
// cases the caller may see. A result is only a pointer: opening it applies the usual access.
func (a *App) SearchChatOccurrences(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	q := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("q")))
	if len([]rune(q)) < chatOccurrenceSearchMin {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Type at least 2 characters", nil, "")
	}
	channels := a.chatOccurrenceChannels(orgID, userID, false, q, chatOccurrenceSearchLimit)
	return r.SendEnvelope(map[string]any{"channels": a.chatOccurrenceResponses(userID, channels)})
}
