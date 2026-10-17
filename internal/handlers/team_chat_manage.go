package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Channel management for the internal chat (docs/superpowers/specs/
// 2026-10-16-chat-interno-gestao-de-canais-design.md, Phase A).
//
// Three rules shape this file:
//  1. Managing who is IN a channel is not reading what is SAID in it. Nothing here
//     returns message content to someone who is not a member, and nobody joins a
//     channel through the members route for themselves: the only way in without
//     being added is JoinChatChannel, which needs team_chat:join_any, a reason, and
//     leaves a system message, an audit entry and an event.
//  2. Every write follows one lock hierarchy: the channel row is the only lock and
//     the first one taken (FOR SHARE for sends/edits/adds, FOR UPDATE for the rest),
//     never upgraded, never two channels in one transaction.
//  3. The change, its system message and its audit entry are written in ONE
//     transaction (audit through the same tx, error checked), and the event goes out
//     after the commit.

const (
	chatRoleOwner  = "owner"
	chatRoleMember = "member"

	chatMinReasonRunes = 5
	chatMaxReasonRunes = 500
	chatAdminListCap   = 500
)

// chatAuditWriter writes the audit entry of a chat change through the given
// transaction. It is a variable only so a test can inject a failure and prove the
// change rolls back with it.
var chatAuditWriter = audit.LogAuditTx

// chatHTTPError is a failure with the HTTP answer it maps to. Code is a stable
// machine-readable reason for the UI ("last_owner", "archived", ...).
type chatHTTPError struct {
	status int
	msg    string
	code   string
}

func (e *chatHTTPError) Error() string { return e.msg }

var (
	errChatNotFound  = &chatHTTPError{http.StatusNotFound, "Channel not found", "not_found"}
	errChatForbidden = &chatHTTPError{http.StatusForbidden, "You cannot do this in this channel", "forbidden"}
	errChatArchived  = &chatHTTPError{http.StatusConflict, "The channel is archived", "archived"}
	errChatLastOwner = &chatHTTPError{http.StatusConflict, "The last owner cannot stop being owner: promote someone else or archive the channel", "last_owner"}
	errChatAlready   = &chatHTTPError{http.StatusConflict, "Already a member", "already_member"}
	errChatSelfAdd   = &chatHTTPError{http.StatusForbidden, "Nobody adds themselves through the members route; use the administrative join", "self_add"}
)

func chatBadRequest(msg string) error { return &chatHTTPError{http.StatusBadRequest, msg, "invalid"} }

// sendChatError answers the request for err; an unexpected error is logged and
// becomes a generic 500.
func (a *App) sendChatError(r *fastglue.Request, err error, what string) error {
	var he *chatHTTPError
	if errors.As(err, &he) {
		return r.SendErrorEnvelope(he.status, he.msg, nil, fastglue.ErrorType(he.code))
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.SendErrorEnvelope(errChatNotFound.status, errChatNotFound.msg, nil, fastglue.ErrorType(errChatNotFound.code))
	}
	a.Log.Error("Chat management failed", "what", what, "error", err)
	return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to "+what, nil, "")
}

// chatLockMode is the strength of the lock taken on the channel row.
type chatLockMode string

const (
	chatLockShare  chatLockMode = "SHARE"  // send, edit, add member
	chatLockUpdate chatLockMode = "UPDATE" // everything else that writes the channel
)

// withLockedChannel runs fn in a transaction whose FIRST statement locks the
// channel row (scoped to the organization) in the given mode. A channel of another
// organization is indistinguishable from a missing one (gorm.ErrRecordNotFound).
func (a *App) withLockedChannel(orgID, channelID uuid.UUID, mode chatLockMode, fn func(tx *gorm.DB, ch *models.ChatChannel) error) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		var ch models.ChatChannel
		if err := tx.Clauses(clause.Locking{Strength: string(mode)}).
			Where("id = ? AND organization_id = ?", channelID, orgID).
			First(&ch).Error; err != nil {
			return err
		}
		return fn(tx, &ch)
	})
}

// chatActor is who is asking and what they are in this channel.
type chatActor struct {
	userID    uuid.UUID
	isMember  bool
	isOwner   bool
	canManage bool // team_chat:manage
}

func (a *App) chatActorIn(tx *gorm.DB, orgID, userID uuid.UUID, ch *models.ChatChannel) chatActor {
	act := chatActor{userID: userID, canManage: a.HasPermission(userID, models.ResourceTeamChat, models.ActionManage, orgID)}
	var m models.ChatChannelMember
	if err := tx.Where("channel_id = ? AND user_id = ? AND organization_id = ?", ch.ID, userID, orgID).First(&m).Error; err == nil {
		act.isMember = true
		act.isOwner = m.Role == chatRoleOwner
	}
	return act
}

// requireFreeChannel: management only exists for free channels. A team channel
// follows its team and a DM is immutable, so both answer as if the channel did not
// exist for this operation.
func requireFreeChannel(ch *models.ChatChannel) error {
	if ch.Kind != models.ChatChannelKindChannel {
		return errChatNotFound
	}
	return nil
}

// requireComposer: the owner or a holder of team_chat:manage may manage the
// channel. Someone who is neither and is not a member is told it does not exist
// (no probing); a member without the right is forbidden.
func (act chatActor) requireComposer() error {
	if act.isOwner || act.canManage {
		return nil
	}
	if !act.isMember {
		return errChatNotFound
	}
	return errChatForbidden
}

func countChatOwners(tx *gorm.DB, channelID uuid.UUID) int64 {
	var n int64
	tx.Model(&models.ChatChannelMember{}).Where("channel_id = ? AND role = ?", channelID, chatRoleOwner).Count(&n)
	return n
}

// recordChatChange writes the system message and the audit entry of a change, in
// the caller's transaction. The system message is inserted BEFORE any membership row
// the change creates, so a newly added person's unread baseline (their join time)
// falls after it and the notice about themselves does not show up as unread.
func (a *App) recordChatChange(tx *gorm.DB, orgID, actorID uuid.UUID, ch *models.ChatChannel, event string, meta models.JSONB, auditChange map[string]any) error {
	actorName := audit.GetUserName(tx, actorID)
	if meta == nil {
		meta = models.JSONB{}
	}
	meta["event"] = event
	meta["actor_id"] = actorID.String()
	meta["actor_name"] = actorName
	msg := models.ChatMessage{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, ChannelID: ch.ID,
		UserID: actorID, Body: "", Kind: models.ChatMessageKindSystem, Meta: meta,
	}
	if err := tx.Create(&msg).Error; err != nil {
		return err
	}
	return chatAuditWriter(tx, orgID, actorID, actorName, "chat_channel", ch.ID, models.AuditActionUpdated, auditChange)
}

// chatRecipientsBefore is read before a change so the event also reaches whoever
// loses access by it.
func (a *App) chatRecipientsBefore(orgID, channelID uuid.UUID) []uuid.UUID {
	ch, ok := a.loadChatChannel(orgID, channelID)
	if !ok {
		return nil
	}
	return a.chatRecipients(orgID, ch)
}

// notifyChannelChanged sends team_chat_channel_updated to the union of the people
// who had access before and after the change. Ids only: what changed is a code, and
// anything with content (names, reason, text) is fetched by REST after an access
// check. It runs AFTER the commit.
func (a *App) notifyChannelChanged(orgID uuid.UUID, ch *models.ChatChannel, before []uuid.UUID, actorID uuid.UUID, change string) {
	if a.WSHub == nil {
		return
	}
	fresh, ok := a.loadChatChannel(orgID, ch.ID)
	if !ok {
		fresh = ch
	}
	seen := map[uuid.UUID]bool{}
	var to []uuid.UUID
	for _, id := range append(append([]uuid.UUID{}, before...), a.chatRecipients(orgID, fresh)...) {
		if !seen[id] {
			seen[id] = true
			to = append(to, id)
		}
	}
	if len(to) == 0 {
		return
	}
	a.WSHub.BroadcastToUsers(orgID, to, websocket.WSMessage{
		Type: websocket.TypeTeamChatChannelUpdated,
		Payload: websocket.TeamChatEventPayload{
			ChannelID: ch.ID, UserID: actorID, At: time.Now(), Change: change,
		},
	})
}

func chatMemberAudit(field string, target uuid.UUID, extra map[string]any) map[string]any {
	c := map[string]any{"field": field, "old_value": nil, "new_value": target.String()}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

// ---- members ----------------------------------------------------------------

type chatMemberRequest struct {
	UserID uuid.UUID `json:"user_id"`
}

type chatRoleRequest struct {
	Role string `json:"role"`
}

type chatRenameRequest struct {
	Name string `json:"name"`
}

type chatJoinRequest struct {
	Reason string `json:"reason"`
}

// ChatMemberResponse is a person in a channel.
type ChatMemberResponse struct {
	UserID   uuid.UUID `json:"user_id"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	IsOnline bool      `json:"is_online"`
}

func (a *App) chatMemberResponses(orgID uuid.UUID, rows []models.ChatChannelMember) []ChatMemberResponse {
	ids := make([]uuid.UUID, len(rows))
	for i, m := range rows {
		ids[i] = m.UserID
	}
	names := a.chatUserNames(ids)
	online := map[uuid.UUID]bool{}
	if a.WSHub != nil {
		for _, id := range a.WSHub.FilterOnlineUsers(orgID, ids) {
			online[id] = true
		}
	}
	out := make([]ChatMemberResponse, len(rows))
	for i, m := range rows {
		out[i] = ChatMemberResponse{UserID: m.UserID, Name: names[m.UserID], Role: m.Role, IsOnline: online[m.UserID]}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Role == chatRoleOwner) != (out[j].Role == chatRoleOwner) {
			return out[i].Role == chatRoleOwner
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ListChatChannelMembers lists who is in a channel the caller is in. Members only:
// whoever is not in the channel (managers included) uses the administration list.
func (a *App) ListChatChannelMembers(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	var rows []models.ChatChannelMember
	if ch.Kind == models.ChatChannelKindTeam {
		if ch.TeamID == nil {
			return r.SendEnvelope(map[string]any{"members": []ChatMemberResponse{}})
		}
		var tms []models.TeamMember
		a.DB.Where("team_id = ?", *ch.TeamID).Find(&tms)
		for _, tm := range tms {
			rows = append(rows, models.ChatChannelMember{UserID: tm.UserID, Role: chatRoleMember})
		}
	} else {
		a.DB.Where("channel_id = ? AND organization_id = ?", ch.ID, orgID).Find(&rows)
	}
	return r.SendEnvelope(map[string]any{"members": a.chatMemberResponses(orgID, rows)})
}

// AddChatChannelMember adds ANOTHER person to a free channel. Nobody adds
// themselves here, whatever their role (criterion 13): that would let someone with
// team_chat:manage and join_any enter without a reason, a system message or an audit
// entry. Entering for oneself is JoinChatChannel.
func (a *App) AddChatChannelMember(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	var req chatMemberRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	txErr := a.withLockedChannel(orgID, channelID, chatLockShare, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		act := a.chatActorIn(tx, orgID, userID, locked)
		if err := act.requireComposer(); err != nil {
			return err
		}
		if req.UserID == userID {
			return errChatSelfAdd
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		if len(a.chatOrgUsers(orgID, []uuid.UUID{req.UserID})) == 0 {
			return errChatNotFound
		}
		var existing int64
		tx.Model(&models.ChatChannelMember{}).Where("channel_id = ? AND user_id = ?", locked.ID, req.UserID).Count(&existing)
		if existing > 0 {
			return errChatAlready
		}
		name := a.chatUserNames([]uuid.UUID{req.UserID})[req.UserID]
		if err := a.recordChatChange(tx, orgID, userID, locked, models.ChatEventMemberAdded,
			models.JSONB{"target_id": req.UserID.String(), "target_name": name},
			chatMemberAudit("member_added", req.UserID, nil)); err != nil {
			return err
		}
		return tx.Create(&models.ChatChannelMember{
			OrganizationID: orgID, ChannelID: locked.ID, UserID: req.UserID, Role: chatRoleMember,
		}).Error
	})
	if txErr != nil {
		return a.sendChatError(r, uniqueToAlready(txErr), "add the member")
	}
	a.notifyChannelChanged(orgID, ch, before, userID, models.ChatEventMemberAdded)
	return r.SendEnvelope(map[string]any{"added": true})
}

// uniqueToAlready turns a duplicate-key failure (two concurrent adds of the same
// person) into the same 409 as the explicit check.
func uniqueToAlready(err error) error {
	if err != nil && (strings.Contains(err.Error(), "idx_chat_member_channel_user") || strings.Contains(err.Error(), "23505")) {
		return errChatAlready
	}
	return err
}

// removeChatMember is shared by removing someone, leaving, and removing oneself:
// they are the same operation with the same last-owner rule.
func (a *App) removeChatMember(r *fastglue.Request, orgID, userID, channelID, target uuid.UUID) error {
	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	self := target == userID
	txErr := a.withLockedChannel(orgID, channelID, chatLockUpdate, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		act := a.chatActorIn(tx, orgID, userID, locked)
		if self {
			if !act.isMember {
				return errChatNotFound
			}
		} else if err := act.requireComposer(); err != nil {
			return err
		}
		var row models.ChatChannelMember
		if err := tx.Where("channel_id = ? AND user_id = ?", locked.ID, target).First(&row).Error; err != nil {
			return errChatNotFound
		}
		// The last owner cannot stop being owner by their own action; someone else
		// who manages may still remove them.
		if self && row.Role == chatRoleOwner && countChatOwners(tx, locked.ID) <= 1 {
			return errChatLastOwner
		}
		event := models.ChatEventMemberRemoved
		if self {
			event = models.ChatEventMemberLeft
		}
		name := a.chatUserNames([]uuid.UUID{target})[target]
		if err := a.recordChatChange(tx, orgID, userID, locked, event,
			models.JSONB{"target_id": target.String(), "target_name": name},
			chatMemberAudit(event, target, nil)); err != nil {
			return err
		}
		return tx.Delete(&models.ChatChannelMember{}, "id = ?", row.ID).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "remove the member")
	}
	change := models.ChatEventMemberRemoved
	if self {
		change = models.ChatEventMemberLeft
	}
	a.notifyChannelChanged(orgID, ch, before, userID, change)
	return r.SendEnvelope(map[string]any{"removed": true})
}

// RemoveChatChannelMember removes a member (or, when it is the caller, leaves).
func (a *App) RemoveChatChannelMember(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	target, err := parsePathUUID(r, "member_id", "member")
	if err != nil {
		return nil
	}
	return a.removeChatMember(r, orgID, userID, channelID, target)
}

// LeaveChatChannel is the caller leaving.
func (a *App) LeaveChatChannel(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	return a.removeChatMember(r, orgID, userID, channelID, userID)
}

// SetChatMemberRole promotes a member to owner or demotes an owner. An owner cannot
// demote themselves while they are the last one.
func (a *App) SetChatMemberRole(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	target, err := parsePathUUID(r, "member_id", "member")
	if err != nil {
		return nil
	}
	var req chatRoleRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Role != chatRoleOwner && req.Role != chatRoleMember {
		return a.sendChatError(r, chatBadRequest("role must be owner or member"), "change the role")
	}

	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	txErr := a.withLockedChannel(orgID, channelID, chatLockUpdate, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		act := a.chatActorIn(tx, orgID, userID, locked)
		if err := act.requireComposer(); err != nil {
			return err
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		var row models.ChatChannelMember
		if err := tx.Where("channel_id = ? AND user_id = ?", locked.ID, target).First(&row).Error; err != nil {
			return errChatNotFound
		}
		if row.Role == req.Role {
			return nil
		}
		if req.Role == chatRoleMember && target == userID && row.Role == chatRoleOwner && countChatOwners(tx, locked.ID) <= 1 {
			return errChatLastOwner
		}
		name := a.chatUserNames([]uuid.UUID{target})[target]
		if err := a.recordChatChange(tx, orgID, userID, locked, models.ChatEventOwnerChanged,
			models.JSONB{"target_id": target.String(), "target_name": name, "role": req.Role},
			chatMemberAudit("member_role", target, map[string]any{"old_value": row.Role, "new_value": req.Role})); err != nil {
			return err
		}
		return tx.Model(&models.ChatChannelMember{}).Where("id = ?", row.ID).Update("role", req.Role).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "change the role")
	}
	a.notifyChannelChanged(orgID, ch, before, userID, models.ChatEventOwnerChanged)
	return r.SendEnvelope(map[string]any{"role": req.Role})
}

// ---- rename, archive, reopen -------------------------------------------------

// RenameChatChannel renames a free channel. Names may repeat: the id is the identity.
func (a *App) RenameChatChannel(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	var req chatRenameRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > chatMaxNameRunes {
		return a.sendChatError(r, chatBadRequest("name must be 1-100 characters"), "rename the channel")
	}

	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	unchanged := false
	txErr := a.withLockedChannel(orgID, channelID, chatLockUpdate, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		act := a.chatActorIn(tx, orgID, userID, locked)
		if err := act.requireComposer(); err != nil {
			return err
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		if locked.Name == name {
			unchanged = true
			return nil
		}
		old := locked.Name
		if err := a.recordChatChange(tx, orgID, userID, locked, models.ChatEventRenamed,
			models.JSONB{"old_name": old, "new_name": name},
			map[string]any{"field": "name", "old_value": old, "new_value": name}); err != nil {
			return err
		}
		return tx.Model(&models.ChatChannel{}).Where("id = ?", locked.ID).Update("name", name).Error
	})
	if txErr != nil {
		return a.sendChatError(r, txErr, "rename the channel")
	}
	if !unchanged {
		a.notifyChannelChanged(orgID, ch, before, userID, models.ChatEventRenamed)
	}
	return r.SendEnvelope(map[string]any{"name": name})
}

// ArchiveChatChannel closes a free channel: hidden from the normal list, no new
// messages, edits or members, history kept. The owner or a manager may archive.
func (a *App) ArchiveChatChannel(r *fastglue.Request) error {
	return a.setChatArchived(r, true)
}

// ReopenChatChannel reopens an archived channel. Only team_chat:manage.
func (a *App) ReopenChatChannel(r *fastglue.Request) error {
	return a.setChatArchived(r, false)
}

func (a *App) setChatArchived(r *fastglue.Request, archive bool) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	txErr := a.withLockedChannel(orgID, channelID, chatLockUpdate, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		act := a.chatActorIn(tx, orgID, userID, locked)
		if archive {
			if err := act.requireComposer(); err != nil {
				return err
			}
			if locked.ArchivedAt != nil {
				return errChatArchived
			}
		} else {
			// Reopening needs team_chat:manage, not just ownership.
			if !act.canManage {
				if !act.isMember {
					return errChatNotFound
				}
				return errChatForbidden
			}
			if locked.ArchivedAt == nil {
				return &chatHTTPError{http.StatusConflict, "The channel is not archived", "not_archived"}
			}
		}
		event, field := models.ChatEventReopened, "reopened"
		updates := map[string]any{"archived_at": nil, "archived_by_id": nil}
		if archive {
			event, field = models.ChatEventArchived, "archived"
			updates = map[string]any{"archived_at": time.Now(), "archived_by_id": userID}
		}
		if err := a.recordChatChange(tx, orgID, userID, locked, event, nil,
			map[string]any{"field": field, "old_value": nil, "new_value": true}); err != nil {
			return err
		}
		return tx.Model(&models.ChatChannel{}).Where("id = ?", locked.ID).Updates(updates).Error
	})
	if txErr != nil {
		what := "reopen the channel"
		if archive {
			what = "archive the channel"
		}
		return a.sendChatError(r, txErr, what)
	}
	change := models.ChatEventReopened
	if archive {
		change = models.ChatEventArchived
	}
	a.notifyChannelChanged(orgID, ch, before, userID, change)
	return r.SendEnvelope(map[string]any{"archived": archive})
}

// ---- administrative join ------------------------------------------------------

// JoinChatChannel is the only way to enter a private channel without being added:
// it needs team_chat:join_any and a reason. Checks run in this order and the first
// failure ends the request with nothing written: permission (403, which says nothing
// about whether the channel exists), the channel inside the session's organization
// (404), then eligibility (free channel; not archived; not already a member, 409).
// The membership, the system message carrying the reason, and the audit entry commit
// together or not at all; the event goes out after the commit.
func (a *App) JoinChatChannel(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionJoinAny)
	if err != nil {
		return nil
	}
	channelID, err := parsePathUUID(r, "id", "channel")
	if err != nil {
		return nil
	}
	var req chatJoinRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := strings.TrimSpace(req.Reason)

	pre, ok := a.loadChatChannel(orgID, channelID)
	if !ok {
		return a.sendChatError(r, errChatNotFound, "join the channel")
	}
	if err := requireFreeChannel(pre); err != nil {
		return a.sendChatError(r, err, "join the channel")
	}
	if n := utf8.RuneCountInString(reason); n < chatMinReasonRunes || n > chatMaxReasonRunes {
		return a.sendChatError(r, chatBadRequest("reason must be 5-500 characters"), "join the channel")
	}

	before := a.chatRecipientsBefore(orgID, channelID)
	var ch *models.ChatChannel
	txErr := a.withLockedChannel(orgID, channelID, chatLockUpdate, func(tx *gorm.DB, locked *models.ChatChannel) error {
		ch = locked
		if err := requireFreeChannel(locked); err != nil {
			return err
		}
		if locked.ArchivedAt != nil {
			return errChatArchived
		}
		var existing int64
		tx.Model(&models.ChatChannelMember{}).Where("channel_id = ? AND user_id = ?", locked.ID, userID).Count(&existing)
		if existing > 0 {
			return errChatAlready
		}
		if err := a.recordChatChange(tx, orgID, userID, locked, models.ChatEventAdminJoined,
			models.JSONB{"reason": reason},
			map[string]any{
				"field": "admin_join", "old_value": nil, "new_value": userID.String(),
				"reason": reason, "permission": models.ResourceTeamChat + ":" + models.ActionJoinAny,
			}); err != nil {
			return err
		}
		return tx.Create(&models.ChatChannelMember{
			OrganizationID: orgID, ChannelID: locked.ID, UserID: userID, Role: chatRoleMember,
		}).Error
	})
	if txErr != nil {
		return a.sendChatError(r, uniqueToAlready(txErr), "join the channel")
	}
	a.notifyChannelChanged(orgID, ch, before, userID, models.ChatEventAdminJoined)
	return r.SendEnvelope(map[string]any{"joined": true})
}

// ---- administration list -------------------------------------------------------

// ChatAdminChannelResponse is the metadata of a free channel, with its composition
// but never its messages.
type ChatAdminChannelResponse struct {
	ID            uuid.UUID            `json:"id"`
	Name          string               `json:"name"`
	Kind          string               `json:"kind"`
	CreatedAt     time.Time            `json:"created_at"`
	CreatedByID   *uuid.UUID           `json:"created_by_id,omitempty"`
	CreatedByName string               `json:"created_by_name,omitempty"`
	ArchivedAt    *time.Time           `json:"archived_at,omitempty"`
	Owners        []ChatMemberResponse `json:"owners"`
	Members       []ChatMemberResponse `json:"members"`
}

// ListChatAdminChannels is the administration view: composition and state of free
// channels. Only free channels: DMs and team channels never appear (the people in a
// DM would reveal who talks to whom). No messages, previews, unread counts or search
// results, so a channel can be administered without being read.
func (a *App) ListChatAdminChannels(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionManage)
	if err != nil {
		return nil
	}
	var channels []models.ChatChannel
	if err := a.DB.Where("organization_id = ? AND kind = ?", orgID, models.ChatChannelKindChannel).
		Order("created_at DESC").Limit(chatAdminListCap).Find(&channels).Error; err != nil {
		a.Log.Error("Failed to list chat admin channels", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list channels", nil, "")
	}
	ids := make([]uuid.UUID, len(channels))
	var creators []uuid.UUID
	for i, c := range channels {
		ids[i] = c.ID
		if c.CreatedByID != nil {
			creators = append(creators, *c.CreatedByID)
		}
	}
	byChannel := map[uuid.UUID][]models.ChatChannelMember{}
	if len(ids) > 0 {
		var rows []models.ChatChannelMember
		a.DB.Where("organization_id = ? AND channel_id IN ?", orgID, ids).Find(&rows)
		for _, m := range rows {
			byChannel[m.ChannelID] = append(byChannel[m.ChannelID], m)
		}
	}
	creatorNames := a.chatUserNames(creators)
	out := make([]ChatAdminChannelResponse, len(channels))
	for i, c := range channels {
		members := a.chatMemberResponses(orgID, byChannel[c.ID])
		owners := []ChatMemberResponse{}
		for _, m := range members {
			if m.Role == chatRoleOwner {
				owners = append(owners, m)
			}
		}
		resp := ChatAdminChannelResponse{
			ID: c.ID, Name: c.Name, Kind: string(c.Kind), CreatedAt: c.CreatedAt,
			CreatedByID: c.CreatedByID, ArchivedAt: c.ArchivedAt, Owners: owners, Members: members,
		}
		if c.CreatedByID != nil {
			resp.CreatedByName = creatorNames[*c.CreatedByID]
		}
		out[i] = resp
	}
	return r.SendEnvelope(map[string]any{"channels": out})
}
