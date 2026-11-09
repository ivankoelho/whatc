package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type pollEnvelope struct {
	Poll *handlers.ChatPollResponse `json:"poll"`
}

func TestChatPoll_CreateVoteAndTally(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	author := chatUser(t, app, org.ID, chatUserKeys)
	peer := chatUser(t, app, org.ID, chatUserKeys)
	outsider := chatUser(t, app, org.ID, chatUserKeys)
	team := createTeamWithMember(t, app, org.ID, author.ID)
	require.NoError(t, app.DB.Create(&models.TeamMember{
		BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: peer.ID, Role: models.TeamRoleAgent}).Error)
	ch := listChannels(t, app, org.ID, author.ID)[0]
	path := map[string]string{"id": ch.ID.String()}

	// Bad polls are refused.
	for _, body := range []map[string]any{
		{"question": "", "options": []string{"a", "b"}},
		{"question": "q", "options": []string{"only"}},
		{"question": "q", "options": []string{"a", "A"}},
		{"question": "q", "options": []string{"a", " "}},
	} {
		code, _ := chatDo(t, app.CreateChatPoll, org.ID, author.ID, body, path, nil)
		assert.Equal(t, fasthttp.StatusBadRequest, code, "%v", body)
	}
	// Someone outside the channel cannot create one: the channel does not exist for them.
	code, _ := chatDo(t, app.CreateChatPoll, org.ID, outsider.ID, map[string]any{"question": "q", "options": []string{"a", "b"}}, path, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code)

	code, raw := chatDo(t, app.CreateChatPoll, org.ID, author.ID,
		map[string]any{"question": "Almoço?", "options": []string{"Pizza", "Sushi", "Salada"}, "multiple": false}, path, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	msg := decodeChat[handlers.ChatMessageResponse](t, raw)
	require.NotNil(t, msg.Poll)
	assert.Equal(t, "Almoço?", msg.Body)
	require.Len(t, msg.Poll.Options, 3)
	vote := func(user uuid.UUID, ids ...string) (int, *handlers.ChatPollResponse) {
		code, raw := chatDo(t, app.VoteChatPoll, org.ID, user, map[string]any{"option_ids": ids}, map[string]string{"id": msg.ID.String()}, nil)
		if code != fasthttp.StatusOK {
			return code, nil
		}
		var env pollEnvelope
		require.NoError(t, json.Unmarshal(raw, &struct {
			Data *pollEnvelope `json:"data"`
		}{&env}))
		return code, env.Poll
	}

	// Single choice: one option, replaced when the voter changes their mind.
	code, p := vote(author.ID, "1")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, 1, p.Options[0].Count)
	assert.True(t, p.Options[0].Mine)
	code, _ = vote(peer.ID, "1")
	assert.Equal(t, fasthttp.StatusOK, code)
	code, p = vote(author.ID, "2")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, 1, p.Options[0].Count, "the peer's vote stays")
	assert.Equal(t, 1, p.Options[1].Count)
	assert.False(t, p.Options[0].Mine)
	assert.Equal(t, 2, p.Voters)
	// Not two options on a single-choice poll, nor an unknown option.
	code, _ = vote(author.ID, "1", "2")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = vote(author.ID, "99")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	// Taking the vote back.
	code, p = vote(author.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, 1, p.Voters)
	// Outsiders cannot vote.
	code, _ = vote(outsider.ID, "1")
	assert.Equal(t, fasthttp.StatusNotFound, code)

	// The list shows the tally to each viewer, with their own choice.
	code, raw = chatDo(t, app.ListChatMessages, org.ID, peer.ID, nil, path, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	listed := decodeChat[chatMessagesData](t, raw).Messages
	require.NotEmpty(t, listed)
	last := listed[len(listed)-1]
	require.NotNil(t, last.Poll)
	assert.True(t, last.Poll.Options[0].Mine)

	// A poll is not editable.
	code, _ = chatDo(t, app.UpdateChatMessage, org.ID, author.ID, map[string]any{"body": "outra"}, map[string]string{"id": msg.ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// A multiple-choice poll takes several options.
	_, raw = chatDo(t, app.CreateChatPoll, org.ID, author.ID,
		map[string]any{"question": "Dias?", "options": []string{"Seg", "Ter"}, "multiple": true}, path, nil)
	multi := decodeChat[handlers.ChatMessageResponse](t, raw)
	code, raw = chatDo(t, app.VoteChatPoll, org.ID, peer.ID, map[string]any{"option_ids": []string{"1", "2"}}, map[string]string{"id": multi.ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, code, string(raw))
}
