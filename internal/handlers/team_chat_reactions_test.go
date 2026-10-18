package handlers_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// Reactions and replies (spec 2026-10-17). Uses the Phase A fixture: ch has owner
// and member, spare is outside it, foreign belongs to another organization.

func (f *phaseA) msg(t *testing.T, ch *models.ChatChannel, from *models.User, body string) handlers.ChatMessageResponse {
	t.Helper()
	code, m := sendMsg(t, f.app, f.org.ID, from.ID, ch.ID, body)
	require.Equal(t, fasthttp.StatusOK, code)
	return m
}

func (f *phaseA) reply(t *testing.T, ch *models.ChatChannel, from *models.User, body string, parent uuid.UUID) (int, handlers.ChatMessageResponse, string) {
	t.Helper()
	code, raw := chatDo(t, f.app.SendChatMessage, from.OrganizationID, from.ID,
		map[string]any{"body": body, "parent_id": parent.String()}, map[string]string{"id": ch.ID.String()}, nil)
	if code != fasthttp.StatusOK {
		return code, handlers.ChatMessageResponse{}, string(raw)
	}
	return code, decodeChat[handlers.ChatMessageResponse](t, raw), ""
}

func (f *phaseA) react(t *testing.T, add bool, u *models.User, msgID uuid.UUID, code string) (int, string) {
	t.Helper()
	h := f.app.PutChatReaction
	if !add {
		h = f.app.DeleteChatReaction
	}
	status, raw := chatDo(t, h, u.OrganizationID, u.ID, nil, map[string]string{"id": msgID.String(), "code": code}, nil)
	return status, string(raw)
}

func (f *phaseA) fetch(t *testing.T, u *models.User, msgID uuid.UUID) (int, handlers.ChatMessageResponse) {
	t.Helper()
	status, raw := chatDo(t, f.app.GetChatMessage, u.OrganizationID, u.ID, nil, map[string]string{"id": msgID.String()}, nil)
	if status != fasthttp.StatusOK {
		return status, handlers.ChatMessageResponse{}
	}
	return status, decodeChat[handlers.ChatMessageResponse](t, raw)
}

func (f *phaseA) page(t *testing.T, u *models.User, ch *models.ChatChannel) []handlers.ChatMessageResponse {
	t.Helper()
	code, raw := chatDo(t, f.app.ListChatMessages, u.OrganizationID, u.ID, nil, map[string]string{"id": ch.ID.String()}, map[string]string{"limit": "100"})
	require.Equal(t, fasthttp.StatusOK, code)
	return decodeChat[chatMessagesData](t, raw).Messages
}

func (f *phaseA) archiveNow(t *testing.T, ch *models.ChatChannel) {
	t.Helper()
	code, _ := f.call(t, f.app.ArchiveChatChannel, f.org, f.owner, nil, f.path(ch))
	require.Equal(t, fasthttp.StatusOK, code)
}

func TestReplies_QuoteAndRules(t *testing.T) {
	f := newPhaseA(t)
	orig := f.msg(t, f.ch, f.owner, "Pode conferir o pedido 123?")

	t.Run("a valid reply carries the quote", func(t *testing.T) {
		code, r, _ := f.reply(t, f.ch, f.member, "Fechado", orig.ID)
		require.Equal(t, fasthttp.StatusOK, code)
		require.NotNil(t, r.ReplyTo)
		assert.Equal(t, orig.ID, r.ReplyTo.ID)
		assert.Equal(t, orig.UserName, r.ReplyTo.UserName)
		assert.Equal(t, "Pode conferir o pedido 123?", r.ReplyTo.Snippet)
		assert.False(t, r.ReplyTo.Deleted)
		assert.NotNil(t, r.ParentID)
	})

	t.Run("a long original is cut to 140 characters", func(t *testing.T) {
		long := f.msg(t, f.ch, f.owner, strings.Repeat("a", 300))
		_, r, _ := f.reply(t, f.ch, f.member, "ok", long.ID)
		assert.Equal(t, strings.Repeat("a", 140)+"…", r.ReplyTo.Snippet)
	})

	t.Run("editing the original updates the quote", func(t *testing.T) {
		code, _ := chatDo(t, f.app.UpdateChatMessage, f.org.ID, f.owner.ID, map[string]any{"body": "editado"},
			map[string]string{"id": orig.ID.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		for _, m := range f.page(t, f.member, f.ch) {
			if m.ReplyTo != nil && m.ReplyTo.ID == orig.ID {
				assert.Equal(t, "editado", m.ReplyTo.Snippet)
			}
		}
	})

	t.Run("targets that cannot be replied to are refused with reply_target_gone", func(t *testing.T) {
		otherCh := f.freeChannel(t, "other", f.owner, f.member)
		inOther := f.msg(t, otherCh, f.owner, "elsewhere")
		sys := models.ChatMessage{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ChannelID: f.ch.ID,
			UserID: f.owner.ID, Kind: models.ChatMessageKindSystem}
		require.NoError(t, f.app.DB.Create(&sys).Error)
		gone := f.msg(t, f.ch, f.owner, "to be deleted")
		require.NoError(t, f.app.DB.Delete(&models.ChatMessage{}, "id = ?", gone.ID).Error)

		for name, id := range map[string]uuid.UUID{
			"unknown": uuid.New(), "other channel": inOther.ID, "system": sys.ID, "deleted": gone.ID,
		} {
			code, _, body := f.reply(t, f.ch, f.member, "x", id)
			assert.Equal(t, fasthttp.StatusBadRequest, code, name)
			assert.Contains(t, body, "reply_target_gone", name)
		}
	})

	t.Run("a deleted original shows as removed, with no text and no author", func(t *testing.T) {
		o := f.msg(t, f.ch, f.owner, "secret words")
		_, _, _ = f.reply(t, f.ch, f.member, "reply", o.ID)
		require.NoError(t, f.app.DB.Delete(&models.ChatMessage{}, "id = ?", o.ID).Error)
		var found bool
		for _, m := range f.page(t, f.member, f.ch) {
			if m.ParentID != nil && *m.ParentID == o.ID {
				found = true
				require.NotNil(t, m.ReplyTo)
				assert.True(t, m.ReplyTo.Deleted)
				assert.Empty(t, m.ReplyTo.Snippet)
				assert.Empty(t, m.ReplyTo.UserName)
			}
		}
		assert.True(t, found)
	})

	t.Run("read-only users cannot reply", func(t *testing.T) {
		code, _, _ := f.reply(t, f.ch, f.readOnl, "x", orig.ID)
		assert.Equal(t, fasthttp.StatusForbidden, code)
	})

	t.Run("an archived channel refuses replies", func(t *testing.T) {
		ch := f.freeChannel(t, "old", f.owner, f.member)
		o := f.msg(t, ch, f.owner, "before")
		f.archiveNow(t, ch)
		code, _, body := f.reply(t, ch, f.member, "late", o.ID)
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Contains(t, body, "archived")
	})
}

func TestReactions_AddRemoveAndShape(t *testing.T) {
	f := newPhaseA(t)
	m := f.msg(t, f.ch, f.owner, "hello")

	code, _ := f.react(t, true, f.member, m.ID, "thumbs_up")
	require.Equal(t, fasthttp.StatusOK, code)
	// Adding again is idempotent.
	code, _ = f.react(t, true, f.member, m.ID, "thumbs_up")
	require.Equal(t, fasthttp.StatusOK, code)
	require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.owner, m.ID, "thumbs_up")))
	require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.owner, m.ID, "heart")))

	var rows int64
	f.app.DB.Model(&models.ChatMessageReaction{}).Where("message_id = ?", m.ID).Count(&rows)
	assert.EqualValues(t, 3, rows, "one row per (person, emoji)")

	_, asMember := f.fetch(t, f.member, m.ID)
	require.Len(t, asMember.Reactions, 2)
	assert.Equal(t, "thumbs_up", asMember.Reactions[0].Code)
	assert.Equal(t, 2, asMember.Reactions[0].Count)
	assert.True(t, asMember.Reactions[0].Mine)
	assert.Len(t, asMember.Reactions[0].Names, 2)
	assert.False(t, asMember.Reactions[1].Mine, "the member did not use heart")

	_, asOwner := f.fetch(t, f.owner, m.ID)
	assert.True(t, asOwner.Reactions[1].Mine)

	// Remove, twice (idempotent).
	require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, false, f.member, m.ID, "thumbs_up")))
	require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, false, f.member, m.ID, "thumbs_up")))
	_, after := f.fetch(t, f.owner, m.ID)
	assert.Equal(t, 1, after.Reactions[0].Count)

	// Names are capped at five and never carry an e-mail.
	many := f.msg(t, f.ch, f.owner, "popular")
	for i := 0; i < 8; i++ {
		u := chatUser(t, f.app, f.org.ID, teamChatKeysOf("agent"))
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{OrganizationID: f.org.ID, ChannelID: f.ch.ID, UserID: u.ID, Role: "member"}).Error)
		require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, u, many.ID, "fire")))
	}
	_, pop := f.fetch(t, f.owner, many.ID)
	assert.Equal(t, 8, pop.Reactions[0].Count)
	assert.Len(t, pop.Reactions[0].Names, 5)
	for _, n := range pop.Reactions[0].Names {
		assert.NotContains(t, n, "@")
	}

	// A reaction is not a message: no new rows, no change to the channel's activity.
	var msgs int64
	f.app.DB.Model(&models.ChatMessage{}).Where("channel_id = ? AND kind = ?", f.ch.ID, models.ChatMessageKindUser).Count(&msgs)
	assert.EqualValues(t, 2, msgs)
}

func okStatus(code int, _ string) int { return code }

func TestReactions_Refusals(t *testing.T) {
	f := newPhaseA(t)
	m := f.msg(t, f.ch, f.owner, "hello")

	t.Run("a code outside the palette", func(t *testing.T) {
		code, body := f.react(t, true, f.member, m.ID, "skull")
		assert.Equal(t, fasthttp.StatusBadRequest, code)
		assert.Contains(t, body, "bad_reaction")
		code, _ = f.react(t, true, f.member, m.ID, "👍")
		assert.Equal(t, fasthttp.StatusBadRequest, code)
	})

	t.Run("twenty-one different emojis on one message", func(t *testing.T) {
		full := f.msg(t, f.ch, f.owner, "full")
		for i := 0; i < 20; i++ {
			require.NoError(t, f.app.DB.Create(&models.ChatMessageReaction{
				ID: uuid.New(), OrganizationID: f.org.ID, ChannelID: f.ch.ID, MessageID: full.ID,
				UserID: f.owner.ID, Code: fmt.Sprintf("seed_%02d", i)}).Error)
		}
		code, body := f.react(t, true, f.member, full.ID, "clap")
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Contains(t, body, "too_many_reactions")
		// Joining an emoji already on the message is still fine.
		require.NoError(t, f.app.DB.Create(&models.ChatMessageReaction{
			ID: uuid.New(), OrganizationID: f.org.ID, ChannelID: f.ch.ID, MessageID: full.ID,
			UserID: f.owner.ID, Code: "heart"}).Error)
		// (20 kinds seeded + heart = 21 rows of kinds; the point is only that an existing kind may be joined)
		code, _ = f.react(t, true, f.member, full.ID, "heart")
		assert.Equal(t, fasthttp.StatusOK, code)
	})

	t.Run("system messages, deleted messages and outsiders get 404", func(t *testing.T) {
		sys := models.ChatMessage{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ChannelID: f.ch.ID,
			UserID: f.owner.ID, Kind: models.ChatMessageKindSystem}
		require.NoError(t, f.app.DB.Create(&sys).Error)
		code, _ := f.react(t, true, f.member, sys.ID, "heart")
		assert.Equal(t, fasthttp.StatusNotFound, code, "system")

		gone := f.msg(t, f.ch, f.owner, "gone")
		require.NoError(t, f.app.DB.Delete(&models.ChatMessage{}, "id = ?", gone.ID).Error)
		code, _ = f.react(t, true, f.member, gone.ID, "heart")
		assert.Equal(t, fasthttp.StatusNotFound, code, "deleted")

		code, _ = f.react(t, true, f.spare, m.ID, "heart")
		assert.Equal(t, fasthttp.StatusNotFound, code, "not in the channel")
		code, _ = f.react(t, true, f.foreign, m.ID, "heart")
		assert.Equal(t, fasthttp.StatusNotFound, code, "another organization")
		code, _ = f.fetch(t, f.foreign, m.ID)
		assert.Equal(t, fasthttp.StatusNotFound, code, "reading from another organization")
		code, _ = f.fetch(t, f.spare, m.ID)
		assert.Equal(t, fasthttp.StatusNotFound, code, "reading without access")
	})

	t.Run("write permission is required, reading is not enough", func(t *testing.T) {
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{OrganizationID: f.org.ID, ChannelID: f.ch.ID, UserID: f.readOnl.ID, Role: "member"}).Error)
		code, _ := f.react(t, true, f.readOnl, m.ID, "heart")
		assert.Equal(t, fasthttp.StatusForbidden, code)
		code, _ = f.fetch(t, f.readOnl, m.ID)
		assert.Equal(t, fasthttp.StatusOK, code, "but they can read the reactions")
	})

	t.Run("an archived channel refuses reacting and un-reacting", func(t *testing.T) {
		ch := f.freeChannel(t, "frozen", f.owner, f.member)
		o := f.msg(t, ch, f.owner, "before")
		require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.member, o.ID, "heart")))
		f.archiveNow(t, ch)
		code, body := f.react(t, true, f.member, o.ID, "clap")
		assert.Equal(t, fasthttp.StatusConflict, code)
		assert.Contains(t, body, "archived")
		code, _ = f.react(t, false, f.member, o.ID, "heart")
		assert.Equal(t, fasthttp.StatusConflict, code)
		code, got := f.fetch(t, f.member, o.ID)
		assert.Equal(t, fasthttp.StatusOK, code)
		assert.Len(t, got.Reactions, 1, "the history stays readable, reactions included")
	})
}

func TestReactions_EditKeepsDeleteHides(t *testing.T) {
	f := newPhaseA(t)
	m := f.msg(t, f.ch, f.owner, "v1")
	require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.member, m.ID, "heart")))
	code, _ := chatDo(t, f.app.UpdateChatMessage, f.org.ID, f.owner.ID, map[string]any{"body": "v2"}, map[string]string{"id": m.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	_, after := f.fetch(t, f.member, m.ID)
	assert.Len(t, after.Reactions, 1, "editing keeps reactions")

	code, _ = chatDo(t, f.app.DeleteChatMessage, f.org.ID, f.owner.ID, nil, map[string]string{"id": m.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	for _, x := range f.page(t, f.member, f.ch) {
		assert.NotEqual(t, m.ID, x.ID, "deleted messages leave the list together with their reactions")
	}
}

func TestReactions_ListUsesConstantQueries(t *testing.T) {
	f := newPhaseA(t)
	var queries int64
	require.NoError(t, f.app.DB.Callback().Query().After("gorm:query").Register("count_chat_reaction_queries", func(*gorm.DB) {
		atomic.AddInt64(&queries, 1)
	}))

	seed := func(ch *models.ChatChannel, n int) {
		prev := f.msg(t, ch, f.owner, "root")
		for i := 0; i < n; i++ {
			_, r, _ := f.reply(t, ch, f.member, fmt.Sprintf("m%d", i), prev.ID)
			require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.owner, r.ID, "heart")))
			require.Equal(t, fasthttp.StatusOK, okStatus(f.react(t, true, f.member, r.ID, "clap")))
			prev = r
		}
	}
	measure := func(ch *models.ChatChannel) int64 {
		atomic.StoreInt64(&queries, 0)
		f.page(t, f.member, ch)
		return atomic.LoadInt64(&queries)
	}

	small := f.freeChannel(t, "small", f.owner, f.member)
	big := f.freeChannel(t, "big", f.owner, f.member)
	seed(small, 3)
	seed(big, 40)
	assert.Equal(t, measure(small), measure(big), "the number of queries does not grow with the page")
}

// Two people reacting at once with the same emoji both count; the same person
// firing the same request repeatedly counts once. Never an error.
func TestReactions_Concurrency(t *testing.T) {
	f := newPhaseA(t)
	m := f.msg(t, f.ch, f.owner, "race")

	var bad int64
	fns := []func(){}
	for i := 0; i < 20; i++ {
		u := f.member
		if i%2 == 0 {
			u = f.owner
		}
		fns = append(fns, func() {
			if callSafe(f.app.PutChatReaction, f.org.ID, u.ID, nil, map[string]string{"id": m.ID.String(), "code": "party"}) != fasthttp.StatusOK {
				atomic.AddInt64(&bad, 1)
			}
		})
	}
	runTogether(fns...)
	assert.Zero(t, bad)
	_, got := f.fetch(t, f.owner, m.ID)
	require.Len(t, got.Reactions, 1)
	assert.Equal(t, 2, got.Reactions[0].Count, "two people, however many requests")
}

// Archiving while people react: every reaction either lands before the archive or
// is refused; none lands after it. Deadlocks would surface as 500.
func TestReactions_Concurrency_ArchiveVersusReact(t *testing.T) {
	f := newPhaseA(t)
	for round := 0; round < 5; round++ {
		ch := f.freeChannel(t, "busy", f.owner, f.member)
		m := f.msg(t, ch, f.owner, "target")
		var other int64
		fns := []func(){func() {
			if callSafe(f.app.ArchiveChatChannel, f.org.ID, f.owner.ID, nil, f.path(ch)) != fasthttp.StatusOK {
				atomic.AddInt64(&other, 1)
			}
		}}
		for i := 0; i < 20; i++ {
			fns = append(fns, func() {
				switch callSafe(f.app.PutChatReaction, f.org.ID, f.member.ID, nil, map[string]string{"id": m.ID.String(), "code": "eyes"}) {
				case fasthttp.StatusOK, fasthttp.StatusConflict:
				default:
					atomic.AddInt64(&other, 1)
				}
			})
		}
		runTogether(fns...)
		assert.Zero(t, other, "round %d", round)
		fresh := f.reload(t, ch)
		require.NotNil(t, fresh.ArchivedAt)
		var rows []models.ChatMessageReaction
		f.app.DB.Where("message_id = ?", m.ID).Find(&rows)
		for _, r := range rows {
			assert.False(t, r.CreatedAt.After(*fresh.ArchivedAt), "reaction after archive")
		}
		code, _ := f.react(t, true, f.owner, m.ID, "fire")
		assert.Equal(t, fasthttp.StatusConflict, code, "later reactions are refused")
	}
}
