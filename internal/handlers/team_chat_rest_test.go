package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

var (
	chatUserKeys = []string{"team_chat:read", "team_chat:write"}
	chatModKeys  = []string{"team_chat:read", "team_chat:write", "team_chat:delete"}
	chatAdmKeys  = []string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"}
)

func chatUser(t *testing.T, app *handlers.App, orgID uuid.UUID, keys []string) *models.User {
	t.Helper()
	role := testutil.CreateTestRoleWithKeys(t, app.DB, orgID, "chat", keys)
	return testutil.CreateTestUser(t, app.DB, orgID, testutil.WithRoleID(&role.ID))
}

// chatDo runs a handler as (org,user) with an optional JSON body, path and query params.
func chatDo(t *testing.T, h func(*fastglue.Request) error, orgID, userID uuid.UUID, body any, path map[string]string, query map[string]string) (int, []byte) {
	t.Helper()
	var req *fastglue.Request
	if body != nil {
		req = testutil.NewJSONRequest(t, body)
	} else {
		req = testutil.NewGETRequest(t)
	}
	testutil.SetAuthContext(req, orgID, userID)
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	for k, v := range query {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

type chatEnv[T any] struct {
	Data T `json:"data"`
}

func decodeChat[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var e chatEnv[T]
	require.NoError(t, json.Unmarshal(raw, &e))
	return e.Data
}

type chatChannelsData struct {
	Channels []handlers.ChatChannelResponse `json:"channels"`
}
type chatMessagesData struct {
	Messages []handlers.ChatMessageResponse `json:"messages"`
	HasMore  bool                           `json:"has_more"`
}

func listChannels(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) []handlers.ChatChannelResponse {
	t.Helper()
	code, raw := chatDo(t, app.ListChatChannels, orgID, userID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	return decodeChat[chatChannelsData](t, raw).Channels
}

func sendMsg(t *testing.T, app *handlers.App, orgID, userID, channelID uuid.UUID, body string) (int, handlers.ChatMessageResponse) {
	t.Helper()
	code, raw := chatDo(t, app.SendChatMessage, orgID, userID, map[string]any{"body": body},
		map[string]string{"id": channelID.String()}, nil)
	if code != fasthttp.StatusOK {
		return code, handlers.ChatMessageResponse{}
	}
	return code, decodeChat[handlers.ChatMessageResponse](t, raw)
}

func channelOfTeam(t *testing.T, chs []handlers.ChatChannelResponse, teamID uuid.UUID) handlers.ChatChannelResponse {
	t.Helper()
	for _, c := range chs {
		if c.TeamID != nil && *c.TeamID == teamID {
			return c
		}
	}
	t.Fatalf("no team channel for team %s", teamID)
	return handlers.ChatChannelResponse{}
}

func TestChatListChannels_TeamChannelsAreCreatedOnceAndScopedToMembership(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	a := chatUser(t, app, org.ID, chatUserKeys)
	b := chatUser(t, app, org.ID, chatUserKeys)
	teamA := createTeamWithMember(t, app, org.ID, a.ID)
	teamB := createTeamWithMember(t, app, org.ID, b.ID)

	first := listChannels(t, app, org.ID, a.ID)
	second := listChannels(t, app, org.ID, a.ID)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].ID, second[0].ID, "listing twice must not create a second channel")
	assert.Equal(t, models.ChatChannelKindTeam, first[0].Kind)
	assert.Equal(t, teamA.Name, first[0].Name)

	var count int64
	app.DB.Model(&models.ChatChannel{}).Where("organization_id = ? AND team_id = ?", org.ID, teamB.ID).Count(&count)
	assert.Zero(t, count, "a team's channel is only created when one of its members lists channels")

	bList := listChannels(t, app, org.ID, b.ID)
	require.Len(t, bList, 1)
	assert.NotEqual(t, first[0].ID, bList[0].ID)
}

func TestChatListChannels_RequiresPermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	noChat := chatUser(t, app, org.ID, []string{"contacts:read"})
	code, _ := chatDo(t, app.ListChatChannels, org.ID, noChat.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}

func TestChatCreateChannel(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, org.ID, chatAdmKeys)
	agent := chatUser(t, app, org.ID, chatUserKeys)
	colleague := chatUser(t, app, org.ID, chatUserKeys)
	foreigner := testutil.CreateTestUser(t, app.DB, other.ID)

	t.Run("needs manage", func(t *testing.T) {
		code, _ := chatDo(t, app.CreateChatChannel, org.ID, agent.ID, map[string]any{"name": "ops"}, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code)
	})

	t.Run("validates the name", func(t *testing.T) {
		code, _ := chatDo(t, app.CreateChatChannel, org.ID, admin.ID, map[string]any{"name": "   "}, nil, nil)
		assert.Equal(t, fasthttp.StatusBadRequest, code)
	})

	t.Run("creator owns it, colleague joins, other-org user is ignored", func(t *testing.T) {
		code, raw := chatDo(t, app.CreateChatChannel, org.ID, admin.ID,
			map[string]any{"name": "logística", "member_ids": []uuid.UUID{colleague.ID, foreigner.ID}}, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		ch := decodeChat[handlers.ChatChannelResponse](t, raw)

		var members []models.ChatChannelMember
		require.NoError(t, app.DB.Where("channel_id = ?", ch.ID).Find(&members).Error)
		got := map[uuid.UUID]string{}
		for _, m := range members {
			got[m.UserID] = m.Role
		}
		assert.Equal(t, map[uuid.UUID]string{admin.ID: "owner", colleague.ID: "member"}, got)

		assert.Len(t, listChannels(t, app, org.ID, colleague.ID), 1)
		assert.Empty(t, listChannels(t, app, org.ID, agent.ID), "non-member does not see it")
	})
}

func TestChatCreateDM(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	a := chatUser(t, app, org.ID, chatUserKeys)
	b := chatUser(t, app, org.ID, chatUserKeys)
	foreigner := testutil.CreateTestUser(t, app.DB, other.ID)

	open := func(from, to uuid.UUID) (int, handlers.ChatChannelResponse) {
		code, raw := chatDo(t, app.CreateChatDM, org.ID, from, map[string]any{"user_id": to}, nil, nil)
		if code != fasthttp.StatusOK {
			return code, handlers.ChatChannelResponse{}
		}
		return code, decodeChat[handlers.ChatChannelResponse](t, raw)
	}

	_, first := open(a.ID, b.ID)
	_, second := open(b.ID, a.ID)
	assert.Equal(t, first.ID, second.ID, "the same pair always lands on one DM, whoever opens it")
	assert.Equal(t, models.ChatChannelKindDM, first.Kind)

	var n int64
	app.DB.Model(&models.ChatChannel{}).Where("organization_id = ? AND kind = ?", org.ID, models.ChatChannelKindDM).Count(&n)
	assert.EqualValues(t, 1, n)

	// Each side sees the other as the DM's name.
	aList := listChannels(t, app, org.ID, a.ID)
	require.Len(t, aList, 1)
	assert.Equal(t, b.ID, *aList[0].DMUserID)

	code, _ := open(a.ID, a.ID)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "no DM with yourself")
	code, _ = open(a.ID, foreigner.ID)
	assert.Equal(t, fasthttp.StatusNotFound, code, "no DM across organizations")
}

func TestChatMessages_AccessAndValidation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	member := chatUser(t, app, org.ID, chatUserKeys)
	outsider := chatUser(t, app, org.ID, chatUserKeys)
	foreigner := chatUser(t, app, other.ID, chatUserKeys)
	team := createTeamWithMember(t, app, org.ID, member.ID)
	_ = team
	ch := listChannels(t, app, org.ID, member.ID)[0]

	code, msg := sendMsg(t, app, org.ID, member.ID, ch.ID, "  olá  ")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "olá", msg.Body, "body is trimmed")

	for name, c := range map[string]struct{ org, user uuid.UUID }{
		"same org, not in the team": {org.ID, outsider.ID},
		"another organization":      {other.ID, foreigner.ID},
	} {
		t.Run(name+" cannot send", func(t *testing.T) {
			code, _ := sendMsg(t, app, c.org, c.user, ch.ID, "hi")
			assert.Equal(t, fasthttp.StatusNotFound, code)
		})
		t.Run(name+" cannot list", func(t *testing.T) {
			code, _ := chatDo(t, app.ListChatMessages, c.org, c.user, nil, map[string]string{"id": ch.ID.String()}, nil)
			assert.Equal(t, fasthttp.StatusNotFound, code)
		})
		t.Run(name+" cannot mark read", func(t *testing.T) {
			code, _ := chatDo(t, app.MarkChatChannelRead, c.org, c.user, nil, map[string]string{"id": ch.ID.String()}, nil)
			assert.Equal(t, fasthttp.StatusNotFound, code)
		})
	}

	t.Run("empty and oversized bodies are rejected", func(t *testing.T) {
		code, _ := sendMsg(t, app, org.ID, member.ID, ch.ID, "   ")
		assert.Equal(t, fasthttp.StatusBadRequest, code)
		long := make([]rune, 4001)
		for i := range long {
			long[i] = 'x'
		}
		code, _ = sendMsg(t, app, org.ID, member.ID, ch.ID, string(long))
		assert.Equal(t, fasthttp.StatusBadRequest, code)
	})

	t.Run("a read-only user cannot send", func(t *testing.T) {
		ro := chatUser(t, app, org.ID, []string{"team_chat:read"})
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: ro.ID, Role: models.TeamRoleAgent}).Error)
		code, _ := sendMsg(t, app, org.ID, ro.ID, ch.ID, "hi")
		assert.Equal(t, fasthttp.StatusForbidden, code)
	})
}

func TestChatMessages_CursorPagination(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	u := chatUser(t, app, org.ID, chatUserKeys)
	createTeamWithMember(t, app, org.ID, u.ID)
	ch := listChannels(t, app, org.ID, u.ID)[0]

	// Same created_at on purpose for the first three: the (created_at, id) cursor
	// must still page them without skipping or repeating.
	same := time.Now().Add(-time.Hour)
	var want []uuid.UUID
	for i := 0; i < 5; i++ {
		m := models.ChatMessage{
			BaseModel:      models.BaseModel{ID: uuid.New(), CreatedAt: same},
			OrganizationID: org.ID, ChannelID: ch.ID, UserID: u.ID, Body: "m",
		}
		if i >= 3 {
			m.CreatedAt = same.Add(time.Duration(i) * time.Minute)
		}
		require.NoError(t, app.DB.Create(&m).Error)
	}
	var all []models.ChatMessage
	require.NoError(t, app.DB.Where("channel_id = ?", ch.ID).Order("created_at ASC, id ASC").Find(&all).Error)
	for _, m := range all {
		want = append(want, m.ID)
	}

	page := func(before string) chatMessagesData {
		q := map[string]string{"limit": "2"}
		if before != "" {
			q["before_id"] = before
		}
		code, raw := chatDo(t, app.ListChatMessages, org.ID, u.ID, nil, map[string]string{"id": ch.ID.String()}, q)
		require.Equal(t, fasthttp.StatusOK, code)
		return decodeChat[chatMessagesData](t, raw)
	}

	p1 := page("")
	require.Len(t, p1.Messages, 2)
	assert.True(t, p1.HasMore)
	p2 := page(p1.Messages[0].ID.String())
	require.Len(t, p2.Messages, 2)
	assert.True(t, p2.HasMore)
	p3 := page(p2.Messages[0].ID.String())
	require.Len(t, p3.Messages, 1)
	assert.False(t, p3.HasMore)

	var got []uuid.UUID
	for _, p := range []chatMessagesData{p3, p2, p1} {
		for _, m := range p.Messages {
			got = append(got, m.ID)
		}
	}
	assert.Equal(t, want, got, "pages stitch back into the full history, in order, with no gaps or repeats")

	code, _ := chatDo(t, app.ListChatMessages, org.ID, u.ID, nil, map[string]string{"id": ch.ID.String()},
		map[string]string{"before_id": uuid.New().String()})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "an unknown cursor is rejected")
}

func TestChatMessages_EditAndDelete(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	author := chatUser(t, app, org.ID, chatUserKeys)
	peer := chatUser(t, app, org.ID, chatUserKeys)
	mod := chatUser(t, app, org.ID, chatModKeys)
	outsiderMod := chatUser(t, app, org.ID, chatModKeys)
	team := createTeamWithMember(t, app, org.ID, author.ID)
	for _, u := range []*models.User{peer, mod} {
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
	}
	ch := listChannels(t, app, org.ID, author.ID)[0]
	_, msg := sendMsg(t, app, org.ID, author.ID, ch.ID, "original")
	path := map[string]string{"id": msg.ID.String()}

	edit := func(as uuid.UUID, body string) int {
		code, _ := chatDo(t, app.UpdateChatMessage, org.ID, as, map[string]any{"body": body}, path, nil)
		return code
	}
	assert.Equal(t, fasthttp.StatusOK, edit(author.ID, "edited"))
	assert.Equal(t, fasthttp.StatusForbidden, edit(peer.ID, "x"), "only the author edits")
	assert.Equal(t, fasthttp.StatusForbidden, edit(mod.ID, "x"), "delete permission does not allow editing")
	assert.Equal(t, fasthttp.StatusNotFound, edit(outsiderMod.ID, "x"), "no channel access: hidden")
	assert.Equal(t, fasthttp.StatusBadRequest, edit(author.ID, " "))

	var stored models.ChatMessage
	require.NoError(t, app.DB.First(&stored, "id = ?", msg.ID).Error)
	assert.Equal(t, "edited", stored.Body)
	assert.NotNil(t, stored.EditedAt)

	del := func(as uuid.UUID) int {
		code, _ := chatDo(t, app.DeleteChatMessage, org.ID, as, nil, path, nil)
		return code
	}
	assert.Equal(t, fasthttp.StatusForbidden, del(peer.ID), "another member without delete permission")
	assert.Equal(t, fasthttp.StatusNotFound, del(outsiderMod.ID), "delete permission without access: hidden")
	assert.Equal(t, fasthttp.StatusOK, del(mod.ID), "moderator with access")

	code, raw := chatDo(t, app.ListChatMessages, org.ID, author.ID, nil, map[string]string{"id": ch.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Empty(t, decodeChat[chatMessagesData](t, raw).Messages, "deleted messages leave the history")

	// The audit entry is written by a goroutine (audit.LogAudit is fire-and-forget),
	// so wait for it instead of counting at once.
	auditCount := func() int64 {
		var n int64
		app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ? AND resource_id = ?",
			org.ID, "chat_message", msg.ID).Count(&n)
		return n
	}
	require.Eventually(t, func() bool { return auditCount() >= 1 }, 5*time.Second, 25*time.Millisecond, "moderation of someone else's message is audited")
	assert.EqualValues(t, 1, auditCount())

	// The author deleting their own message is not moderation.
	_, own := sendMsg(t, app, org.ID, author.ID, ch.ID, "mine")
	code, _ = chatDo(t, app.DeleteChatMessage, org.ID, author.ID, nil, map[string]string{"id": own.ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
}

func TestChatUnreadCounts(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	a := chatUser(t, app, org.ID, chatUserKeys)
	b := chatUser(t, app, org.ID, chatUserKeys)
	team := createTeamWithMember(t, app, org.ID, a.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{
		BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: b.ID, Role: models.TeamRoleAgent}).Error)

	unread := func(as uuid.UUID) int64 {
		return channelOfTeam(t, listChannels(t, app, org.ID, as), team.ID).UnreadCount
	}
	chID := channelOfTeam(t, listChannels(t, app, org.ID, a.ID), team.ID).ID
	pause := func() { time.Sleep(25 * time.Millisecond) }

	_, _ = sendMsg(t, app, org.ID, b.ID, chID, "1")
	pause()
	_, _ = sendMsg(t, app, org.ID, b.ID, chID, "2")
	pause()
	_, _ = sendMsg(t, app, org.ID, a.ID, chID, "mine")
	assert.EqualValues(t, 2, unread(a.ID), "only other people's messages count")
	assert.EqualValues(t, 1, unread(b.ID), "and the sender's own do not")

	code, _ := chatDo(t, app.MarkChatChannelRead, org.ID, a.ID, nil, map[string]string{"id": chID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.EqualValues(t, 0, unread(a.ID))
	// Marking read twice is harmless.
	code, _ = chatDo(t, app.MarkChatChannelRead, org.ID, a.ID, nil, map[string]string{"id": chID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)

	pause()
	_, _ = sendMsg(t, app, org.ID, b.ID, chID, "3")
	assert.EqualValues(t, 1, unread(a.ID))

	t.Run("a new team member does not inherit the history as unread", func(t *testing.T) {
		pause()
		c := chatUser(t, app, org.ID, chatUserKeys)
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: c.ID, Role: models.TeamRoleAgent}).Error)
		assert.EqualValues(t, 0, unread(c.ID))
		pause()
		_, _ = sendMsg(t, app, org.ID, b.ID, chID, "4")
		assert.EqualValues(t, 1, unread(c.ID), "only what arrives after joining")
	})

	t.Run("leaving and rejoining does not resurrect the time away", func(t *testing.T) {
		require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, a.ID).Delete(&models.TeamMember{}).Error)
		pause()
		_, _ = sendMsg(t, app, org.ID, b.ID, chID, "while away")
		pause()
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: a.ID, Role: models.TeamRoleAgent}).Error)
		assert.EqualValues(t, 0, unread(a.ID))
	})
}

func TestChatUnread_DMAndFreeChannelBaselineIsJoinTime(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	a := chatUser(t, app, org.ID, chatUserKeys)
	b := chatUser(t, app, org.ID, chatUserKeys)

	_, raw := chatDo(t, app.CreateChatDM, org.ID, a.ID, map[string]any{"user_id": b.ID}, nil, nil)
	dm := decodeChat[handlers.ChatChannelResponse](t, raw)
	time.Sleep(25 * time.Millisecond)
	_, _ = sendMsg(t, app, org.ID, a.ID, dm.ID, "hey")

	bList := listChannels(t, app, org.ID, b.ID)
	require.Len(t, bList, 1)
	assert.EqualValues(t, 1, bList[0].UnreadCount)
	assert.EqualValues(t, 0, listChannels(t, app, org.ID, a.ID)[0].UnreadCount)
}

func TestChatListUsers(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	me := chatUser(t, app, org.ID, chatUserKeys)
	peer := chatUser(t, app, org.ID, chatUserKeys)
	testutil.CreateTestUser(t, app.DB, other.ID) // another organization: never listed
	inactive := chatUser(t, app, org.ID, chatUserKeys)
	require.NoError(t, app.DB.Model(inactive).Update("is_active", false).Error)

	code, raw := chatDo(t, app.ListChatUsers, org.ID, me.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	data := decodeChat[struct {
		Users []handlers.ChatUserResponse `json:"users"`
	}](t, raw)
	require.Len(t, data.Users, 1, "only active people of my organization, not myself")
	assert.Equal(t, peer.ID, data.Users[0].ID)
	assert.NotContains(t, string(raw), "@", "no e-mail leaves the server")

	noChat := chatUser(t, app, org.ID, []string{"contacts:read"})
	code, _ = chatDo(t, app.ListChatUsers, org.ID, noChat.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}
