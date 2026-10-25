package handlers_test

import (
	"sync"
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
	"github.com/zerodha/fastglue"
)

// chatWorld: a forwarded case (SAC -> Logistica Porto) and its discussion channel.
type chatWorld struct {
	*notifWorld
	occID uuid.UUID
	chID  uuid.UUID
}

func newChatWorld(t *testing.T) *chatWorld {
	t.Helper()
	w := &chatWorld{notifWorld: newNotifWorld(t)}
	w.occID = w.forward(t, nil)
	w.chID = w.chatID(t, w.sac.ID)
	return w
}

func (w *chatWorld) chatID(t *testing.T, user uuid.UUID) uuid.UUID {
	t.Helper()
	code, body := w.call(t, w.app.GetOccurrenceChat, user, w.occID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	return decodeChat[occChatState](t, body).ChannelID
}

type occChatState struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Unread    int64     `json:"unread"`
	CanPost   bool      `json:"can_post"`
	Messages  int64     `json:"messages"`
}

func (w *chatWorld) state(t *testing.T, user uuid.UUID) occChatState {
	code, body := w.call(t, w.app.GetOccurrenceChat, user, w.occID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	return decodeChat[occChatState](t, body)
}
func (w *chatWorld) say(t *testing.T, user uuid.UUID, text string) int {
	code, _ := chatDo(t, w.app.SendChatMessage, w.org.ID, user, map[string]any{"body": text}, map[string]string{"id": w.chID.String()}, nil)
	return code
}

func (w *chatWorld) read(t *testing.T, user uuid.UUID) (int, []handlers.ChatMessageResponse) {
	code, raw := chatDo(t, w.app.ListChatMessages, w.org.ID, user, nil, map[string]string{"id": w.chID.String()}, nil)
	if code != fasthttp.StatusOK {
		return code, nil
	}
	return code, decodeChat[chatMessagesData](t, raw).Messages
}

func TestOccurrenceChat_OneChannelPerCaseEvenUnderConcurrency(t *testing.T) {
	w := newChatWorld(t)
	ids := make([]uuid.UUID, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, body := w.call(t, w.app.GetOccurrenceChat, []uuid.UUID{w.sac.ID, w.logPorto.ID, w.supPorto.ID}[i%3], w.occID, nil, nil)
			if code == fasthttp.StatusOK {
				ids[i] = decodeChat[occChatState](t, body).ChannelID
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		assert.Equal(t, w.chID, id)
	}
	var n int64
	w.app.DB.Model(&models.ChatChannel{}).Where("occurrence_id = ?", w.occID).Count(&n)
	assert.EqualValues(t, 1, n)
}

func TestOccurrenceChat_ReadersWritersAndStrangers(t *testing.T) {
	w := newChatWorld(t)

	// SAC (participant with comment), the receiving team and the store supervisor talk.
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "Cliente informa atraso. Pedido 45872."))
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.logPorto.ID, "Vou consultar a expedição."))
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.supPorto.ID, "Acompanhando."))
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.supSAC.ID, "Posso intervir se precisar."))
	for _, u := range []*models.User{w.sac, w.logPorto, w.supPorto, w.supSAC} {
		code, msgs := w.read(t, u.ID)
		require.Equal(t, fasthttp.StatusOK, code, u.FullName)
		assert.Len(t, msgs, 4, u.FullName)
	}

	// Another store, another organization and someone without CRM access see a channel that "does not exist".
	assert.Equal(t, fasthttp.StatusNotFound, w.say(t, w.logOutra.ID, "intruso"))
	code, _ := w.read(t, w.logOutra.ID)
	assert.Equal(t, fasthttp.StatusNotFound, code)
	other := newIsoWorld(t)
	code, _ = chatDo(t, w.app.ListChatMessages, other.org.ID, other.supSAC.ID, nil, map[string]string{"id": w.chID.String()}, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code, "another organization, even a view_all admin")
	noCRM := chatUser(t, w.app, w.org.ID, []string{"team_chat:read", "team_chat:write"})
	assert.Equal(t, fasthttp.StatusNotFound, w.say(t, noCRM.ID, "x"))
	code, _ = w.call(t, w.app.GetOccurrenceChat, w.logOutra.ID, w.occID, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "the endpoint that hands out the channel obeys the case")

	// A read-only participant reads but cannot write.
	require.NoError(t, w.app.DB.Create(&models.OccurrenceParticipant{OrganizationID: w.org.ID, OccurrenceID: w.occID, UserID: w.logOutra.ID}).Error)
	code, msgs := w.read(t, w.logOutra.ID)
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Len(t, msgs, 4)
	assert.Equal(t, fasthttp.StatusForbidden, w.say(t, w.logOutra.ID, "ainda sem permissão"))
	assert.False(t, w.state(t, w.logOutra.ID).CanPost)
	assert.True(t, w.state(t, w.sac.ID).CanPost)

	// Revoking the participation closes the door at once.
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND user_id = ?", w.occID, w.logOutra.ID).Delete(&models.OccurrenceParticipant{}).Error)
	code, _ = w.read(t, w.logOutra.ID)
	assert.Equal(t, fasthttp.StatusNotFound, code)
}

func TestOccurrenceChat_UnreadCountsAndReadMarker(t *testing.T) {
	w := newChatWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "um"))
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "dois"))
	assert.EqualValues(t, 0, w.state(t, w.sac.ID).Unread, "your own messages are not unread")
	assert.EqualValues(t, 2, w.state(t, w.logPorto.ID).Unread)

	code, _ := chatDo(t, w.app.MarkChatChannelRead, w.org.ID, w.logPorto.ID, map[string]any{}, map[string]string{"id": w.chID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.EqualValues(t, 0, w.state(t, w.logPorto.ID).Unread)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "tres"))
	assert.EqualValues(t, 1, w.state(t, w.logPorto.ID).Unread)
}

// What reaches the wire: only connected users who may see the case, over the one hub.
func TestOccurrenceChat_RealtimeReachesOnlyThoseWhoMaySeeTheCase(t *testing.T) {
	w := newChatWorld(t)
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	hub.SetOccurrenceAuthorizer(w.app.CanViewOccurrenceByID)
	mk := func(u *models.User) *websocket.Client {
		c := websocket.NewClient(hub, nil, u.ID, w.org.ID)
		hub.Register(c)
		return c
	}
	logistica, stranger, sac := mk(w.logPorto), mk(w.logOutra), mk(w.sac)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.GetClientCount() != 3 {
		time.Sleep(5 * time.Millisecond)
	}
	w.app.WSHub = hub

	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "oi equipe"))
	readBroadcast(t, logistica, websocket.TypeTeamChatMessage)
	readBroadcast(t, sac, websocket.TypeTeamChatMessage)
	assertNoBroadcast(t, stranger)
}

// The discussion is internal by construction: nothing here reaches WhatsApp.
func TestOccurrenceChat_NeverProducesACustomerMessage(t *testing.T) {
	w := newChatWorld(t)
	w.run() // let the receipt template go out; it is the only legitimate request
	baseline := w.meta.count()
	var before int64
	w.app.DB.Model(&models.Message{}).Where("organization_id = ?", w.org.ID).Count(&before)

	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "isto é interno"))
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.logPorto.ID, "Cliente: pode ligar?"))
	w.run()

	var after int64
	w.app.DB.Model(&models.Message{}).Where("organization_id = ?", w.org.ID).Count(&after)
	assert.Equal(t, before, after, "no WhatsApp message row")
	assert.Equal(t, baseline, w.meta.count(), "nothing was sent to the provider")

	// And the other direction: a reply to the customer is not a chat message.
	var chatBefore int64
	w.app.DB.Model(&models.ChatMessage{}).Where("channel_id = ?", w.chID).Count(&chatBefore)
	w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, w.occID, map[string]any{}, nil)
	w.call(t, w.app.ReplyToOccurrence, w.logPorto.ID, w.occID, map[string]any{"content": "Olá, cliente"}, nil)
	var chatAfter int64
	w.app.DB.Model(&models.ChatMessage{}).Where("channel_id = ?", w.chID).Count(&chatAfter)
	assert.Equal(t, chatBefore, chatAfter)
}

func TestOccurrenceChat_ManagementRoutesDoNotApply(t *testing.T) {
	w := newChatWorld(t)
	admin := chatUser(t, w.app, w.org.ID, []string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:join_any", "occurrences:read", "occurrences:write", "conversations:view_all"})
	path := map[string]string{"id": w.chID.String()}
	for name, h := range map[string]func(*fastglue.Request) error{
		"rename":  w.app.RenameChatChannel,
		"archive": w.app.ArchiveChatChannel,
		"reopen":  w.app.ReopenChatChannel,
		"join":    w.app.JoinChatChannel,
		"add":     w.app.AddChatChannelMember,
		"leave":   w.app.LeaveChatChannel,
	} {
		code, _ := chatDo(t, h, w.org.ID, admin.ID, map[string]any{"name": "x", "reason": "x", "user_id": w.logPorto.ID.String()}, path, nil)
		assert.Contains(t, []int{fasthttp.StatusNotFound, fasthttp.StatusForbidden, fasthttp.StatusBadRequest}, code, name)
		assert.NotEqual(t, fasthttp.StatusOK, code, name)
	}
	// The channel stayed what it was, and does not show in anyone's channel list.
	var ch models.ChatChannel
	require.NoError(t, w.app.DB.First(&ch, w.chID).Error)
	assert.Nil(t, ch.ArchivedAt)
	assert.Equal(t, "", func() string {
		for _, c := range listChannels(t, w.app, w.org.ID, w.sac.ID) {
			if c.ID == w.chID {
				return "listed"
			}
		}
		return ""
	}())
}

// A resolved and reopened case keeps its discussion; old notes stay readable.
func TestOccurrenceChat_SurvivesReopeningAndKeepsOldNotes(t *testing.T) {
	w := newChatWorld(t)
	// A note written before the chat existed (the old internal-notes mechanism).
	code, _ := w.call(t, w.app.CreateOccurrenceEvent, w.sac.ID, w.occID, map[string]any{"content": "nota antiga"}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "mensagem 1"))

	code, _ = w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, w.occID, map[string]any{}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	for _, st := range []string{models.GeneralStateResolved, models.GeneralStateInProgress} {
		code, _ = w.call(t, w.app.ChangeOccurrenceStage, w.logPorto.ID, w.occID, map[string]any{"stage_id": w.stageOf(t, st).ID.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, code, st)
	}
	assert.Equal(t, w.chID, w.chatID(t, w.logPorto.ID), "same channel after reopening")
	_, msgs := w.read(t, w.logPorto.ID)
	assert.Len(t, msgs, 1)
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.logPorto.ID, "voltamos a tratar"))

	code, body := w.call(t, w.app.ListOccurrenceEvents, w.logPorto.ID, w.occID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, string(body), "nota antiga", "the old notes are still in the case history")
	assert.NotContains(t, string(body), "mensagem 1", "chat messages are not copied into the history (no duplicates)")
}

func TestOccurrenceChat_AttachmentsFollowTheCase(t *testing.T) {
	w := newChatWorld(t)
	f := &phaseA{app: w.app}
	f.withStorage(t)
	ch := &models.ChatChannel{}
	require.NoError(t, w.app.DB.First(ch, w.chID).Error)

	att := mustUpload(t, f, w.sac, ch, "foto.png", pngBytes(5, 5))
	code, _, body := f.sendFiles(t, w.sac, ch, "segue a foto do produto", att.ID)
	require.Equal(t, fasthttp.StatusOK, code, body)

	// Whoever may see the case downloads; nobody else, and a case reader cannot upload.
	assert.Equal(t, 200, testutil.GetResponseStatusCode(f.download(t, w.logPorto, att.ID, "")))
	assert.Equal(t, 200, testutil.GetResponseStatusCode(f.download(t, w.supPorto, att.ID, "")))
	assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, w.logOutra, att.ID, "")), "another store")
	foreign := newIsoWorld(t)
	assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, foreign.supSAC, att.ID, "")), "another organization")

	require.NoError(t, w.app.DB.Create(&models.OccurrenceParticipant{OrganizationID: w.org.ID, OccurrenceID: w.occID, UserID: w.logOutra.ID}).Error)
	assert.Equal(t, 200, testutil.GetResponseStatusCode(f.download(t, w.logOutra, att.ID, "")), "a participant reads")
	code, _, _ = f.upload(t, w.logOutra, ch, "x.png", pngBytes(4, 4))
	assert.Equal(t, fasthttp.StatusForbidden, code, "but cannot write")
}
