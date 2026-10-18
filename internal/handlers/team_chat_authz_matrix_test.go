package handlers_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// teamChatKeysOf returns the team_chat permissions a SYSTEM role gets, straight
// from the production role definitions, so the matrix tests the real roles.
func teamChatKeysOf(role string) []string {
	var keys []string
	for _, k := range models.SystemRolePermissions()[role] {
		if strings.HasPrefix(k, "team_chat:") {
			keys = append(keys, k)
		}
	}
	return keys
}

type chatState struct {
	bodies   map[uuid.UUID]string
	messages int64
	members  int64
	reads    int64
	channels int64
}

func snapshotChat(t *testing.T, app *handlers.App, orgIDs ...uuid.UUID) chatState {
	t.Helper()
	s := chatState{bodies: map[uuid.UUID]string{}}
	var msgs []models.ChatMessage
	require.NoError(t, app.DB.Unscoped().Where("organization_id IN ?", orgIDs).Find(&msgs).Error)
	for _, m := range msgs {
		s.bodies[m.ID] = m.Body
		if !m.DeletedAt.Valid {
			s.messages++
		}
	}
	app.DB.Model(&models.ChatChannelMember{}).Where("organization_id IN ?", orgIDs).Count(&s.members)
	app.DB.Model(&models.ChatRead{}).Where("organization_id IN ?", orgIDs).Count(&s.reads)
	app.DB.Model(&models.ChatChannel{}).Where("organization_id IN ?", orgIDs).Count(&s.channels)
	return s
}

// A denied request must change nothing, whatever ids it carried.
func TestChatAuthorizationMatrix(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)

	// The real system roles, in the real shape.
	owner := chatUser(t, app, org.ID, teamChatKeysOf("agent"))        // an agent: use, not administer
	manager := chatUser(t, app, org.ID, teamChatKeysOf("manager"))    // a manager
	outsider := chatUser(t, app, org.ID, teamChatKeysOf("manager"))   // a manager of the SAME org, not in the channel
	foreign := chatUser(t, app, otherOrg.ID, teamChatKeysOf("admin")) // an admin of ANOTHER org
	noPerm := chatUser(t, app, org.ID, []string{"chat:read", "chat:write"})
	readOnly := chatUser(t, app, org.ID, []string{"team_chat:read"})

	// A free channel with owner, manager, noPerm and readOnly inside; a message by the owner.
	code, raw := chatDo(t, app.CreateChatChannel, org.ID, manager.ID,
		map[string]any{"name": "ops", "member_ids": []uuid.UUID{owner.ID, noPerm.ID, readOnly.ID}}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	ch := decodeChat[handlers.ChatChannelResponse](t, raw)
	_, msg := sendMsg(t, app, org.ID, owner.ID, ch.ID, "original")
	chPath := map[string]string{"id": ch.ID.String()}
	msgPath := map[string]string{"id": msg.ID.String()}

	type call struct {
		name string
		h    func(*fastglue.Request) error
		body any
		path map[string]string
	}
	calls := []call{
		{"list messages", app.ListChatMessages, nil, chPath},
		{"send", app.SendChatMessage, map[string]any{"body": "x"}, chPath},
		{"mark read", app.MarkChatChannelRead, nil, chPath},
		{"typing", app.NotifyChatTyping, map[string]any{}, chPath},
		{"edit", app.UpdateChatMessage, map[string]any{"body": "hacked"}, msgPath},
		{"delete", app.DeleteChatMessage, nil, msgPath},
	}

	run := func(who *models.User, orgID uuid.UUID, c call) int {
		code, _ := chatDo(t, c.h, orgID, who.ID, c.body, c.path, nil)
		return code
	}

	t.Run("someone outside the channel, even a manager of the same organization, is told it does not exist", func(t *testing.T) {
		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		for _, c := range calls {
			assert.Equal(t, fasthttp.StatusNotFound, run(outsider, org.ID, c), c.name)
		}
		assert.Equal(t, before, snapshotChat(t, app, org.ID, otherOrg.ID), "nothing changed")
	})

	t.Run("an admin of another organization, with the right ids, is told it does not exist", func(t *testing.T) {
		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		for _, c := range calls {
			assert.Equal(t, fasthttp.StatusNotFound, run(foreign, otherOrg.ID, c), c.name)
		}
		assert.Equal(t, before, snapshotChat(t, app, org.ID, otherOrg.ID), "nothing changed")
	})

	t.Run("a member whose role has no team_chat permission is refused outright", func(t *testing.T) {
		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		for _, c := range calls {
			assert.Equal(t, fasthttp.StatusForbidden, run(noPerm, org.ID, c), c.name)
		}
		code, _ := chatDo(t, app.ListChatChannels, org.ID, noPerm.ID, nil, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "list channels")
		code, _ = chatDo(t, app.ListChatUsers, org.ID, noPerm.ID, nil, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "list users")
		assert.Equal(t, before, snapshotChat(t, app, org.ID, otherOrg.ID))
	})

	t.Run("a read-only member can read but cannot write, type, edit or delete", func(t *testing.T) {
		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		assert.Equal(t, fasthttp.StatusOK, run(readOnly, org.ID, calls[0]), "list messages")
		for _, c := range []call{calls[1], calls[3], calls[4]} {
			assert.Equal(t, fasthttp.StatusForbidden, run(readOnly, org.ID, c), c.name)
		}
		assert.Equal(t, fasthttp.StatusForbidden, run(readOnly, org.ID, calls[5]), "delete someone else's")
		code, _ := chatDo(t, app.CreateChatDM, org.ID, readOnly.ID, map[string]any{"user_id": owner.ID}, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "open a DM")
		assert.Equal(t, before.messages, snapshotChat(t, app, org.ID, otherOrg.ID).messages)
		assert.Equal(t, "original", snapshotChat(t, app, org.ID).bodies[msg.ID])
	})

	t.Run("an agent uses the chat but cannot administer it", func(t *testing.T) {
		code, _ := chatDo(t, app.CreateChatChannel, org.ID, owner.ID, map[string]any{"name": "nope"}, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "create channel")
		_, other := sendMsg(t, app, org.ID, manager.ID, ch.ID, "from the manager")
		code, _ = chatDo(t, app.DeleteChatMessage, org.ID, owner.ID, nil, map[string]string{"id": other.ID.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "delete someone else's message")
		code, _ = chatDo(t, app.UpdateChatMessage, org.ID, owner.ID, map[string]any{"body": "x"}, map[string]string{"id": other.ID.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "edit someone else's message")
		assert.Equal(t, fasthttp.StatusOK, run(owner, org.ID, calls[0]), "but reads")
		assert.Equal(t, fasthttp.StatusOK, run(owner, org.ID, calls[1]), "and writes")
	})

	t.Run("a manager moderates inside channels they belong to, and edits only their own words", func(t *testing.T) {
		_, mine := sendMsg(t, app, org.ID, owner.ID, ch.ID, "to be moderated")
		code, _ := chatDo(t, app.UpdateChatMessage, org.ID, manager.ID, map[string]any{"body": "rewritten"},
			map[string]string{"id": mine.ID.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "manager cannot rewrite another person's message")
		code, _ = chatDo(t, app.DeleteChatMessage, org.ID, manager.ID, nil, map[string]string{"id": mine.ID.String()}, nil)
		assert.Equal(t, fasthttp.StatusOK, code, "manager can remove it")
	})

	t.Run("swapping ids: a message of a channel you are not in cannot be reached through any other channel you are in", func(t *testing.T) {
		// owner is in `ch` only. A second channel exists that owner is not in.
		code, raw := chatDo(t, app.CreateChatChannel, org.ID, manager.ID, map[string]any{"name": "private"}, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		secret := decodeChat[handlers.ChatChannelResponse](t, raw)
		_, secretMsg := sendMsg(t, app, org.ID, manager.ID, secret.ID, "board only")
		secretMsgPath := map[string]string{"id": secretMsg.ID.String()}

		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		for _, h := range []func(*fastglue.Request) error{app.UpdateChatMessage, app.DeleteChatMessage} {
			code, _ := chatDo(t, h, org.ID, owner.ID, map[string]any{"body": "x"}, secretMsgPath, nil)
			assert.Equal(t, fasthttp.StatusNotFound, code)
		}
		// Reading the secret channel's history through its id, or paging it with a
		// cursor taken from a message that belongs to it, is refused the same way.
		code, _ = chatDo(t, app.ListChatMessages, org.ID, owner.ID, nil,
			map[string]string{"id": secret.ID.String()}, map[string]string{"before_id": secretMsg.ID.String()})
		assert.Equal(t, fasthttp.StatusNotFound, code)
		// A cursor from the secret channel used on a channel the owner CAN read must not leak it.
		code, raw = chatDo(t, app.ListChatMessages, org.ID, owner.ID, nil, chPath,
			map[string]string{"before_id": secretMsg.ID.String()})
		assert.Equal(t, fasthttp.StatusBadRequest, code, "a foreign cursor is rejected, not honoured")
		assert.NotContains(t, string(raw), "board only")
		assert.Equal(t, before, snapshotChat(t, app, org.ID, otherOrg.ID))

		// And the owner's channel list never mentions it.
		for _, c := range listChannels(t, app, org.ID, owner.ID) {
			assert.NotEqual(t, secret.ID, c.ID)
		}
	})

	t.Run("a DM across organizations is impossible in both directions", func(t *testing.T) {
		before := snapshotChat(t, app, org.ID, otherOrg.ID)
		code, _ := chatDo(t, app.CreateChatDM, org.ID, owner.ID, map[string]any{"user_id": foreign.ID}, nil, nil)
		assert.Equal(t, fasthttp.StatusNotFound, code)
		code, _ = chatDo(t, app.CreateChatDM, otherOrg.ID, foreign.ID, map[string]any{"user_id": owner.ID}, nil, nil)
		assert.Equal(t, fasthttp.StatusNotFound, code)
		assert.Equal(t, before, snapshotChat(t, app, org.ID, otherOrg.ID))
	})

	t.Run("people lists never cross organizations and carry no e-mail", func(t *testing.T) {
		code, raw := chatDo(t, app.ListChatUsers, org.ID, owner.ID, nil, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		assert.NotContains(t, string(raw), foreign.ID.String())
		assert.NotContains(t, string(raw), "@example.com")
	})

	t.Run("a team channel follows the team: leaving closes it, rejoining reopens it", func(t *testing.T) {
		team := createTeamWithMember(t, app, org.ID, owner.ID)
		tc := channelOfTeam(t, listChannels(t, app, org.ID, owner.ID), team.ID)
		path := map[string]string{"id": tc.ID.String()}
		require.Equal(t, fasthttp.StatusOK, mustCode(t, app.ListChatMessages, org.ID, owner.ID, path))

		require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, owner.ID).Delete(&models.TeamMember{}).Error)
		assert.Equal(t, fasthttp.StatusNotFound, mustCode(t, app.ListChatMessages, org.ID, owner.ID, path))
		assert.Equal(t, fasthttp.StatusNotFound, mustCode(t, app.MarkChatChannelRead, org.ID, owner.ID, path))
		for _, c := range listChannels(t, app, org.ID, owner.ID) {
			assert.NotEqual(t, tc.ID, c.ID, "the channel disappears from the list")
		}

		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: owner.ID, Role: models.TeamRoleAgent}).Error)
		assert.Equal(t, fasthttp.StatusOK, mustCode(t, app.ListChatMessages, org.ID, owner.ID, path))
	})
}

func mustCode(t *testing.T, h func(*fastglue.Request) error, orgID, userID uuid.UUID, path map[string]string) int {
	t.Helper()
	code, _ := chatDo(t, h, orgID, userID, nil, path, nil)
	return code
}
