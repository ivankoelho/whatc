package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func (w *chatWorld) channelsOf(t *testing.T, orgID, user uuid.UUID) []handlers.ChatChannelResponse {
	code, raw := chatDo(t, w.app.ListChatChannels, orgID, user, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	return decodeChat[chatChannelsData](t, raw).Channels
}

func (w *chatWorld) caseChannel(chs []handlers.ChatChannelResponse) *handlers.ChatChannelResponse {
	for i := range chs {
		if chs[i].Kind == models.ChatChannelKindOccurrence {
			return &chs[i]
		}
	}
	return nil
}

func (w *chatWorld) search(t *testing.T, orgID, user uuid.UUID, q string) (int, []handlers.ChatChannelResponse) {
	code, raw := chatDo(t, w.app.SearchChatOccurrences, orgID, user, nil, nil, map[string]string{"q": q})
	if code != fasthttp.StatusOK {
		return code, nil
	}
	return code, decodeChat[chatChannelsData](t, raw).Channels
}

// The discussions of cases appear in the Chat Interno module: the list holds the ones
// the person takes part in, search reaches the rest of what they may see, and nothing
// outside the case's scope is ever listed or found.
func TestChatInterno_ListsAndSearchesCaseDiscussions(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	var occ models.Occurrence
	require.NoError(t, w.app.DB.First(&occ, "id = ?", w.occID).Error)

	// Nothing to list until someone writes in the discussion.
	assert.Nil(t, w.caseChannel(w.channelsOf(t, w.org.ID, w.sac.ID)))

	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "SAC: cliente aguardando previsão"))

	// The SAC that opened the case sees it, named by protocol, linked to the case.
	mine := w.caseChannel(w.channelsOf(t, w.org.ID, w.sac.ID))
	require.NotNil(t, mine)
	assert.Contains(t, mine.Name, occ.ProtocolNumber)
	require.NotNil(t, mine.OccurrenceID)
	assert.Equal(t, w.occID, *mine.OccurrenceID)
	assert.EqualValues(t, 0, mine.UnreadCount, "their own message is not unread")

	// A team member who is not involved yet does not get it in the list...
	assert.Nil(t, w.caseChannel(w.channelsOf(t, w.org.ID, w.logPorto.ID)))
	// ...but finds it by protocol, title or customer, and opening it works.
	code, found := w.search(t, w.org.ID, w.logPorto.ID, occ.ProtocolNumber)
	require.Equal(t, fasthttp.StatusOK, code)
	require.Len(t, found, 1)
	assert.Equal(t, mine.ID, found[0].ID)
	assert.EqualValues(t, 1, found[0].UnreadCount)
	code, _ = w.read(t, w.logPorto.ID)
	assert.Equal(t, fasthttp.StatusOK, code)

	// Once they answered (or read) it, it is theirs: listed, with unread tracking for both sides.
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.logPorto.ID, "Logística: saída amanhã"))
	require.NotNil(t, w.caseChannel(w.channelsOf(t, w.org.ID, w.logPorto.ID)))
	assert.EqualValues(t, 1, w.caseChannel(w.channelsOf(t, w.org.ID, w.sac.ID)).UnreadCount, "the SAC has one new message")

	// Short queries are refused; case-insensitive search; wildcards stay literal.
	code, _ = w.search(t, w.org.ID, w.logPorto.ID, "a")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	_, found = w.search(t, w.org.ID, w.logPorto.ID, "  "+occ.ProtocolNumber[:6]+"  ")
	assert.Len(t, found, 1)
	_, found = w.search(t, w.org.ID, w.logPorto.ID, "%%")
	assert.Empty(t, found)

	// Outside the case's scope: another store, another organization, no CRM access.
	_, found = w.search(t, w.org.ID, w.logOutra.ID, occ.ProtocolNumber)
	assert.Empty(t, found, "another store")
	assert.Nil(t, w.caseChannel(w.channelsOf(t, w.org.ID, w.logOutra.ID)))
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, caseSupervisorPerms)
	_, found = w.search(t, other.org.ID, foreign.ID, occ.ProtocolNumber)
	assert.Empty(t, found, "another organization, even supervising all of its own cases")
	noCRM := chatUser(t, w.app, w.org.ID, []string{"team_chat:read", "team_chat:write"})
	_, found = w.search(t, w.org.ID, noCRM.ID, occ.ProtocolNumber)
	assert.Empty(t, found, "chat access without occurrences:read")
}
