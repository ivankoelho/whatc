package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Phase A (channel management). The fixture seeds rows directly so the routes under
// test are the only thing that changes state.

type phaseA struct {
	app     *handlers.App
	org     *models.Organization
	other   *models.Organization
	admin   *models.User // team_chat:* incl. manage, join_any
	manager *models.User // manage, no join_any
	owner   *models.User // agent, owner of ch
	member  *models.User // agent, plain member of ch
	spare   *models.User // agent, in no channel
	foreign *models.User // admin of another organization
	noPerm  *models.User // role without team_chat
	readOnl *models.User // team_chat:read only
	ch      *models.ChatChannel
}

func newPhaseA(t *testing.T) *phaseA {
	t.Helper()
	app := newTestApp(t)
	f := &phaseA{app: app}
	f.org = testutil.CreateTestOrganization(t, app.DB)
	f.other = testutil.CreateTestOrganization(t, app.DB)
	f.admin = chatUser(t, app, f.org.ID, teamChatKeysOf("admin"))
	f.manager = chatUser(t, app, f.org.ID, teamChatKeysOf("manager"))
	f.owner = chatUser(t, app, f.org.ID, teamChatKeysOf("agent"))
	f.member = chatUser(t, app, f.org.ID, teamChatKeysOf("agent"))
	f.spare = chatUser(t, app, f.org.ID, teamChatKeysOf("agent"))
	f.foreign = chatUser(t, app, f.other.ID, teamChatKeysOf("admin"))
	f.noPerm = chatUser(t, app, f.org.ID, []string{"chat:read"})
	f.readOnl = chatUser(t, app, f.org.ID, []string{"team_chat:read"})
	f.ch = f.freeChannel(t, "ops", f.owner, f.member)
	return f
}

// freeChannel seeds a free channel owned by owner with the given extra members.
func (f *phaseA) freeChannel(t *testing.T, name string, owner *models.User, members ...*models.User) *models.ChatChannel {
	t.Helper()
	ch := &models.ChatChannel{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: owner.OrganizationID,
		Kind: models.ChatChannelKindChannel, Name: name, CreatedByID: &owner.ID,
	}
	require.NoError(t, f.app.DB.Create(ch).Error)
	require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{
		OrganizationID: ch.OrganizationID, ChannelID: ch.ID, UserID: owner.ID, Role: "owner"}).Error)
	for _, m := range members {
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{
			OrganizationID: ch.OrganizationID, ChannelID: ch.ID, UserID: m.ID, Role: "member"}).Error)
	}
	return ch
}

func (f *phaseA) path(ch *models.ChatChannel, extra ...string) map[string]string {
	p := map[string]string{"id": ch.ID.String()}
	if len(extra) > 0 {
		p["member_id"] = extra[0]
	}
	return p
}

func errorCode(raw []byte) string {
	var e struct {
		ErrorType string `json:"error_type"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.ErrorType
}

func (f *phaseA) isMember(t *testing.T, ch *models.ChatChannel, u *models.User) bool {
	t.Helper()
	var n int64
	f.app.DB.Model(&models.ChatChannelMember{}).Where("channel_id = ? AND user_id = ?", ch.ID, u.ID).Count(&n)
	return n > 0
}

func (f *phaseA) systemMessages(t *testing.T, ch *models.ChatChannel) []models.ChatMessage {
	t.Helper()
	var msgs []models.ChatMessage
	require.NoError(t, f.app.DB.Where("channel_id = ? AND kind = ?", ch.ID, models.ChatMessageKindSystem).
		Order("created_at ASC, id ASC").Find(&msgs).Error)
	return msgs
}

func (f *phaseA) audits(t *testing.T, ch *models.ChatChannel) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	require.NoError(t, f.app.DB.Where("resource_type = ? AND resource_id = ?", "chat_channel", ch.ID).
		Order("created_at ASC").Find(&rows).Error)
	return rows
}

func (f *phaseA) reload(t *testing.T, ch *models.ChatChannel) *models.ChatChannel {
	t.Helper()
	var fresh models.ChatChannel
	require.NoError(t, f.app.DB.First(&fresh, "id = ?", ch.ID).Error)
	return &fresh
}

func (f *phaseA) call(t *testing.T, h func(*fastglue.Request) error, org *models.Organization, u *models.User, body any, path map[string]string, q ...map[string]string) (int, []byte) {
	t.Helper()
	var query map[string]string
	if len(q) > 0 {
		query = q[0]
	}
	return chatDo(t, h, org.ID, u.ID, body, path, query)
}

// Criteria 1 and 13: a manager changes WHO is in a channel they are not in, never
// puts themselves in, and learns nothing of what is said there.
func TestPhaseA_ManagerComposesWithoutReading(t *testing.T) {
	f := newPhaseA(t)
	cs := chatHub(t, f.app, f.org.ID, f.owner, f.member, f.spare, f.manager)

	code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.manager, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.True(t, f.isMember(t, f.ch, f.spare))
	for _, id := range []uuid.UUID{f.owner.ID, f.member.ID, f.spare.ID} {
		ev := expectEvent(t, cs[id], ws.TypeTeamChatChannelUpdated)
		assert.Equal(t, f.ch.ID, ev.ChannelID)
		assert.Equal(t, "member_added", ev.Change)
	}
	assertNoBroadcast(t, cs[f.manager.ID]) // not a member: no event

	code, _ = f.call(t, f.app.RemoveChatChannelMember, f.org, f.manager, nil, f.path(f.ch, f.spare.ID.String()))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.False(t, f.isMember(t, f.ch, f.spare))

	// Never themselves, by this route.
	before := snapshotChat(t, f.app, f.org.ID)
	code, raw := f.call(t, f.app.AddChatChannelMember, f.org, f.manager, map[string]any{"user_id": f.manager.ID}, f.path(f.ch))
	assert.Equal(t, fasthttp.StatusForbidden, code)
	assert.Equal(t, "self_add", errorCode(raw))
	assert.False(t, f.isMember(t, f.ch, f.manager))
	assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), "nothing written")

	// And no reading, typing or events for someone who is not in it.
	code, _ = f.call(t, f.app.ListChatMessages, f.org, f.manager, nil, f.path(f.ch))
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = f.call(t, f.app.ListChatChannelMembers, f.org, f.manager, nil, f.path(f.ch))
	assert.Equal(t, fasthttp.StatusNotFound, code, "the member list is for members; managers use the administration list")
	code, _ = f.call(t, f.app.NotifyChatTyping, f.org, f.manager, map[string]any{}, f.path(f.ch))
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

// Criterion 13: admin and super admin (who also hold manage) cannot put themselves
// in through the members route either.
func TestPhaseA_NobodyAddsThemselvesThroughTheMembersRoute(t *testing.T) {
	f := newPhaseA(t)
	super := chatUser(t, f.app, f.org.ID, nil)
	require.NoError(t, f.app.DB.Model(super).Update("is_super_admin", true).Error)

	for name, u := range map[string]*models.User{"admin": f.admin, "super admin": super, "manager": f.manager} {
		before := snapshotChat(t, f.app, f.org.ID)
		code, raw := f.call(t, f.app.AddChatChannelMember, f.org, u, map[string]any{"user_id": u.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
		assert.Equal(t, "self_add", errorCode(raw), name)
		assert.False(t, f.isMember(t, f.ch, u), name)
		assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), name+": nothing written")
		assert.Empty(t, f.systemMessages(t, f.ch), name)
		assert.Empty(t, f.audits(t, f.ch), name)
	}
}

// Criteria 2, 15, 18, 20: the administrative join.
func TestPhaseA_AdminJoin(t *testing.T) {
	f := newPhaseA(t)
	// History that exists before the admin arrives.
	for _, body := range []string{"before 1", "before 2"} {
		code, _ := sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, body)
		require.Equal(t, fasthttp.StatusOK, code)
	}
	cs := chatHub(t, f.app, f.org.ID, f.owner, f.member, f.admin, f.manager)
	const reason = "Investigating the complaint opened by the customer <b>#1042</b>"

	code, raw := f.call(t, f.app.JoinChatChannel, f.org, f.admin, map[string]any{"reason": reason}, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.True(t, f.isMember(t, f.ch, f.admin))

	t.Run("system message carries who and why, visible to the members", func(t *testing.T) {
		sys := f.systemMessages(t, f.ch)
		require.Len(t, sys, 1)
		assert.Equal(t, "admin_joined", sys[0].Meta["event"])
		assert.Equal(t, reason, sys[0].Meta["reason"])
		assert.Equal(t, f.admin.ID.String(), sys[0].Meta["actor_id"])

		code, raw := f.call(t, f.app.ListChatMessages, f.org, f.member, nil, f.path(f.ch))
		require.Equal(t, fasthttp.StatusOK, code)
		msgs := decodeChat[chatMessagesData](t, raw).Messages
		last := msgs[len(msgs)-1]
		assert.Equal(t, models.ChatMessageKindSystem, last.Kind)
		assert.Equal(t, reason, last.Meta["reason"], "returned as text, to be rendered escaped")
		assert.Empty(t, last.Body)
	})

	t.Run("audit entry has actor, reason and the permission used", func(t *testing.T) {
		rows := f.audits(t, f.ch)
		require.Len(t, rows, 1)
		assert.Equal(t, f.admin.ID, rows[0].UserID)
		blob, _ := json.Marshal(rows[0].Changes)
		assert.Contains(t, string(blob), "admin_join")
		assert.Contains(t, string(blob), "Investigating the complaint")
		assert.Contains(t, string(blob), "team_chat:join_any")
	})

	t.Run("the event reaches the members and carries ids and a change code only", func(t *testing.T) {
		for _, id := range []uuid.UUID{f.owner.ID, f.member.ID, f.admin.ID} {
			msg := readBroadcast(t, cs[id], ws.TypeTeamChatChannelUpdated)
			payload, ok := msg.Payload.(map[string]any)
			require.True(t, ok)
			keys := []string{}
			for k := range payload {
				keys = append(keys, k)
			}
			assert.ElementsMatch(t, []string{"channel_id", "user_id", "at", "change"}, keys)
			assert.Equal(t, "admin_joined", payload["change"])
			blob, _ := json.Marshal(payload)
			assert.NotContains(t, string(blob), "Investigating", "the reason never travels in the event")
			assert.NotContains(t, string(blob), "complaint")
		}
		assertNoBroadcast(t, cs[f.manager.ID]) // not in the channel
	})

	t.Run("full history is readable, joining marks nothing read and old messages are not unread", func(t *testing.T) {
		code, raw := f.call(t, f.app.ListChatMessages, f.org, f.admin, nil, f.path(f.ch))
		require.Equal(t, fasthttp.StatusOK, code)
		bodies := []string{}
		for _, m := range decodeChat[chatMessagesData](t, raw).Messages {
			bodies = append(bodies, m.Body)
		}
		assert.Contains(t, bodies, "before 1")
		assert.Contains(t, bodies, "before 2")

		var reads int64
		f.app.DB.Model(&models.ChatRead{}).Where("channel_id = ? AND user_id = ?", f.ch.ID, f.admin.ID).Count(&reads)
		assert.Zero(t, reads, "entering writes no read marker")

		for _, c := range listChannels(t, f.app, f.org.ID, f.admin.ID) {
			if c.ID == f.ch.ID {
				assert.Zero(t, c.UnreadCount, "the earlier messages do not count as unread")
			}
		}
		_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, "after")
		for _, c := range listChannels(t, f.app, f.org.ID, f.admin.ID) {
			if c.ID == f.ch.ID {
				assert.EqualValues(t, 1, c.UnreadCount, "later messages count normally")
			}
		}
	})

	t.Run("a second entry is a 409 that writes nothing", func(t *testing.T) {
		sysBefore, auditBefore := len(f.systemMessages(t, f.ch)), len(f.audits(t, f.ch))
		code, raw := f.call(t, f.app.JoinChatChannel, f.org, f.admin, map[string]any{"reason": "again, for good measure"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "already_member", errorCode(raw))
		assert.Len(t, f.systemMessages(t, f.ch), sysBefore)
		assert.Len(t, f.audits(t, f.ch), auditBefore)
	})
}

// Criteria 2 and 18: who may not join, and in what order the checks answer.
func TestPhaseA_AdminJoinChecksInOrder(t *testing.T) {
	f := newPhaseA(t)
	body := map[string]any{"reason": "a perfectly good reason"}
	custom := chatUser(t, f.app, f.org.ID, []string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"})
	unknown := &models.ChatChannel{BaseModel: models.BaseModel{ID: uuid.New()}}

	t.Run("without join_any the answer is 403 whether or not the channel exists", func(t *testing.T) {
		for name, u := range map[string]*models.User{"manager": f.manager, "agent": f.spare, "custom role with manage+delete": custom, "read-only": f.readOnl} {
			before := snapshotChat(t, f.app, f.org.ID)
			code, _ := f.call(t, f.app.JoinChatChannel, f.org, u, body, f.path(f.ch))
			assert.Equal(t, fasthttp.StatusForbidden, code, name+" / existing channel")
			code, _ = f.call(t, f.app.JoinChatChannel, f.org, u, body, f.path(unknown))
			assert.Equal(t, fasthttp.StatusForbidden, code, name+" / unknown channel: no existence leak")
			assert.False(t, f.isMember(t, f.ch, u), name)
			assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), name+": nothing written")
		}
		assert.Empty(t, f.systemMessages(t, f.ch))
		assert.Empty(t, f.audits(t, f.ch))
	})

	t.Run("an admin of another organization is told the channel does not exist", func(t *testing.T) {
		code, _ := f.call(t, f.app.JoinChatChannel, f.other, f.foreign, body, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code)
		assert.False(t, f.isMember(t, f.ch, f.foreign))
	})

	t.Run("only free channels: never a DM, never a team channel", func(t *testing.T) {
		dm := chatDM(t, f, f.owner, f.member)
		team := createTeamWithMember(t, f.app, f.org.ID, f.owner.ID)
		teamCh := newChatChannel(t, f.app, f.org.ID, models.ChatChannelKindTeam, &team.ID)
		for name, ch := range map[string]*models.ChatChannel{"dm": dm, "team": teamCh} {
			code, _ := f.call(t, f.app.JoinChatChannel, f.org, f.admin, body, f.path(ch))
			assert.Equal(t, fasthttp.StatusNotFound, code, name)
			assert.False(t, f.isMember(t, ch, f.admin), name)
		}
	})

	t.Run("a reason of 5 to 500 characters is required", func(t *testing.T) {
		for name, reason := range map[string]string{"missing": "", "blank": "      ", "too short": " abcd ", "too long": strings.Repeat("x", 501)} {
			code, _ := f.call(t, f.app.JoinChatChannel, f.org, f.admin, map[string]any{"reason": reason}, f.path(f.ch))
			assert.Equal(t, fasthttp.StatusBadRequest, code, name)
		}
		assert.False(t, f.isMember(t, f.ch, f.admin))
		// The limits themselves are accepted, spaces trimmed first.
		code, _ := f.call(t, f.app.JoinChatChannel, f.org, f.admin, map[string]any{"reason": "  " + strings.Repeat("y", 5) + "  "}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("an archived channel answers 409", func(t *testing.T) {
		archived := f.freeChannel(t, "old", f.owner)
		require.NoError(t, f.app.DB.Model(archived).Update("archived_at", gorm.Expr("now()")).Error)
		code, raw := f.call(t, f.app.JoinChatChannel, f.org, f.admin, body, f.path(archived))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "archived", errorCode(raw))
		assert.False(t, f.isMember(t, archived, f.admin))
	})
}

func chatDM(t *testing.T, f *phaseA, a, b *models.User) *models.ChatChannel {
	t.Helper()
	low, high := models.CanonicalDMPair(a.ID, b.ID)
	dm := &models.ChatChannel{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Kind: models.ChatChannelKindDM,
		DMUserLowID: &low, DMUserHighID: &high,
	}
	require.NoError(t, f.app.DB.Create(dm).Error)
	for _, u := range []*models.User{a, b} {
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{
			OrganizationID: f.org.ID, ChannelID: dm.ID, UserID: u.ID, Role: "member"}).Error)
	}
	return dm
}

// Criterion 3: a DM and a team channel accept none of the management operations,
// from anyone, however privileged.
func TestPhaseA_DMAndTeamChannelsAreNotManageable(t *testing.T) {
	f := newPhaseA(t)
	dm := chatDM(t, f, f.owner, f.member)
	team := createTeamWithMember(t, f.app, f.org.ID, f.owner.ID)
	teamCh := newChatChannel(t, f.app, f.org.ID, models.ChatChannelKindTeam, &team.ID)

	type op struct {
		name string
		h    func(*fastglue.Request) error
		body any
		uid  string
	}
	ops := func(target uuid.UUID) []op {
		return []op{
			{"add member", f.app.AddChatChannelMember, map[string]any{"user_id": target}, ""},
			{"remove member", f.app.RemoveChatChannelMember, nil, target.String()},
			{"leave", f.app.LeaveChatChannel, nil, ""},
			{"change role", f.app.SetChatMemberRole, map[string]any{"role": "owner"}, target.String()},
			{"rename", f.app.RenameChatChannel, map[string]any{"name": "new name"}, ""},
			{"archive", f.app.ArchiveChatChannel, nil, ""},
			{"reopen", f.app.ReopenChatChannel, nil, ""},
			{"join", f.app.JoinChatChannel, map[string]any{"reason": "some valid reason"}, ""},
		}
	}
	for chName, ch := range map[string]*models.ChatChannel{"dm": dm, "team": teamCh} {
		for _, actor := range []*models.User{f.admin, f.manager, f.owner} {
			for _, o := range ops(f.spare.ID) {
				before := snapshotChat(t, f.app, f.org.ID)
				p := f.path(ch)
				if o.uid != "" {
					p = f.path(ch, o.uid)
				}
				code, _ := f.call(t, o.h, f.org, actor, o.body, p)
				// The administrative join answers permission first (403 without join_any,
				// whatever the channel is); only an admin gets as far as "does not exist".
				want := fasthttp.StatusNotFound
				if o.name == "join" && actor != f.admin {
					want = fasthttp.StatusForbidden
				}
				assert.Equal(t, want, code, chName+" / "+o.name)
				assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), chName+" / "+o.name+": nothing written")
			}
		}
		assert.Empty(t, f.systemMessages(t, ch), chName)
		assert.Empty(t, f.audits(t, ch), chName)
	}
	fresh := f.reload(t, dm)
	assert.Nil(t, fresh.ArchivedAt)
	assert.Equal(t, "", fresh.Name)
}

// Criterion 4: every route, with an id from another organization, answers 404, and a
// target from another organization (or an inactive one) is refused.
func TestPhaseA_OrganizationIsolation(t *testing.T) {
	f := newPhaseA(t)
	foreignMember := chatUser(t, f.app, f.other.ID, teamChatKeysOf("agent"))
	inactive := chatUser(t, f.app, f.org.ID, teamChatKeysOf("agent"))
	require.NoError(t, f.app.DB.Model(inactive).Update("is_active", false).Error)

	type op struct {
		name string
		h    func(*fastglue.Request) error
		body any
		uid  string
	}
	ops := []op{
		{"add member", f.app.AddChatChannelMember, map[string]any{"user_id": foreignMember.ID}, ""},
		{"remove member", f.app.RemoveChatChannelMember, nil, f.member.ID.String()},
		{"leave", f.app.LeaveChatChannel, nil, ""},
		{"change role", f.app.SetChatMemberRole, map[string]any{"role": "owner"}, f.member.ID.String()},
		{"rename", f.app.RenameChatChannel, map[string]any{"name": "taken"}, ""},
		{"archive", f.app.ArchiveChatChannel, nil, ""},
		{"reopen", f.app.ReopenChatChannel, nil, ""},
		{"join", f.app.JoinChatChannel, map[string]any{"reason": "a valid reason"}, ""},
		{"members list", f.app.ListChatChannelMembers, nil, ""},
	}
	before := snapshotChat(t, f.app, f.org.ID, f.other.ID)
	for _, o := range ops {
		p := f.path(f.ch)
		if o.uid != "" {
			p = f.path(f.ch, o.uid)
		}
		code, _ := f.call(t, o.h, f.other, f.foreign, o.body, p)
		assert.Equal(t, fasthttp.StatusNotFound, code, o.name)
	}
	assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID, f.other.ID), "nothing written by the foreign admin")

	for name, target := range map[string]*models.User{"other organization": foreignMember, "inactive": inactive} {
		code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": target.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code, name)
		assert.False(t, f.isMember(t, f.ch, target), name)
	}
	assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID, f.other.ID))
}

// Criteria 5 and 20: removing someone cuts access at once (REST and events); the
// history stays; and everybody learns of it, the removed person included.
func TestPhaseA_RemovalRevokesAccessImmediately(t *testing.T) {
	f := newPhaseA(t)
	_, _ = sendMsg(t, f.app, f.org.ID, f.member.ID, f.ch.ID, "written by the member")
	cs := chatHub(t, f.app, f.org.ID, f.owner, f.member)
	msgsBefore := snapshotChat(t, f.app, f.org.ID).messages

	code, _ := f.call(t, f.app.RemoveChatChannelMember, f.org, f.owner, nil, f.path(f.ch, f.member.ID.String()))
	require.Equal(t, fasthttp.StatusOK, code)

	// The removed person is told (the event is computed before the change).
	assert.Equal(t, "member_removed", expectEvent(t, cs[f.member.ID], ws.TypeTeamChatChannelUpdated).Change)
	expectEvent(t, cs[f.owner.ID], ws.TypeTeamChatChannelUpdated)

	for name, h := range map[string]func(*fastglue.Request) error{
		"list messages": f.app.ListChatMessages, "mark read": f.app.MarkChatChannelRead,
		"typing": f.app.NotifyChatTyping, "members": f.app.ListChatChannelMembers,
	} {
		code, _ := f.call(t, h, f.org, f.member, map[string]any{}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code, name)
	}
	code, _ = sendMsg(t, f.app, f.org.ID, f.member.ID, f.ch.ID, "still here?")
	assert.Equal(t, fasthttp.StatusNotFound, code)

	// No more events for them, and the history (their own messages too) stays.
	_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, "after removal")
	expectEvent(t, cs[f.owner.ID], ws.TypeTeamChatMessage)
	assertNoBroadcast(t, cs[f.member.ID])
	assert.Equal(t, msgsBefore+2 /* the system message and the new one */, snapshotChat(t, f.app, f.org.ID).messages)
	code, raw := f.call(t, f.app.ListChatMessages, f.org, f.owner, nil, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	found := false
	for _, m := range decodeChat[chatMessagesData](t, raw).Messages {
		if m.Body == "written by the member" {
			found = true
			assert.Equal(t, f.member.ID, m.UserID, "authorship is kept")
		}
	}
	assert.True(t, found)
}

// Criteria 6 and 11: the last owner.
func TestPhaseA_LastOwner(t *testing.T) {
	f := newPhaseA(t)
	_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, "my message")

	t.Run("cannot leave, remove themselves or demote themselves", func(t *testing.T) {
		before := snapshotChat(t, f.app, f.org.ID)
		code, raw := f.call(t, f.app.LeaveChatChannel, f.org, f.owner, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "last_owner", errorCode(raw))
		code, raw = f.call(t, f.app.RemoveChatChannelMember, f.org, f.owner, nil, f.path(f.ch, f.owner.ID.String()))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "last_owner", errorCode(raw))
		code, raw = f.call(t, f.app.SetChatMemberRole, f.org, f.owner, map[string]any{"role": "member"}, f.path(f.ch, f.owner.ID.String()))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "last_owner", errorCode(raw))
		assert.True(t, f.isMember(t, f.ch, f.owner))
		assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), "nothing changed")
	})

	t.Run("after promoting someone else the owner can leave, and nothing is deleted", func(t *testing.T) {
		code, _ := f.call(t, f.app.SetChatMemberRole, f.org, f.owner, map[string]any{"role": "owner"}, f.path(f.ch, f.member.ID.String()))
		require.Equal(t, fasthttp.StatusOK, code)
		msgs := snapshotChat(t, f.app, f.org.ID).messages
		code, _ = f.call(t, f.app.LeaveChatChannel, f.org, f.owner, nil, f.path(f.ch))
		require.Equal(t, fasthttp.StatusOK, code)
		assert.False(t, f.isMember(t, f.ch, f.owner))
		assert.Equal(t, msgs+1 /* the system message */, snapshotChat(t, f.app, f.org.ID).messages, "leaving deletes no message")
		// ...and the new sole owner is now the last one.
		code, _ = f.call(t, f.app.LeaveChatChannel, f.org, f.member, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
	})

	t.Run("someone else who manages may still remove the last owner; the channel then has none", func(t *testing.T) {
		g := newPhaseA(t)
		code, _ := g.call(t, g.app.RemoveChatChannelMember, g.org, g.manager, nil, g.path(g.ch, g.owner.ID.String()))
		require.Equal(t, fasthttp.StatusOK, code)
		assert.False(t, g.isMember(t, g.ch, g.owner))
		// Administration stays with whoever has team_chat:manage.
		code, _ = g.call(t, g.app.SetChatMemberRole, g.org, g.manager, map[string]any{"role": "owner"}, g.path(g.ch, g.member.ID.String()))
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("leaving a channel that has several owners works", func(t *testing.T) {
		g := newPhaseA(t)
		require.NoError(t, g.app.DB.Model(&models.ChatChannelMember{}).
			Where("channel_id = ? AND user_id = ?", g.ch.ID, g.member.ID).Update("role", "owner").Error)
		code, _ := g.call(t, g.app.LeaveChatChannel, g.org, g.owner, nil, g.path(g.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("a plain member can always leave", func(t *testing.T) {
		g := newPhaseA(t)
		code, _ := g.call(t, g.app.LeaveChatChannel, g.org, g.member, nil, g.path(g.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
		assert.False(t, g.isMember(t, g.ch, g.member))
	})
}

// Criterion 7: archiving.
func TestPhaseA_Archive(t *testing.T) {
	f := newPhaseA(t)
	_, msg := sendMsg(t, f.app, f.org.ID, f.member.ID, f.ch.ID, "to be read later")

	code, _ := f.call(t, f.app.ArchiveChatChannel, f.org, f.owner, nil, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	fresh := f.reload(t, f.ch)
	require.NotNil(t, fresh.ArchivedAt)
	assert.Equal(t, f.owner.ID, *fresh.ArchivedByID)

	t.Run("hidden from the normal list, shown in the archived one", func(t *testing.T) {
		for _, c := range listChannels(t, f.app, f.org.ID, f.member.ID) {
			assert.NotEqual(t, f.ch.ID, c.ID)
		}
		code, raw := chatDo(t, f.app.ListChatChannels, f.org.ID, f.member.ID, nil, nil, map[string]string{"archived": "true"})
		require.Equal(t, fasthttp.StatusOK, code)
		got := decodeChat[chatChannelsData](t, raw).Channels
		require.Len(t, got, 1)
		assert.Equal(t, f.ch.ID, got[0].ID)
		assert.NotNil(t, got[0].ArchivedAt)
		// Someone outside never sees it, archived or not.
		code, raw = chatDo(t, f.app.ListChatChannels, f.org.ID, f.spare.ID, nil, nil, map[string]string{"archived": "true"})
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Empty(t, decodeChat[chatChannelsData](t, raw).Channels)
	})

	t.Run("closed to messages, edits, typing, new members, renames and entries", func(t *testing.T) {
		before := snapshotChat(t, f.app, f.org.ID)
		code, raw := f.call(t, f.app.SendChatMessage, f.org, f.member, map[string]any{"body": "x"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Equal(t, "archived", errorCode(raw))
		code, _ = f.call(t, f.app.UpdateChatMessage, f.org, f.member, map[string]any{"body": "edited"}, map[string]string{"id": msg.ID.String()})
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, _ = f.call(t, f.app.NotifyChatTyping, f.org, f.member, map[string]any{}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, _ = f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, _ = f.call(t, f.app.RenameChatChannel, f.org, f.owner, map[string]any{"name": "renamed"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, _ = f.call(t, f.app.JoinChatChannel, f.org, f.admin, map[string]any{"reason": "a valid reason"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, _ = f.call(t, f.app.ArchiveChatChannel, f.org, f.owner, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code, "archiving twice")
		assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID), "nothing written")
		var stored models.ChatMessage
		require.NoError(t, f.app.DB.First(&stored, "id = ?", msg.ID).Error)
		assert.Equal(t, "to be read later", stored.Body)
	})

	t.Run("members can still read, mark read and delete", func(t *testing.T) {
		code, raw := f.call(t, f.app.ListChatMessages, f.org, f.owner, nil, f.path(f.ch))
		require.Equal(t, fasthttp.StatusOK, code)
		assert.NotEmpty(t, decodeChat[chatMessagesData](t, raw).Messages)
		code, _ = f.call(t, f.app.MarkChatChannelRead, f.org, f.owner, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
		code, _ = f.call(t, f.app.DeleteChatMessage, f.org, f.member, nil, map[string]string{"id": msg.ID.String()})
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("only team_chat:manage reopens, and sending works again", func(t *testing.T) {
		code, _ := f.call(t, f.app.ReopenChatChannel, f.org, f.owner, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusForbidden, code, "an owner without manage cannot reopen")
		code, _ = f.call(t, f.app.ReopenChatChannel, f.org, f.spare, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code, "an outsider without manage is told it does not exist")
		assert.NotNil(t, f.reload(t, f.ch).ArchivedAt)

		// A manager who is not even a member can reopen it (administration, not reading).
		code, _ = f.call(t, f.app.ReopenChatChannel, f.org, f.manager, nil, f.path(f.ch))
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Nil(t, f.reload(t, f.ch).ArchivedAt)
		code, _ = f.call(t, f.app.ReopenChatChannel, f.org, f.manager, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code, "not archived")
		code, _ = sendMsg(t, f.app, f.org.ID, f.member.ID, f.ch.ID, "back")
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("a manager who is not a member can archive without reading", func(t *testing.T) {
		g := newPhaseA(t)
		code, _ := g.call(t, g.app.ArchiveChatChannel, g.org, g.manager, nil, g.path(g.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
		code, _ = g.call(t, g.app.ListChatMessages, g.org, g.manager, nil, g.path(g.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code)
	})
}

// Criteria 8 and 16: the administration list is composition and state, nothing else.
func TestPhaseA_AdminListIsMetadataOnly(t *testing.T) {
	f := newPhaseA(t)
	_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, "a secret sentence about pricing")
	dm := chatDM(t, f, f.owner, f.member)
	team := createTeamWithMember(t, f.app, f.org.ID, f.owner.ID)
	teamCh := newChatChannel(t, f.app, f.org.ID, models.ChatChannelKindTeam, &team.ID)
	archived := f.freeChannel(t, "archived one", f.owner)
	require.NoError(t, f.app.DB.Model(archived).Update("archived_at", gorm.Expr("now()")).Error)
	foreignCh := f.freeChannel(t, "other org channel", f.foreign)
	_ = foreignCh

	code, raw := f.call(t, f.app.ListChatAdminChannels, f.org, f.manager, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	body := string(raw)
	for _, forbidden := range []string{"a secret sentence", "\"body\"", "unread", "preview", "last_message", "\"messages\""} {
		assert.NotContains(t, body, forbidden)
	}
	var res struct {
		Data struct {
			Channels []handlers.ChatAdminChannelResponse `json:"channels"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &res))
	ids := map[uuid.UUID]handlers.ChatAdminChannelResponse{}
	for _, c := range res.Data.Channels {
		ids[c.ID] = c
		assert.Equal(t, "channel", c.Kind)
	}
	assert.Contains(t, ids, f.ch.ID)
	assert.Contains(t, ids, archived.ID, "archived free channels are listed for administration")
	assert.NotContains(t, ids, dm.ID, "a DM never appears")
	assert.NotContains(t, ids, teamCh.ID, "a team channel never appears")
	assert.NotContains(t, ids, foreignCh.ID, "another organization's channel never appears")

	got := ids[f.ch.ID]
	assert.Equal(t, "ops", got.Name)
	require.Len(t, got.Owners, 1)
	assert.Equal(t, f.owner.ID, got.Owners[0].UserID)
	assert.Len(t, got.Members, 2)
	assert.NotNil(t, ids[archived.ID].ArchivedAt)

	// Composition without membership: the manager sees it but is still not in it.
	assert.False(t, f.isMember(t, f.ch, f.manager))
	for name, u := range map[string]*models.User{"agent": f.spare, "read-only": f.readOnl, "no team_chat": f.noPerm} {
		code, _ := f.call(t, f.app.ListChatAdminChannels, f.org, u, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
	}
}

// Criterion 10: the id is the identity; same-named channels never interfere.
func TestPhaseA_SameNameChannelsAreIndependent(t *testing.T) {
	f := newPhaseA(t)
	twin := f.freeChannel(t, "ops", f.owner, f.member) // same name, same people

	code, _ := f.call(t, f.app.RenameChatChannel, f.org, f.owner, map[string]any{"name": "ops renamed"}, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "ops renamed", f.reload(t, f.ch).Name)
	assert.Equal(t, "ops", f.reload(t, twin).Name)

	code, _ = f.call(t, f.app.ArchiveChatChannel, f.org, f.owner, nil, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.NotNil(t, f.reload(t, f.ch).ArchivedAt)
	assert.Nil(t, f.reload(t, twin).ArchivedAt)

	code, _ = f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": f.spare.ID}, f.path(twin))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.True(t, f.isMember(t, twin, f.spare))
	assert.False(t, f.isMember(t, f.ch, f.spare))

	// Two channels with the same name coexist in the list, told apart by id (and by creator/members).
	names := 0
	for _, c := range listChannels(t, f.app, f.org.ID, f.owner.ID) {
		if c.Name == "ops" {
			names++
			assert.Equal(t, twin.ID, c.ID)
			assert.EqualValues(t, 3, c.MemberCount)
			assert.Equal(t, "owner", c.MyRole)
		}
	}
	assert.Equal(t, 1, names)
}

// Roles and permissions on the member operations, in one place.
func TestPhaseA_WhoMayDoWhat(t *testing.T) {
	f := newPhaseA(t)

	t.Run("a plain member cannot manage the composition", func(t *testing.T) {
		before := snapshotChat(t, f.app, f.org.ID)
		code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.member, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusForbidden, code)
		code, _ = f.call(t, f.app.RemoveChatChannelMember, f.org, f.member, nil, f.path(f.ch, f.owner.ID.String()))
		assert.Equal(t, fasthttp.StatusForbidden, code)
		code, _ = f.call(t, f.app.SetChatMemberRole, f.org, f.member, map[string]any{"role": "owner"}, f.path(f.ch, f.member.ID.String()))
		assert.Equal(t, fasthttp.StatusForbidden, code)
		code, _ = f.call(t, f.app.RenameChatChannel, f.org, f.member, map[string]any{"name": "mine now"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusForbidden, code)
		code, _ = f.call(t, f.app.ArchiveChatChannel, f.org, f.member, nil, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusForbidden, code)
		assert.Equal(t, before, snapshotChat(t, f.app, f.org.ID))
	})

	t.Run("someone outside without manage is told the channel does not exist", func(t *testing.T) {
		for name, h := range map[string]func(*fastglue.Request) error{
			"rename": f.app.RenameChatChannel, "archive": f.app.ArchiveChatChannel, "members": f.app.ListChatChannelMembers,
		} {
			code, _ := f.call(t, h, f.org, f.spare, map[string]any{"name": "x"}, f.path(f.ch))
			assert.Equal(t, fasthttp.StatusNotFound, code, name)
		}
		code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.spare, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusNotFound, code)
	})

	t.Run("roles without team_chat cannot use any of it", func(t *testing.T) {
		for name, u := range map[string]*models.User{"no team_chat": f.noPerm} {
			for _, h := range []func(*fastglue.Request) error{f.app.AddChatChannelMember, f.app.RenameChatChannel, f.app.ArchiveChatChannel, f.app.LeaveChatChannel} {
				code, _ := f.call(t, h, f.org, u, map[string]any{"user_id": f.spare.ID, "name": "x"}, f.path(f.ch))
				assert.Equal(t, fasthttp.StatusForbidden, code, name)
			}
		}
	})

	t.Run("the owner manages, the manager manages, validation applies", func(t *testing.T) {
		code, _ := f.call(t, f.app.RenameChatChannel, f.org, f.owner, map[string]any{"name": "  "}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusBadRequest, code)
		code, _ = f.call(t, f.app.RenameChatChannel, f.org, f.owner, map[string]any{"name": strings.Repeat("n", 101)}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusBadRequest, code)
		code, _ = f.call(t, f.app.RenameChatChannel, f.org, f.owner, map[string]any{"name": " ops 2 "}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusOK, code)
		assert.Equal(t, "ops 2", f.reload(t, f.ch).Name, "trimmed")
		code, _ = f.call(t, f.app.RenameChatChannel, f.org, f.manager, map[string]any{"name": "ops 3"}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusOK, code, "a manager renames without being a member")
		code, _ = f.call(t, f.app.SetChatMemberRole, f.org, f.owner, map[string]any{"role": "boss"}, f.path(f.ch, f.member.ID.String()))
		assert.Equal(t, fasthttp.StatusBadRequest, code)
		code, _ = f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": f.member.ID}, f.path(f.ch))
		assert.Equal(t, fasthttp.StatusConflict, code, "already a member")
		code, _ = f.call(t, f.app.RemoveChatChannelMember, f.org, f.owner, nil, f.path(f.ch, f.spare.ID.String()))
		assert.Equal(t, fasthttp.StatusNotFound, code, "removing someone who is not in it")
	})
}

// System messages: never editable, never deletable, and they do not become unread
// for the person they are about when that person is added.
func TestPhaseA_SystemMessages(t *testing.T) {
	f := newPhaseA(t)
	code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	sys := f.systemMessages(t, f.ch)
	require.Len(t, sys, 1)
	assert.Equal(t, "member_added", sys[0].Meta["event"])
	assert.Equal(t, f.spare.ID.String(), sys[0].Meta["target_id"])
	idPath := map[string]string{"id": sys[0].ID.String()}

	code, _ = f.call(t, f.app.UpdateChatMessage, f.org, f.owner, map[string]any{"body": "rewritten"}, idPath)
	assert.Equal(t, fasthttp.StatusForbidden, code, "the actor cannot edit it")
	for name, u := range map[string]*models.User{"actor": f.owner, "moderator with team_chat:delete": f.manager} {
		_ = name
		code, _ = f.call(t, f.app.DeleteChatMessage, f.org, u, nil, idPath)
		assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, code)
	}
	code, _ = f.call(t, f.app.DeleteChatMessage, f.org, f.admin, nil, idPath)
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, code)
	assert.Len(t, f.systemMessages(t, f.ch), 1, "still there")

	// Unread: a newcomer's baseline is their entry, so the notice about their own
	// arrival is not unread; the other members do see it as new.
	for _, c := range listChannels(t, f.app, f.org.ID, f.spare.ID) {
		if c.ID == f.ch.ID {
			assert.Zero(t, c.UnreadCount)
		}
	}
	for _, c := range listChannels(t, f.app, f.org.ID, f.member.ID) {
		if c.ID == f.ch.ID {
			assert.EqualValues(t, 1, c.UnreadCount)
		}
	}
	for _, c := range listChannels(t, f.app, f.org.ID, f.owner.ID) {
		if c.ID == f.ch.ID {
			assert.Zero(t, c.UnreadCount, "the actor's own record is not unread for them")
		}
	}
}

// History policy (criterion 20): added by the owner, the newcomer reads everything.
func TestPhaseA_NewMemberReadsFullHistoryWithoutMarkers(t *testing.T) {
	f := newPhaseA(t)
	for _, b := range []string{"one", "two", "three"} {
		_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, b)
	}
	var readsBefore int64
	f.app.DB.Model(&models.ChatRead{}).Count(&readsBefore)

	code, _ := f.call(t, f.app.AddChatChannelMember, f.org, f.owner, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)

	code, raw := f.call(t, f.app.ListChatMessages, f.org, f.spare, nil, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	bodies := []string{}
	for _, m := range decodeChat[chatMessagesData](t, raw).Messages {
		if m.Kind != models.ChatMessageKindSystem {
			bodies = append(bodies, m.Body)
		}
	}
	assert.Equal(t, []string{"one", "two", "three"}, bodies)

	var readsAfter int64
	f.app.DB.Model(&models.ChatRead{}).Count(&readsAfter)
	assert.Equal(t, readsBefore, readsAfter, "adding someone writes no read marker")
	for _, c := range listChannels(t, f.app, f.org.ID, f.spare.ID) {
		if c.ID == f.ch.ID {
			assert.Zero(t, c.UnreadCount)
		}
	}
	_, _ = sendMsg(t, f.app, f.org.ID, f.owner.ID, f.ch.ID, "four")
	for _, c := range listChannels(t, f.app, f.org.ID, f.spare.ID) {
		if c.ID == f.ch.ID {
			assert.EqualValues(t, 1, c.UnreadCount)
		}
	}
}

// The member list is for members; it carries roles and names, not e-mail.
func TestPhaseA_MemberList(t *testing.T) {
	f := newPhaseA(t)
	code, raw := f.call(t, f.app.ListChatChannelMembers, f.org, f.member, nil, f.path(f.ch))
	require.Equal(t, fasthttp.StatusOK, code)
	var res struct {
		Data struct {
			Members []handlers.ChatMemberResponse `json:"members"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &res))
	require.Len(t, res.Data.Members, 2)
	assert.Equal(t, f.owner.ID, res.Data.Members[0].UserID, "owners first")
	assert.Equal(t, "owner", res.Data.Members[0].Role)
	assert.NotContains(t, string(raw), "@example.com")
}
