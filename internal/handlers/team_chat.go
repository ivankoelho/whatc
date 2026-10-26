package handlers

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// REST for the internal chat (docs/superpowers/specs/2026-10-16-chat-interno-
// nativo-design.md, Entrega 2). The organization always comes from the session;
// every channel/message id from the client goes through loadChatChannel and
// canAccessChatChannel. A channel the caller cannot access is answered 404, the
// same as a missing one, so ids cannot be probed. Real-time events are Entrega 3.

const (
	chatMaxBodyRunes   = 4000
	chatMaxNameRunes   = 100
	chatDefaultPage    = 50
	chatMaxPage        = 100
	chatMaxInitMembers = 200
)

// ChatChannelResponse is one row of the channel list.
type ChatChannelResponse struct {
	ID            uuid.UUID              `json:"id"`
	Kind          models.ChatChannelKind `json:"kind"`
	Name          string                 `json:"name"`
	TeamID        *uuid.UUID             `json:"team_id,omitempty"`
	DMUserID      *uuid.UUID             `json:"dm_user_id,omitempty"`
	UnreadCount   int64                  `json:"unread_count"`
	LastMessageAt *time.Time             `json:"last_message_at,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`

	// Free channels only: lets the UI tell same-named channels apart and show the
	// right actions. MyRole is the caller's role in the channel ("owner"/"member").
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
	MyRole        string     `json:"my_role,omitempty"`
	MemberCount   int64      `json:"member_count,omitempty"`
	CreatedByName string     `json:"created_by_name,omitempty"`
}

// ChatMessageResponse is one message with its author's display name.
type ChatMessageResponse struct {
	ID          uuid.UUID  `json:"id"`
	ChannelID   uuid.UUID  `json:"channel_id"`
	UserID      uuid.UUID  `json:"user_id"`
	UserName    string     `json:"user_name"`
	Body        string     `json:"body"`
	ParentID    *uuid.UUID `json:"parent_id,omitempty"`
	ContextType string     `json:"context_type,omitempty"`
	ContextID   *uuid.UUID `json:"context_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	EditedAt    *time.Time `json:"edited_at,omitempty"`

	// Kind is "user" or "system". A system message carries no body: Meta holds the
	// event ("member_added", ...) and its parameters for the UI to translate.
	Kind models.ChatMessageKind `json:"kind"`
	Meta models.JSONB           `json:"meta,omitempty"`

	// ReplyTo quotes the message this one answers (ParentID); Reactions are the
	// emojis on it, aggregated, with whether the viewer used each.
	ReplyTo   *ChatReplyTo          `json:"reply_to,omitempty"`
	Reactions []ChatReactionSummary `json:"reactions"`

	// Attachments are the files and voice notes of the message.
	Attachments []ChatAttachmentResponse `json:"attachments"`

	// Poll is set when the message is a poll: its options with the tally and the viewer's own votes.
	Poll *ChatPollResponse `json:"poll,omitempty"`
}

type chatChannelRequest struct {
	Name      string      `json:"name"`
	MemberIDs []uuid.UUID `json:"member_ids"`
}

type chatDMRequest struct {
	UserID uuid.UUID `json:"user_id"`
}

type chatMessageRequest struct {
	Body     string     `json:"body"`
	ParentID *uuid.UUID `json:"parent_id"` // send only: the message being answered
	// AttachmentIDs (send only): pending uploads to fix to the new message.
	AttachmentIDs []uuid.UUID `json:"attachment_ids"`
}

// validChatBody trims and bounds a message body. Empty or oversized is rejected.
func validChatBody(raw string) (string, bool) {
	body := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(body)
	return body, n >= 1 && n <= chatMaxBodyRunes
}

// chatUserNames maps user ids to display names.
func (a *App) chatUserNames(ids []uuid.UUID) map[uuid.UUID]string {
	names := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	var users []models.User
	a.DB.Select("id", "full_name", "email").Where("id IN ?", ids).Find(&users)
	for _, u := range users {
		name := u.FullName
		if name == "" {
			name = u.Email
		}
		names[u.ID] = name
	}
	return names
}

// chatOrgUsers returns the subset of ids that are active users of the
// organization (home org or cross-org membership).
func (a *App) chatOrgUsers(orgID uuid.UUID, ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	var found []uuid.UUID
	a.DB.Model(&models.User{}).
		Joins("JOIN user_organizations uo ON uo.user_id = users.id AND uo.organization_id = ? AND uo.deleted_at IS NULL", orgID).
		Where("users.id IN ? AND users.is_active = true", ids).
		Pluck("users.id", &found)
	return found
}

// chatChannelFor loads the channel from the :id path parameter and checks the
// caller may access it. On failure it has already answered (400/404).
func (a *App) chatChannelFor(r *fastglue.Request, userID, orgID uuid.UUID) (*models.ChatChannel, bool) {
	id, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil, false
	}
	ch, ok := a.loadChatChannel(orgID, id)
	if !ok || !a.canAccessChatChannel(userID, orgID, ch) {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Channel not found", nil, "")
		return nil, false
	}
	return ch, true
}

// ensureTeamChatChannels creates the missing team channels for the teams the
// user belongs to (active teams of the organization). The partial unique index
// makes concurrent callers safe: the loser's insert is a no-op.
func (a *App) ensureTeamChatChannels(orgID, userID uuid.UUID) ([]uuid.UUID, error) {
	var teamIDs []uuid.UUID
	if err := a.DB.Model(&models.TeamMember{}).
		Joins("JOIN teams ON teams.id = team_members.team_id AND teams.deleted_at IS NULL AND teams.is_active = true").
		Where("team_members.user_id = ? AND teams.organization_id = ?", userID, orgID).
		Pluck("team_members.team_id", &teamIDs).Error; err != nil || len(teamIDs) == 0 {
		return nil, err
	}
	var have []uuid.UUID
	if err := a.DB.Model(&models.ChatChannel{}).
		Where("organization_id = ? AND kind = ? AND team_id IN ?", orgID, models.ChatChannelKindTeam, teamIDs).
		Pluck("team_id", &have).Error; err != nil {
		return nil, err
	}
	haveSet := make(map[uuid.UUID]bool, len(have))
	for _, id := range have {
		haveSet[id] = true
	}
	for _, tid := range teamIDs {
		if haveSet[tid] {
			continue
		}
		tid := tid
		ch := models.ChatChannel{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
			Kind: models.ChatChannelKindTeam, TeamID: &tid,
		}
		if err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&ch).Error; err != nil {
			return nil, err
		}
	}
	return teamIDs, nil
}

// chatUnreadCounts counts, per channel, messages from other people newer than
// the user's read marker. With no marker the baseline is when the user got
// access (channel/DM join time; for a team channel the later of the channel's
// creation and the user joining the team), so a new member or a new channel
// never inherits history as unread. A marker older than that baseline loses to
// it, so rejoining a team does not resurrect the period spent outside.
func (a *App) chatUnreadCounts(userID uuid.UUID, channels []models.ChatChannel, joined map[uuid.UUID]time.Time) map[uuid.UUID]int64 {
	out := make(map[uuid.UUID]int64, len(channels))
	if len(channels) == 0 {
		return out
	}
	ids := make([]uuid.UUID, len(channels))
	for i, c := range channels {
		ids[i] = c.ID
	}
	var reads []models.ChatRead
	a.DB.Where("user_id = ? AND channel_id IN ?", userID, ids).Find(&reads)
	readBy := make(map[uuid.UUID]models.ChatRead, len(reads))
	for _, rd := range reads {
		readBy[rd.ChannelID] = rd
	}
	for _, c := range channels {
		baseline := joined[c.ID]
		if c.Kind == models.ChatChannelKindTeam && c.CreatedAt.After(baseline) {
			baseline = c.CreatedAt
		}
		q := a.DB.Model(&models.ChatMessage{}).Where("channel_id = ? AND user_id <> ?", c.ID, userID)
		rd, hasRead := readBy[c.ID]
		if hasRead && rd.LastReadMessageID != nil && !rd.LastReadAt.Before(baseline) {
			q = q.Where("(created_at, id) > (?, ?)", rd.LastReadAt, *rd.LastReadMessageID)
		} else {
			since := baseline
			if hasRead && rd.LastReadAt.After(since) {
				since = rd.LastReadAt
			}
			q = q.Where("created_at > ?", since)
		}
		var n int64
		q.Count(&n)
		out[c.ID] = n
	}
	return out
}

// ListChatChannels returns the channels the caller can access, with unread
// counts. Team channels are created on first listing.
func (a *App) ListChatChannels(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}

	teamIDs, err := a.ensureTeamChatChannels(orgID, userID)
	if err != nil {
		a.Log.Error("Failed to ensure team chat channels", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list channels", nil, "")
	}

	var memberRows []models.ChatChannelMember
	a.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).Find(&memberRows)
	joined := make(map[uuid.UUID]time.Time, len(memberRows))
	roleBy := make(map[uuid.UUID]string, len(memberRows))
	memberIDs := make([]uuid.UUID, 0, len(memberRows))
	for _, m := range memberRows {
		joined[m.ChannelID] = m.JoinedAt
		roleBy[m.ChannelID] = m.Role
		memberIDs = append(memberIDs, m.ChannelID)
	}

	var channels []models.ChatChannel
	q := a.DB.Where("organization_id = ?", orgID)
	switch {
	case len(teamIDs) > 0 && len(memberIDs) > 0:
		q = q.Where("(kind = ? AND team_id IN ?) OR id IN ?", models.ChatChannelKindTeam, teamIDs, memberIDs)
	case len(teamIDs) > 0:
		q = q.Where("kind = ? AND team_id IN ?", models.ChatChannelKindTeam, teamIDs)
	case len(memberIDs) > 0:
		q = q.Where("id IN ?", memberIDs)
	default:
		return r.SendEnvelope(map[string]any{"channels": []ChatChannelResponse{}})
	}
	if err := q.Find(&channels).Error; err != nil {
		a.Log.Error("Failed to list chat channels", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list channels", nil, "")
	}
	archivedOnly := string(r.RequestCtx.QueryArgs().Peek("archived")) == "true"
	kept := channels[:0]
	for _, c := range channels {
		if (c.ArchivedAt != nil) == archivedOnly {
			kept = append(kept, c)
		}
	}
	channels = kept

	// Team joined-at (for the unread baseline) and team names.
	teamJoined := map[uuid.UUID]time.Time{}
	teamName := map[uuid.UUID]string{}
	if len(teamIDs) > 0 {
		var tms []models.TeamMember
		a.DB.Where("user_id = ? AND team_id IN ?", userID, teamIDs).Find(&tms)
		for _, tm := range tms {
			teamJoined[tm.TeamID] = tm.CreatedAt
		}
		var teams []models.Team
		a.DB.Select("id", "name").Where("id IN ?", teamIDs).Find(&teams)
		for _, t := range teams {
			teamName[t.ID] = t.Name
		}
	}
	for _, c := range channels {
		if c.Kind == models.ChatChannelKindTeam && c.TeamID != nil {
			joined[c.ID] = teamJoined[*c.TeamID]
		}
	}

	// DM display: the other participant.
	dmOther := map[uuid.UUID]uuid.UUID{}
	var dmIDs []uuid.UUID
	for _, c := range channels {
		if c.Kind == models.ChatChannelKindDM && c.DMUserLowID != nil && c.DMUserHighID != nil {
			other := *c.DMUserLowID
			if other == userID {
				other = *c.DMUserHighID
			}
			dmOther[c.ID] = other
			dmIDs = append(dmIDs, other)
		}
	}
	names := a.chatUserNames(dmIDs)

	unread := a.chatUnreadCounts(userID, channels, joined)

	chanIDs := make([]uuid.UUID, len(channels))
	for i, c := range channels {
		chanIDs[i] = c.ID
	}
	last := map[uuid.UUID]time.Time{}
	if len(chanIDs) > 0 {
		type lastRow struct {
			ChannelID uuid.UUID
			Last      time.Time
		}
		var rows []lastRow
		a.DB.Model(&models.ChatMessage{}).
			Select("channel_id, MAX(created_at) AS last").
			Where("channel_id IN ?", chanIDs).Group("channel_id").Scan(&rows)
		for _, rw := range rows {
			last[rw.ChannelID] = rw.Last
		}
	}

	// Free channels: member count and creator, so same-named channels can be told apart.
	var freeIDs, creatorIDs []uuid.UUID
	for _, c := range channels {
		if c.Kind == models.ChatChannelKindChannel {
			freeIDs = append(freeIDs, c.ID)
			if c.CreatedByID != nil {
				creatorIDs = append(creatorIDs, *c.CreatedByID)
			}
		}
	}
	memberCount := map[uuid.UUID]int64{}
	if len(freeIDs) > 0 {
		type countRow struct {
			ChannelID uuid.UUID
			N         int64
		}
		var rows []countRow
		a.DB.Model(&models.ChatChannelMember{}).Select("channel_id, COUNT(*) AS n").
			Where("channel_id IN ?", freeIDs).Group("channel_id").Scan(&rows)
		for _, rw := range rows {
			memberCount[rw.ChannelID] = rw.N
		}
	}
	creatorNames := a.chatUserNames(creatorIDs)

	out := make([]ChatChannelResponse, 0, len(channels))
	for _, c := range channels {
		resp := ChatChannelResponse{
			ID: c.ID, Kind: c.Kind, Name: c.Name, TeamID: c.TeamID,
			UnreadCount: unread[c.ID], CreatedAt: c.CreatedAt,
		}
		switch c.Kind {
		case models.ChatChannelKindTeam:
			if c.TeamID != nil {
				resp.Name = teamName[*c.TeamID]
			}
		case models.ChatChannelKindDM:
			if other, ok := dmOther[c.ID]; ok {
				o := other
				resp.DMUserID = &o
				resp.Name = names[other]
			}
		}
		if c.Kind == models.ChatChannelKindChannel {
			resp.ArchivedAt = c.ArchivedAt
			resp.MyRole = roleBy[c.ID]
			resp.MemberCount = memberCount[c.ID]
			if c.CreatedByID != nil {
				resp.CreatedByName = creatorNames[*c.CreatedByID]
			}
		}
		if t, ok := last[c.ID]; ok {
			t := t
			resp.LastMessageAt = &t
		}
		out = append(out, resp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].LastMessageAt, out[j].LastMessageAt
		switch {
		case li != nil && lj != nil && !li.Equal(*lj):
			return li.After(*lj)
		case (li != nil) != (lj != nil):
			return li != nil
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return r.SendEnvelope(map[string]any{"channels": out})
}

// CreateChatChannel creates a free (private) channel. The creator owns it;
// extra members must be active users of the organization.
func (a *App) CreateChatChannel(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionManage)
	if err != nil {
		return nil
	}
	var req chatChannelRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > chatMaxNameRunes {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name must be 1-100 characters", nil, "")
	}
	if len(req.MemberIDs) > chatMaxInitMembers {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "too many members", nil, "")
	}

	// Creator first, then the validated, de-duplicated extras.
	members := []uuid.UUID{userID}
	seen := map[uuid.UUID]bool{userID: true}
	for _, id := range a.chatOrgUsers(orgID, req.MemberIDs) {
		if !seen[id] {
			seen[id] = true
			members = append(members, id)
		}
	}

	ch := models.ChatChannel{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
		Kind: models.ChatChannelKindChannel, Name: name, CreatedByID: &userID,
	}
	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		for i, id := range members {
			role := "member"
			if i == 0 {
				role = "owner"
			}
			if err := tx.Create(&models.ChatChannelMember{
				OrganizationID: orgID, ChannelID: ch.ID, UserID: id, Role: role,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		a.Log.Error("Failed to create chat channel", "error", txErr)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create channel", nil, "")
	}
	a.logAudit(orgID, userID, "chat_channel", ch.ID, models.AuditActionCreated, nil, ch)
	a.notifyChat(orgID, &ch, websocket.TypeTeamChatChannel, userID, nil, uuid.Nil)
	return r.SendEnvelope(ChatChannelResponse{
		ID: ch.ID, Kind: ch.Kind, Name: ch.Name, CreatedAt: ch.CreatedAt,
	})
}

// CreateChatDM returns the direct-message channel between the caller and the
// target user, creating it if needed. Idempotent: the canonical pair has a
// unique index, so concurrent callers converge on one channel.
func (a *App) CreateChatDM(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req chatDMRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.UserID == uuid.Nil || req.UserID == userID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "choose another user", nil, "")
	}
	if len(a.chatOrgUsers(orgID, []uuid.UUID{req.UserID})) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
	}

	low, high := models.CanonicalDMPair(userID, req.UserID)
	find := func() (*models.ChatChannel, bool) {
		var ch models.ChatChannel
		err := a.DB.Where("organization_id = ? AND kind = ? AND dm_user_low_id = ? AND dm_user_high_id = ?",
			orgID, models.ChatChannelKindDM, low, high).First(&ch).Error
		return &ch, err == nil
	}
	ch, ok := find()
	if !ok {
		newCh := models.ChatChannel{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
			Kind: models.ChatChannelKindDM, CreatedByID: &userID, DMUserLowID: &low, DMUserHighID: &high,
		}
		txErr := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&newCh).Error; err != nil {
				return err
			}
			for _, id := range []uuid.UUID{low, high} {
				if err := tx.Create(&models.ChatChannelMember{
					OrganizationID: orgID, ChannelID: newCh.ID, UserID: id, Role: "member",
				}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if txErr != nil {
			// Lost a race against a concurrent create: use the winner.
			if ch, ok = find(); !ok {
				a.Log.Error("Failed to create chat DM", "error", txErr)
				return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create conversation", nil, "")
			}
		} else {
			ch = &newCh
			a.notifyChat(orgID, ch, websocket.TypeTeamChatChannel, userID, nil, uuid.Nil)
		}
	}
	other := req.UserID
	return r.SendEnvelope(ChatChannelResponse{
		ID: ch.ID, Kind: ch.Kind, DMUserID: &other,
		Name: a.chatUserNames([]uuid.UUID{other})[other], CreatedAt: ch.CreatedAt,
	})
}

func chatMessageToResponse(m models.ChatMessage, names map[uuid.UUID]string) ChatMessageResponse {
	return ChatMessageResponse{
		ID: m.ID, ChannelID: m.ChannelID, UserID: m.UserID, UserName: names[m.UserID],
		Body: m.Body, ParentID: m.ParentID, ContextType: m.ContextType, ContextID: m.ContextID,
		CreatedAt: m.CreatedAt, EditedAt: m.EditedAt, Kind: kindOrUser(m.Kind), Meta: m.Meta,
	}
}

func kindOrUser(k models.ChatMessageKind) models.ChatMessageKind {
	if k == "" {
		return models.ChatMessageKindUser
	}
	return k
}

// ListChatMessages pages a channel's history backwards with a (created_at, id)
// cursor: pass before_id=<oldest message id you hold> to load older ones.
// The page is returned oldest-first.
func (a *App) ListChatMessages(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}

	limit, _ := strconv.Atoi(string(r.RequestCtx.QueryArgs().Peek("limit")))
	if limit < 1 || limit > chatMaxPage {
		limit = chatDefaultPage
	}

	q := a.DB.Where("channel_id = ?", ch.ID)
	if s := string(r.RequestCtx.QueryArgs().Peek("before_id")); s != "" {
		beforeID, perr := uuid.Parse(s)
		if perr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid before_id", nil, "")
		}
		// The anchor must belong to this channel (Unscoped: it may have been
		// deleted since the client loaded it, and is still a valid cursor).
		var anchor models.ChatMessage
		if err := a.DB.Unscoped().Where("id = ? AND channel_id = ?", beforeID, ch.ID).First(&anchor).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid before_id", nil, "")
		}
		q = q.Where("(created_at, id) < (?, ?)", anchor.CreatedAt, anchor.ID)
	}

	var msgs []models.ChatMessage
	if err := q.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&msgs).Error; err != nil {
		a.Log.Error("Failed to list chat messages", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list messages", nil, "")
	}
	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return r.SendEnvelope(map[string]any{"messages": a.chatMessageResponses(userID, msgs), "has_more": hasMore})
}

// SendChatMessage posts a message to a channel the caller can access.
func (a *App) SendChatMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	var req chatMessageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if len(req.AttachmentIDs) > chatMaxAttachments {
		return a.sendChatError(r, errChatTooManyAtt, "send the message")
	}
	body, valid := validChatBody(req.Body)
	// With attachments the text is optional (0-4000 characters); without, 1-4000.
	if !valid && (len(req.AttachmentIDs) == 0 || strings.TrimSpace(req.Body) != "") {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "body must be 1-4000 characters", nil, "")
	}
	// FOR SHARE on the channel row: concurrent sends do not block each other, but an
	// archive or a removal (FOR UPDATE) waits for them and the other way round, so a
	// message either lands before the archive or is refused, never after it. Access is
	// re-checked inside the lock: someone removed a moment ago cannot slip one in.
	var msg models.ChatMessage
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessChatChannelDB(tx, userID, orgID, locked) {
			return errChatNotFound
		}
		if !a.canPostChatChannelDB(tx, userID, orgID, locked) {
			return errChatForbidden
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		if req.ParentID != nil {
			// The original must still exist, be a person's message, and be in this channel.
			var n int64
			tx.Model(&models.ChatMessage{}).Where("id = ? AND channel_id = ? AND organization_id = ? AND kind = ?",
				*req.ParentID, locked.ID, orgID, models.ChatMessageKindUser).Count(&n)
			if n == 0 {
				return errChatReplyGone
			}
		}
		msg = models.ChatMessage{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID,
			ChannelID: locked.ID, UserID: userID, Body: body, Kind: models.ChatMessageKindUser,
			ParentID: req.ParentID,
		}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		if len(req.AttachmentIDs) > 0 {
			return attachPendingFiles(tx, orgID, userID, locked.ID, msg.ID, req.AttachmentIDs)
		}
		return nil
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "send the message")
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessage, userID, &msg.ID, uuid.Nil)
	return r.SendEnvelope(a.chatMessageResponses(userID, []models.ChatMessage{msg})[0])
}

// chatMessageFor loads the message from the :id path parameter within the
// caller's organization, together with its channel. A message whose channel the
// caller cannot access is answered 404.
func (a *App) chatMessageFor(r *fastglue.Request, userID, orgID uuid.UUID) (*models.ChatMessage, *models.ChatChannel, bool) {
	id, err := parsePathUUID(r, "id", "message")
	if err != nil {
		return nil, nil, false
	}
	var msg models.ChatMessage
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&msg).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Message not found", nil, "")
		return nil, nil, false
	}
	ch, ok := a.loadChatChannel(orgID, msg.ChannelID)
	if !ok || !a.canAccessChatChannel(userID, orgID, ch) {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Message not found", nil, "")
		return nil, nil, false
	}
	return &msg, ch, true
}

// UpdateChatMessage edits the caller's own message.
func (a *App) UpdateChatMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	msg, ch, ok := a.chatMessageFor(r, userID, orgID)
	if !ok {
		return nil
	}
	if msg.Kind == models.ChatMessageKindSystem {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "System messages cannot be edited", nil, fastglue.ErrorType("system_message"))
	}
	if msg.UserID != userID {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You can only edit your own messages", nil, "")
	}
	if _, _, isPoll := pollOf(*msg); isPoll {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A poll cannot be edited", nil, fastglue.ErrorType("poll_message"))
	}
	var req chatMessageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	body, valid := validChatBody(req.Body)
	if !valid {
		// A message with attachments may end up with no text at all.
		var withFiles int64
		a.DB.Model(&models.ChatAttachment{}).Where("message_id = ? AND purged_at IS NULL", msg.ID).Count(&withFiles)
		if withFiles == 0 || strings.TrimSpace(req.Body) != "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "body must be 1-4000 characters", nil, "")
		}
	}
	now := time.Now()
	txErr := a.withLockedChannel(orgID, ch.ID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		if !a.canAccessChatChannelDB(tx, userID, orgID, locked) {
			return errChatNotFound
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		return tx.Model(msg).Updates(map[string]any{"body": body, "edited_at": now}).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "edit the message")
	}
	msg.Body, msg.EditedAt = body, &now
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessageUpdated, userID, &msg.ID, uuid.Nil)
	return r.SendEnvelope(a.chatMessageResponses(userID, []models.ChatMessage{*msg})[0])
}

// DeleteChatMessage soft-deletes a message: the author may delete their own,
// a holder of team_chat:delete may delete anyone's (both need channel access).
// Moderation (deleting someone else's) is written to the audit log.
func (a *App) DeleteChatMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	msg, ch, ok := a.chatMessageFor(r, userID, orgID)
	if !ok {
		return nil
	}
	if !a.canDeleteChatMessage(userID, orgID, msg, ch) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot delete this message", nil, "")
	}
	if err := a.DB.Delete(msg).Error; err != nil {
		a.Log.Error("Failed to delete chat message", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete message", nil, "")
	}
	if msg.UserID != userID {
		a.logAudit(orgID, userID, "chat_message", msg.ID, models.AuditActionDeleted,
			map[string]any{"channel_id": msg.ChannelID, "author_id": msg.UserID}, nil)
	}
	a.notifyChat(orgID, ch, websocket.TypeTeamChatMessageDeleted, userID, &msg.ID, uuid.Nil)
	return r.SendEnvelope(map[string]any{"deleted": true})
}

// MarkChatChannelRead moves the caller's read marker to the channel's latest
// message. The marker only moves forward.
func (a *App) MarkChatChannelRead(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}

	var latest models.ChatMessage
	hasLatest := a.DB.Where("channel_id = ?", ch.ID).
		Order("created_at DESC, id DESC").First(&latest).Error == nil

	at := time.Now()
	var msgID *uuid.UUID
	if hasLatest {
		at, msgID = latest.CreatedAt, &latest.ID
	}

	var cur models.ChatRead
	if a.DB.Where("channel_id = ? AND user_id = ?", ch.ID, userID).First(&cur).Error == nil {
		if hasLatest && cur.LastReadMessageID != nil && *cur.LastReadMessageID == latest.ID {
			a.notifyChatRead(orgID, userID, ch)
			return r.SendEnvelope(map[string]any{"unread_count": 0})
		}
		if cur.LastReadAt.After(at) {
			a.notifyChatRead(orgID, userID, ch)
			return r.SendEnvelope(map[string]any{"unread_count": 0})
		}
		a.DB.Model(&cur).Updates(map[string]any{"last_read_at": at, "last_read_message_id": msgID})
	} else {
		rd := models.ChatRead{
			OrganizationID: orgID, ChannelID: ch.ID, UserID: userID,
			LastReadAt: at, LastReadMessageID: msgID,
		}
		// A concurrent first mark-read is harmless: the other row wins.
		a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rd)
	}
	a.notifyChatRead(orgID, userID, ch)
	return r.SendEnvelope(map[string]any{"unread_count": 0})
}

// ChatUserResponse is one person in the picker for DMs and channel members.
type ChatUserResponse struct {
	ID       uuid.UUID `json:"id"`
	FullName string    `json:"full_name"`
	IsOnline bool      `json:"is_online"`
}

// ListChatUsers returns the active users of the caller's organization (minus the
// caller) so a chat-only agent can pick someone to talk to without holding
// users:read. Only id and display name leave the server, never e-mail or role.
// Presence is the hub's, the same source as everywhere else.
func (a *App) ListChatUsers(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	q := a.DB.Model(&models.User{}).
		Joins("JOIN user_organizations uo ON uo.user_id = users.id AND uo.organization_id = ? AND uo.deleted_at IS NULL", orgID).
		Where("users.is_active = true AND users.id <> ?", userID)
	if s := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("q"))); s != "" {
		q = q.Where("users.full_name ILIKE ?", "%"+s+"%")
	}
	var users []models.User
	if err := q.Select("users.id", "users.full_name", "users.email").
		Order("users.full_name ASC").Limit(200).Find(&users).Error; err != nil {
		a.Log.Error("Failed to list chat users", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list users", nil, "")
	}
	ids := make([]uuid.UUID, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	online := map[uuid.UUID]bool{}
	if a.WSHub != nil {
		for _, id := range a.WSHub.FilterOnlineUsers(orgID, ids) {
			online[id] = true
		}
	}
	out := make([]ChatUserResponse, len(users))
	for i, u := range users {
		name := u.FullName
		if name == "" {
			name = u.Email
		}
		out[i] = ChatUserResponse{ID: u.ID, FullName: name, IsOnline: online[u.ID]}
	}
	return r.SendEnvelope(map[string]any{"users": out})
}
