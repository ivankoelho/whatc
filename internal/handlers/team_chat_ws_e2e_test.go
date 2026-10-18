package handlers_test

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
	"github.com/zerodha/fastglue"
)

// End-to-end over real WebSocket connections: the production upgrade handler,
// the production hub, message-based JWT auth, and the chat REST handlers
// producing the events. Nothing about delivery is mocked; each "person" is a
// separate authenticated socket.

type wsPerson struct {
	name   string
	conn   *websocket.Conn
	frames chan ws.WSMessage
}

type wsRig struct {
	t   *testing.T
	app *handlers.App
	ln  *fasthttputil.InmemoryListener
}

func newWSRig(t *testing.T, app *handlers.App) *wsRig {
	t.Helper()
	hub := ws.NewHub(testutil.NopLogger())
	go hub.Run()
	app.WSHub = hub

	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		_ = app.WebSocketHandler(&fastglue.Request{RequestCtx: ctx})
	}}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return &wsRig{t: t, app: app, ln: ln}
}

// connect opens a socket and authenticates it with a JWT for user.
func (r *wsRig) connect(name string, user *models.User) *wsPerson {
	r.t.Helper()
	return r.connectWithToken(name, testutil.GenerateTestRefreshToken(r.t, user, testutil.TestJWTSecret, time.Hour))
}

// connectWithToken opens a socket and authenticates it with the given token.
func (r *wsRig) connectWithToken(name, token string) *wsPerson {
	r.t.Helper()
	d := websocket.Dialer{NetDial: func(_, _ string) (net.Conn, error) { return r.ln.Dial() }}
	conn, _, err := d.Dial("ws://chat.test/ws", nil)
	require.NoError(r.t, err)

	require.NoError(r.t, conn.WriteJSON(ws.WSMessage{Type: ws.TypeAuth, Payload: ws.AuthPayload{Token: token}}))

	p := &wsPerson{name: name, conn: conn, frames: make(chan ws.WSMessage, 64)}
	go func() {
		defer close(p.frames)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m ws.WSMessage
			if json.Unmarshal(data, &m) == nil {
				p.frames <- m
			}
		}
	}()
	r.t.Cleanup(func() { _ = conn.Close() })
	return p
}

// waitOnline blocks until the hub has registered every given user.
func (r *wsRig) waitOnline(orgID uuid.UUID, users ...*models.User) {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, u := range users {
			if !r.app.WSHub.IsUserOnline(orgID, u.ID) {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("users did not come online on the hub")
}

func (p *wsPerson) expect(t *testing.T, typ string) ws.TeamChatEventPayload {
	t.Helper()
	select {
	case m, ok := <-p.frames:
		require.True(t, ok, "%s: socket closed while waiting for %s", p.name, typ)
		require.Equal(t, typ, m.Type, "%s received the wrong event", p.name)
		raw, _ := json.Marshal(m.Payload)
		var pl ws.TeamChatEventPayload
		require.NoError(t, json.Unmarshal(raw, &pl))
		return pl
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: timed out waiting for %s", p.name, typ)
		return ws.TeamChatEventPayload{}
	}
}

func (p *wsPerson) expectClosed(t *testing.T) {
	t.Helper()
	select {
	case m, ok := <-p.frames:
		require.False(t, ok, "%s should have been disconnected, got %s", p.name, m.Type)
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: connection with a forged token was not closed", p.name)
	}
}

func (p *wsPerson) expectSilence(t *testing.T) {
	t.Helper()
	select {
	case m, ok := <-p.frames:
		if ok {
			t.Fatalf("%s should have received nothing, got %s", p.name, m.Type)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestChatWSEndToEnd_TwoAccountsAndOutsiders(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	ana := chatUser(t, app, org.ID, chatUserKeys)
	bia := chatUser(t, app, org.ID, chatUserKeys)
	carol := chatUser(t, app, org.ID, chatUserKeys) // same organization, not in the team
	dan := chatUser(t, app, otherOrg.ID, chatUserKeys)
	team := createTeamWithMember(t, app, org.ID, ana.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{
		BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: bia.ID, Role: models.TeamRoleAgent}).Error)
	chID := channelOfTeam(t, listChannels(t, app, org.ID, ana.ID), team.ID).ID

	rig := newWSRig(t, app)
	pAna := rig.connect("ana", ana)
	pBia := rig.connect("bia", bia)
	pCarol := rig.connect("carol", carol)
	pDan := rig.connect("dan", dan)
	rig.waitOnline(org.ID, ana, bia, carol)
	rig.waitOnline(otherOrg.ID, dan)
	path := map[string]string{"id": chID.String()}

	// A socket with a forged token is closed by the server and never receives anything.
	bad := rig.connectWithToken("forged", "not-a-valid-jwt")

	// 1. Ana sends: both teammates get the event, with ids only.
	code, sent := sendMsg(t, app, org.ID, ana.ID, chID, "reunião às 15h")
	require.Equal(t, fasthttp.StatusOK, code)
	for _, p := range []*wsPerson{pAna, pBia} {
		ev := p.expect(t, ws.TypeTeamChatMessage)
		assert.Equal(t, chID, ev.ChannelID)
		require.NotNil(t, ev.MessageID)
		assert.Equal(t, sent.ID, *ev.MessageID)
		assert.Equal(t, ana.ID, ev.UserID)
	}
	pCarol.expectSilence(t)
	pDan.expectSilence(t)
	bad.expectClosed(t)

	// 2. Bia sees Ana's message by REST (events carry no text), then edits nothing of Ana's.
	code, raw := chatDo(t, app.ListChatMessages, org.ID, bia.ID, nil, path, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "reunião às 15h", decodeChat[chatMessagesData](t, raw).Messages[0].Body)

	// 3. Ana edits: the teammate is told.
	code, _ = chatDo(t, app.UpdateChatMessage, org.ID, ana.ID, map[string]any{"body": "reunião às 16h"},
		map[string]string{"id": sent.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	pAna.expect(t, ws.TypeTeamChatMessageUpdated)
	pBia.expect(t, ws.TypeTeamChatMessageUpdated)
	pCarol.expectSilence(t)

	// 4. Bia types: Ana sees it, Bia does not.
	code, _ = chatDo(t, app.NotifyChatTyping, org.ID, bia.ID, map[string]any{}, path, nil)
	require.Equal(t, fasthttp.StatusNoContent, code)
	assert.Equal(t, bia.ID, pAna.expect(t, ws.TypeTeamChatTyping).UserID)
	pBia.expectSilence(t)
	pCarol.expectSilence(t)

	// 5. Bia reads: only Bia's own connection hears it.
	code, _ = chatDo(t, app.MarkChatChannelRead, org.ID, bia.ID, nil, path, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	pBia.expect(t, ws.TypeTeamChatRead)
	pAna.expectSilence(t)

	// 6. An outsider trying to type, read or send in the channel changes nothing for anyone.
	for _, h := range []func(*fastglue.Request) error{app.NotifyChatTyping, app.MarkChatChannelRead} {
		code, _ = chatDo(t, h, org.ID, carol.ID, map[string]any{}, path, nil)
		assert.Equal(t, fasthttp.StatusNotFound, code)
	}
	code, _ = sendMsg(t, app, org.ID, carol.ID, chID, "intruso")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = sendMsg(t, app, otherOrg.ID, dan.ID, chID, "intruso de outra org")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	for _, p := range []*wsPerson{pAna, pBia, pCarol, pDan} {
		p.expectSilence(t)
	}

	// 7. Ana deletes: the teammate is told, outsiders are not.
	code, _ = chatDo(t, app.DeleteChatMessage, org.ID, ana.ID, nil, map[string]string{"id": sent.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	pAna.expect(t, ws.TypeTeamChatMessageDeleted)
	pBia.expect(t, ws.TypeTeamChatMessageDeleted)
	pCarol.expectSilence(t)

	// 8. Bia leaves the team: from now on she hears nothing from the channel,
	// even though her socket is still connected, and REST refuses her too.
	require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, bia.ID).Delete(&models.TeamMember{}).Error)
	_, _ = sendMsg(t, app, org.ID, ana.ID, chID, "depois que a Bia saiu")
	pAna.expect(t, ws.TypeTeamChatMessage)
	pBia.expectSilence(t)
	code, _ = chatDo(t, app.ListChatMessages, org.ID, bia.ID, nil, path, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

func TestChatWSEndToEnd_ChannelAndDMEventsReachOnlyTheirMembers(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, org.ID, chatAdmKeys)
	member := chatUser(t, app, org.ID, chatUserKeys)
	other := chatUser(t, app, org.ID, chatUserKeys)

	rig := newWSRig(t, app)
	pAdmin := rig.connect("admin", admin)
	pMember := rig.connect("member", member)
	pOther := rig.connect("other", other)
	rig.waitOnline(org.ID, admin, member, other)

	code, _ := chatDo(t, app.CreateChatChannel, org.ID, admin.ID,
		map[string]any{"name": "ops", "member_ids": []uuid.UUID{member.ID}}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	pAdmin.expect(t, ws.TypeTeamChatChannel)
	pMember.expect(t, ws.TypeTeamChatChannel)
	pOther.expectSilence(t)

	code, raw := chatDo(t, app.CreateChatDM, org.ID, member.ID, map[string]any{"user_id": other.ID}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	dm := decodeChat[handlers.ChatChannelResponse](t, raw)
	pMember.expect(t, ws.TypeTeamChatChannel)
	pOther.expect(t, ws.TypeTeamChatChannel)
	pAdmin.expectSilence(t) // the admin is not in this DM, whatever their permissions

	_, _ = sendMsg(t, app, org.ID, member.ID, dm.ID, "só nós dois")
	pMember.expect(t, ws.TypeTeamChatMessage)
	pOther.expect(t, ws.TypeTeamChatMessage)
	pAdmin.expectSilence(t)
	code, _ = chatDo(t, app.ListChatMessages, org.ID, admin.ID, nil, map[string]string{"id": dm.ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code, "team_chat:manage does not open someone else's DM")
}
