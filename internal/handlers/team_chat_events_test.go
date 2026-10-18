package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// chatHub starts a real hub on the app and registers one live connection per
// user (org-scoped, like production), so a test can assert exactly who receives
// each event.
func chatHub(t *testing.T, app *handlers.App, orgID uuid.UUID, users ...*models.User) map[uuid.UUID]*websocket.Client {
	t.Helper()
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	app.WSHub = hub
	clients := make(map[uuid.UUID]*websocket.Client, len(users))
	for _, u := range users {
		c := websocket.NewClient(hub, nil, u.ID, orgID)
		hub.Register(c)
		clients[u.ID] = c
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.GetClientCount() != len(users) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, len(users), hub.GetClientCount())
	return clients
}

func expectEvent(t *testing.T, c *websocket.Client, typ string) websocket.TeamChatEventPayload {
	t.Helper()
	msg := readBroadcast(t, c, typ)
	raw, err := json.Marshal(msg.Payload)
	require.NoError(t, err)
	var p websocket.TeamChatEventPayload
	require.NoError(t, json.Unmarshal(raw, &p))
	return p
}

// setupTeamChat builds an org with a team of two chat users plus an outsider in
// the same org, and returns the team channel id (the list call creates it).
func setupTeamChat(t *testing.T, app *handlers.App) (org *models.Organization, a, b, outsider *models.User, team *models.Team, chID uuid.UUID) {
	t.Helper()
	org = testutil.CreateTestOrganization(t, app.DB)
	a = chatUser(t, app, org.ID, chatUserKeys)
	b = chatUser(t, app, org.ID, chatUserKeys)
	outsider = chatUser(t, app, org.ID, chatUserKeys)
	team = createTeamWithMember(t, app, org.ID, a.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{
		BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: b.ID, Role: models.TeamRoleAgent}).Error)
	chID = channelOfTeam(t, listChannels(t, app, org.ID, a.ID), team.ID).ID
	return
}

func TestChatEvents_MessageGoesOnlyToTheChannelsMembers_AndCarriesIDsOnly(t *testing.T) {
	app := newTestApp(t)
	org, a, b, outsider, _, chID := setupTeamChat(t, app)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreign := chatUser(t, app, otherOrg.ID, chatUserKeys)

	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	app.WSHub = hub
	reg := func(orgID uuid.UUID, u *models.User) *websocket.Client {
		c := websocket.NewClient(hub, nil, u.ID, orgID)
		hub.Register(c)
		return c
	}
	ca, cb, co, cf := reg(org.ID, a), reg(org.ID, b), reg(org.ID, outsider), reg(otherOrg.ID, foreign)
	for hub.GetClientCount() != 4 {
		time.Sleep(5 * time.Millisecond)
	}

	code, sent := sendMsg(t, app, org.ID, a.ID, chID, "segredo da equipe")
	require.Equal(t, fasthttp.StatusOK, code)

	// The sender's other tabs and the teammate both learn of it.
	for _, c := range []*websocket.Client{ca, cb} {
		p := expectEvent(t, c, websocket.TypeTeamChatMessage)
		assert.Equal(t, chID, p.ChannelID)
		require.NotNil(t, p.MessageID)
		assert.Equal(t, sent.ID, *p.MessageID)
		assert.Equal(t, a.ID, p.UserID)
	}
	assertNoBroadcast(t, co) // same org, not in the team
	assertNoBroadcast(t, cf) // another organization

	// IDs only: the text never travels over the socket.
	raw, _ := json.Marshal(websocket.WSMessage{Type: websocket.TypeTeamChatMessage,
		Payload: websocket.TeamChatEventPayload{ChannelID: chID, UserID: a.ID}})
	assert.NotContains(t, string(raw), "body")
	assert.NotContains(t, string(raw), "segredo")
}

func TestChatEvents_RecipientsAreRecomputedAtEveryEvent(t *testing.T) {
	app := newTestApp(t)
	org, a, b, _, team, chID := setupTeamChat(t, app)
	clients := chatHub(t, app, org.ID, a, b)

	t.Run("a member who left the team stops receiving", func(t *testing.T) {
		require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, b.ID).Delete(&models.TeamMember{}).Error)
		_, _ = sendMsg(t, app, org.ID, a.ID, chID, "after b left")
		expectEvent(t, clients[a.ID], websocket.TypeTeamChatMessage)
		assertNoBroadcast(t, clients[b.ID])
	})

	t.Run("a member without team_chat:read is not notified", func(t *testing.T) {
		noRead := chatUser(t, app, org.ID, []string{"contacts:read"})
		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: noRead.ID, Role: models.TeamRoleAgent}).Error)
		cs := chatHub(t, app, org.ID, a, noRead)
		_, _ = sendMsg(t, app, org.ID, a.ID, chID, "ping")
		expectEvent(t, cs[a.ID], websocket.TypeTeamChatMessage)
		assertNoBroadcast(t, cs[noRead.ID])
	})
}

func TestChatEvents_FreeChannelAndEditDelete(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, org.ID, chatAdmKeys)
	member := chatUser(t, app, org.ID, chatUserKeys)
	outsider := chatUser(t, app, org.ID, chatUserKeys)
	cs := chatHub(t, app, org.ID, admin, member, outsider)

	code, raw := chatDo(t, app.CreateChatChannel, org.ID, admin.ID,
		map[string]any{"name": "ops", "member_ids": []uuid.UUID{member.ID}}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	ch := decodeChat[handlers.ChatChannelResponse](t, raw)

	// Creating the channel tells its members (so their list updates), not others.
	expectEvent(t, cs[admin.ID], websocket.TypeTeamChatChannel)
	p := expectEvent(t, cs[member.ID], websocket.TypeTeamChatChannel)
	assert.Equal(t, ch.ID, p.ChannelID)
	assertNoBroadcast(t, cs[outsider.ID])

	_, msg := sendMsg(t, app, org.ID, admin.ID, ch.ID, "hello")
	expectEvent(t, cs[admin.ID], websocket.TypeTeamChatMessage)
	expectEvent(t, cs[member.ID], websocket.TypeTeamChatMessage)

	path := map[string]string{"id": msg.ID.String()}
	code, _ = chatDo(t, app.UpdateChatMessage, org.ID, admin.ID, map[string]any{"body": "hello!"}, path, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	expectEvent(t, cs[admin.ID], websocket.TypeTeamChatMessageUpdated)
	pu := expectEvent(t, cs[member.ID], websocket.TypeTeamChatMessageUpdated)
	assert.Equal(t, msg.ID, *pu.MessageID)

	code, _ = chatDo(t, app.DeleteChatMessage, org.ID, admin.ID, nil, path, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	expectEvent(t, cs[admin.ID], websocket.TypeTeamChatMessageDeleted)
	expectEvent(t, cs[member.ID], websocket.TypeTeamChatMessageDeleted)
	assertNoBroadcast(t, cs[outsider.ID])
}

func TestChatEvents_FailedRequestsSendNothing(t *testing.T) {
	app := newTestApp(t)
	org, a, b, outsider, _, chID := setupTeamChat(t, app)
	cs := chatHub(t, app, org.ID, a, b, outsider)

	code, _ := sendMsg(t, app, org.ID, outsider.ID, chID, "intruder")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = sendMsg(t, app, org.ID, a.ID, chID, "   ")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	for _, c := range cs {
		assertNoBroadcast(t, c)
	}
}

func TestChatEvents_Typing(t *testing.T) {
	app := newTestApp(t)
	org, a, b, outsider, _, chID := setupTeamChat(t, app)
	cs := chatHub(t, app, org.ID, a, b, outsider)
	path := map[string]string{"id": chID.String()}

	code, _ := chatDo(t, app.NotifyChatTyping, org.ID, a.ID, map[string]any{}, path, nil)
	assert.Equal(t, fasthttp.StatusNoContent, code)
	p := expectEvent(t, cs[b.ID], websocket.TypeTeamChatTyping)
	assert.Equal(t, a.ID, p.UserID)
	assert.Equal(t, chID, p.ChannelID)
	assertNoBroadcast(t, cs[a.ID]) // the typist does not see their own typing
	assertNoBroadcast(t, cs[outsider.ID])

	t.Run("a non-member cannot fake typing in a channel", func(t *testing.T) {
		code, _ := chatDo(t, app.NotifyChatTyping, org.ID, outsider.ID, map[string]any{}, path, nil)
		assert.Equal(t, fasthttp.StatusNotFound, code)
		assertNoBroadcast(t, cs[a.ID])
		assertNoBroadcast(t, cs[b.ID])
	})

	t.Run("read-only users cannot send typing", func(t *testing.T) {
		ro := chatUser(t, app, org.ID, []string{"team_chat:read"})
		code, _ := chatDo(t, app.NotifyChatTyping, org.ID, ro.ID, map[string]any{}, path, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code)
	})
}

func TestChatEvents_ReadReachesOnlyTheReader(t *testing.T) {
	app := newTestApp(t)
	org, a, b, _, _, chID := setupTeamChat(t, app)
	cs := chatHub(t, app, org.ID, a, b)
	_, _ = sendMsg(t, app, org.ID, b.ID, chID, "hi")
	expectEvent(t, cs[a.ID], websocket.TypeTeamChatMessage)
	expectEvent(t, cs[b.ID], websocket.TypeTeamChatMessage)

	code, _ := chatDo(t, app.MarkChatChannelRead, org.ID, a.ID, nil, map[string]string{"id": chID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	p := expectEvent(t, cs[a.ID], websocket.TypeTeamChatRead)
	assert.Equal(t, chID, p.ChannelID)
	assertNoBroadcast(t, cs[b.ID]) // nobody else learns who read what
}

func TestChatEvents_NewDMNotifiesTheOtherUserOnce(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	a := chatUser(t, app, org.ID, chatUserKeys)
	b := chatUser(t, app, org.ID, chatUserKeys)
	cs := chatHub(t, app, org.ID, a, b)

	for i := 0; i < 2; i++ {
		code, _ := chatDo(t, app.CreateChatDM, org.ID, a.ID, map[string]any{"user_id": b.ID}, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
	}
	expectEvent(t, cs[b.ID], websocket.TypeTeamChatChannel)
	expectEvent(t, cs[a.ID], websocket.TypeTeamChatChannel)
	assertNoBroadcast(t, cs[b.ID]) // opening the same DM again is not a new channel
	assertNoBroadcast(t, cs[a.ID])
}
