package handlers_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type historyPage struct {
	Events     []handlers.OccurrenceEventResponse `json:"events"`
	HasMore    bool                               `json:"has_more"`
	NextCursor string                             `json:"next_cursor"`
}

func (w *chatWorld) history(t *testing.T, u *models.User, occID uuid.UUID, query string) (int, historyPage) {
	code, raw, _ := w.fileCall(t, w.app.ListOccurrenceHistory, u, occID, "", query)
	var env struct {
		Data historyPage `json:"data"`
	}
	if code == fasthttp.StatusOK {
		require.NoError(t, json.Unmarshal(raw, &env))
	}
	return code, env.Data
}

func (w *chatWorld) addEvent(t *testing.T, typ models.OccurrenceEventType, content string, by uuid.UUID, at time.Time) {
	e := &models.OccurrenceEvent{OrganizationID: w.org.ID, OccurrenceID: w.occID, Type: typ, Content: content, CreatedByID: &by, Metadata: models.JSONB{}}
	e.ID = uuid.New()
	require.NoError(t, w.app.DB.Create(e).Error)
	require.NoError(t, w.app.DB.Model(e).UpdateColumn("created_at", at).Error)
}

func TestOccurrenceHistory_PagesByCursorAndStaysStable(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	base := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 25; i++ {
		w.addEvent(t, models.OccurrenceEventNote, fmt.Sprintf("nota %02d", i), w.logPorto.ID, base.Add(time.Duration(i)*time.Minute))
	}
	_, p1 := w.history(t, w.logPorto, w.occID, "limit=10&types=note")
	require.Len(t, p1.Events, 10)
	assert.True(t, p1.HasMore)
	assert.Equal(t, "nota 24", p1.Events[0].Content, "newest first")

	// New events arrive between the two pages: the second page is unaffected.
	w.addEvent(t, models.OccurrenceEventNote, "nota nova", w.logPorto.ID, time.Now())
	_, p2 := w.history(t, w.logPorto, w.occID, "limit=10&types=note&cursor="+p1.NextCursor)
	require.Len(t, p2.Events, 10)
	assert.Equal(t, "nota 14", p2.Events[0].Content)
	_, p3 := w.history(t, w.logPorto, w.occID, "limit=10&types=note&cursor="+p2.NextCursor)
	require.Len(t, p3.Events, 5)
	assert.False(t, p3.HasMore)

	seen := map[uuid.UUID]bool{}
	for _, e := range append(append(p1.Events, p2.Events...), p3.Events...) {
		assert.False(t, seen[e.ID], "no repeats across pages")
		seen[e.ID] = true
	}
	assert.Len(t, seen, 25)

	// Filter by type, search by text, bad input.
	_, f := w.history(t, w.logPorto, w.occID, "q=nota%2007&types=note")
	require.Len(t, f.Events, 1)
	code, _ := w.history(t, w.logPorto, w.occID, "cursor=garbage")
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	_, none := w.history(t, w.logPorto, w.occID, "types=bogus")
	assert.Empty(t, none.Events)
	_, capped := w.history(t, w.logPorto, w.occID, "limit=100000")
	assert.LessOrEqual(t, len(capped.Events), 100)
}

func TestOccurrenceHistory_WithholdsTheCustomerConversationAndAnsweredScope(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	w.addEvent(t, models.OccurrenceEventReply, "SEGREDO: valor combinado com o cliente", w.logPorto.ID, time.Now().Add(-time.Minute))

	// Who works the case reads it; the SAC that only follows it sees that it happened, not the text.
	_, ok := w.history(t, w.logPorto, w.occID, "types=reply")
	require.Len(t, ok.Events, 1)
	assert.Contains(t, ok.Events[0].Content, "SEGREDO")
	_, redacted := w.history(t, w.sac, w.occID, "types=reply")
	require.Len(t, redacted.Events, 1)
	assert.Empty(t, redacted.Events[0].Content)
	assert.True(t, redacted.Events[0].Redacted)
	_, hit := w.history(t, w.sac, w.occID, "q=SEGREDO")
	assert.Empty(t, hit.Events, "the withheld text cannot be found by search")

	// The old timeline endpoint obeys the same rule.
	_, raw, _ := w.fileCall(t, w.app.ListOccurrenceEvents, w.sac, w.occID, "", "")
	assert.NotContains(t, string(raw), "SEGREDO")

	// Scope: another store and another organization do not get a page.
	code, _ := w.history(t, w.logOutra, w.occID, "")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, caseSupervisorPerms)
	code, _ = w.history(t, foreign, w.occID, "")
	assert.NotEqual(t, fasthttp.StatusOK, code)
}

func TestOccurrenceHistory_DoesNotListACloseTwice(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	now := time.Now()
	w.addEvent(t, models.OccurrenceEventStageChange, "Aberto → Resolvido", w.logPorto.ID, now)
	w.addEvent(t, models.OccurrenceEventClosed, "", w.logPorto.ID, now.Add(300*time.Millisecond))
	w.addEvent(t, models.OccurrenceEventStageChange, "Resolvido → Em análise", w.supPorto.ID, now.Add(time.Hour))
	_, p := w.history(t, w.logPorto, w.occID, "types=stage_change,closed")
	var types []string
	for _, e := range p.Events {
		types = append(types, e.Type)
	}
	assert.ElementsMatch(t, []string{"closed", "stage_change"}, types, "the stage change that is half of the close is not shown again")
}
