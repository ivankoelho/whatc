package handlers_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The decision "the owner can no longer start a call, so the send may be settled or freed" is taken on the
// DATABASE's clock only. When the database clock cannot be read nothing is judged: no fallback to the local clock.
//
// Every test below makes the real local time say "this send is long overdue" (the rows are minutes past their
// deadline plus margin in real time) and then breaks the database clock. With the old fallback each of them would
// have reconciled the row on the local clock; now the row must stay exactly as it was.

var errClockDown = errors.New("database unreachable")

// stuckDispatched makes a dispatched send whose owner deadline and margin are long past in REAL time.
func (w *msgWorld) stuckDispatched(t *testing.T, occ uuid.UUID) models.OccurrenceSendIntent {
	t.Helper()
	d, dl := time.Now().Add(-10*time.Minute), time.Now().Add(-10*time.Minute+40*time.Second)
	row := models.OccurrenceSendIntent{ID: uuid.New(), OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
		OccurrenceID: occ, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: d, DispatchedAt: &d, DispatchDeadline: &dl}
	require.NoError(t, w.app.DB.Create(&row).Error)
	return row
}

func (w *msgWorld) stateOf(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var r models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.First(&r, "id = ?", id).Error)
	return string(r.State)
}

func TestDBClock_TheBackgroundCycleIsSkippedWhenTheDatabaseClockCannotBeRead(t *testing.T) {
	w := newMsgWorld(t)
	row := w.stuckDispatched(t, w.occ.ID)

	w.app.SetDBClockForTest(func() (time.Time, error) { return time.Time{}, errClockDown })
	defer w.app.SetDBClockForTest(nil)
	assert.False(t, w.app.RunReconcileCycleForTest(), "the cycle must say it did not run")
	assert.Equal(t, models.SendIntentProcessing, w.stateOf(t, row.ID), "the local clock says it is overdue, but nothing may be decided on it")

	// With the database clock back, the same row is reconciled.
	w.app.SetDBClockForTest(nil)
	assert.True(t, w.app.RunReconcileCycleForTest())
	assert.Equal(t, models.SendIntentUncertain, w.stateOf(t, row.ID))
}

func TestDBClock_TheStateQueryDoesNotReconcileWithoutTheDatabaseClock(t *testing.T) {
	w := newMsgWorld(t)
	row := w.stuckDispatched(t, w.occ.ID)
	w.app.SetDBClockForTest(func() (time.Time, error) { return time.Time{}, errClockDown })
	defer w.app.SetDBClockForTest(nil)

	code, data := w.getSend(t, w.agent, w.occ, fmt.Sprint(row.ID))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.SendIntentProcessing, data["state"], "reported as it is: still open")
	assert.Equal(t, models.SendIntentProcessing, w.stateOf(t, row.ID))
}

func TestDBClock_ANewSendDoesNotFreeTheCaseWithoutTheDatabaseClock(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	row := w.stuckDispatched(t, w.occ.ID) // in the way, and overdue in real time
	w.app.SetDBClockForTest(func() (time.Time, error) { return time.Time{}, errClockDown })
	defer w.app.SetDBClockForTest(nil)

	a := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "relogio fora"})
	assert.Equal(t, fasthttp.StatusConflict, a.code, "the case stays closed: the row in the way could not be judged")
	assert.Equal(t, "send_pending", a.env.ErrorType)
	assert.Equal(t, 0, w.providerTotal())
	assert.Equal(t, models.SendIntentProcessing, w.stateOf(t, row.ID))

	// The same repeat of the owner's own key is not reconciled either.
	var orig models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.First(&orig, "id = ?", row.ID).Error)

	// With the clock back, the claim reconciles the overdue row (to uncertain: no evidence) and the case stays
	// closed for a person to settle it, which is the designed behavior.
	w.app.SetDBClockForTest(nil)
	b := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "relogio de volta"})
	assert.Equal(t, fasthttp.StatusConflict, b.code)
	assert.Equal(t, models.SendIntentUncertain, w.stateOf(t, row.ID))
}

func TestDBClock_ASettlementIsRefusedWithoutTheDatabaseClock_AndChangesNothing(t *testing.T) {
	w := newMsgWorld(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	sendID := w.uncertainSend(t)
	w.app.SetDBClockForTest(func() (time.Time, error) { return time.Time{}, errClockDown })
	defer w.app.SetDBClockForTest(nil)

	a := w.resolve(t, resolver, w.occ, sendID, goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusServiceUnavailable, a.code)
	row := w.lastIntent(t)
	assert.Equal(t, models.SendIntentUncertain, string(row.State), "nothing was decided: the provider's evidence could not be re-read reliably")
	assert.Nil(t, row.ResolvedByID)

	w.app.SetDBClockForTest(nil)
	ok := w.resolve(t, resolver, w.occ, sendID, goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusOK, ok.code)
}
