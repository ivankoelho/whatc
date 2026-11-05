package handlers_test

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// M1 of the review: a person must never be able to free a case while the ORIGINAL send can still reach the
// customer. These tests reproduce the sequence deterministically by stopping the owner of a send at the two
// instants that matter (just before and just after its point of no return) while the reconciliation, a person
// and a new send act, and then letting the owner go on.

// gate stops the FIRST caller that reaches it and lets everyone else through.
type gate struct {
	parked  chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate { return &gate{parked: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) hook() func() {
	return func() {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.parked)
			<-g.release
		}
	}
}

func (g *gate) waitParked(t *testing.T) {
	t.Helper()
	select {
	case <-g.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the owner never reached the instant the test stops it at")
	}
}

// ownerSend starts a send in the background and returns what it will answer.
func (w *msgWorld) ownerSend(t *testing.T, key, text string) <-chan keyedAnswer {
	t.Helper()
	out := make(chan keyedAnswer, 1)
	go func() { out <- w.sendKeyedNoRequire(t, w.agent, w.occ, key, map[string]string{"message": text}) }()
	return out
}

func answerWithin(t *testing.T, c <-chan keyedAnswer) keyedAnswer {
	t.Helper()
	select {
	case a := <-c:
		return a
	case <-time.After(15 * time.Second):
		t.Fatal("the owner never answered")
		return keyedAnswer{}
	}
}

// 1. The owner stalls BEFORE its point of no return (after the claim, before the dispatch).
//
//	reconciliation fences it -> the case is free -> another send goes out -> the owner wakes up ->
//	its dispatch finds the row fenced -> it sends NOTHING.
func TestFencing_OwnerPausedBeforeThePointOfNoReturn_NeverSendsAfterBeingFenced(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	g := newGate()
	defer handlers.SetSendHooksForTest(g.hook(), nil)()

	ownerKey := uuid.NewString()
	owner := w.ownerSend(t, ownerKey, "do dono parado")
	g.waitParked(t) // claimed, audit attempt written, message row created: parked right before the dispatch

	row := w.lastIntent(t)
	require.Equal(t, models.SendIntentProcessing, string(row.State))
	require.Nil(t, row.DispatchedAt, "it never reached the point of no return")

	// While it is parked: still inside the wait, nothing frees the case.
	w.app.ReconcileSendIntents(time.Now())
	assert.Equal(t, models.SendIntentProcessing, string(w.lastIntent(t).State))
	blocked := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "cedo demais"})
	assert.Equal(t, fasthttp.StatusConflict, blocked.code)

	// Past the wait the reconciliation FENCES it: proven not sent, not "uncertain".
	assert.Greater(t, w.app.ReconcileSendIntents(time.Now().Add(3*time.Minute)), 0)
	row = w.lastIntent(t)
	assert.Equal(t, models.SendIntentFailed, string(row.State), "an owner that never dispatched is fenced as not sent")
	assert.Nil(t, row.DispatchedAt)
	assert.Equal(t, 0, w.providerTotal())

	// The case is free: another person sends, and the customer gets that message.
	next := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "enviada por outra pessoa"})
	require.Equal(t, fasthttp.StatusOK, next.code)
	assert.Equal(t, 1, w.providerTotal())

	// The owner wakes up. Its own point of no return finds the row fenced and it sends nothing.
	close(g.release)
	got := answerWithin(t, owner)
	assert.Equal(t, 1, w.providerTotal(), "the fenced owner reached the customer: that would be a duplicate")
	assert.Equal(t, 1, w.mockHas("enviada por outra pessoa"))
	assert.Equal(t, 0, w.mockHas("do dono parado"))
	assert.Equal(t, "failed", got.env.Data["result"], "and it is told so, from the row")

	// Its message row says it was not sent either; nothing else was created.
	var failedMsgs int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND content = ? AND status = ?", w.contact.ID, "do dono parado", models.MessageStatusFailed).Count(&failedMsgs).Error)
	assert.EqualValues(t, 1, failedMsgs)
	assert.EqualValues(t, 0, w.openIntents(t, w.occ))
}

// 2. The owner stalls AFTER its point of no return and past the deadline it was given.
//
//	still "processing" (nobody may free it) -> after deadline+margin it becomes uncertain -> a person settles it as
//	"not delivered" -> a new send goes out -> the owner wakes up -> its provider call is past the deadline and is
//	never made.
func TestFencing_OwnerPausedAfterThePointOfNoReturn_CannotSendPastItsDeadline(t *testing.T) {
	w := newMsgWorld(t)
	other := w.colleague(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	defer handlers.SetSendFencingForTest(400*time.Millisecond, 200*time.Millisecond)()
	g := newGate()
	defer handlers.SetSendHooksForTest(nil, g.hook())()

	owner := w.ownerSend(t, uuid.NewString(), "do dono congelado")
	g.waitParked(t) // dispatched (deadline stored), parked right before the provider is called

	row := w.lastIntent(t)
	require.Equal(t, models.SendIntentProcessing, string(row.State))
	require.NotNil(t, row.DispatchedAt)
	require.NotNil(t, row.DispatchDeadline)

	// Inside the deadline the owner may still call the provider: nobody can free the case.
	w.app.ReconcileSendIntents(time.Now())
	assert.Equal(t, models.SendIntentProcessing, string(w.lastIntent(t).State))
	assert.Equal(t, fasthttp.StatusConflict, w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "cedo"}).code)
	early := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, early.code, "a person cannot settle a send that is not uncertain yet")
	assert.Equal(t, "send_not_uncertain", early.env.ErrorType)

	// Past the deadline plus the margin the owner can no longer START a send: now it is uncertain (no evidence).
	time.Sleep(700 * time.Millisecond)
	assert.Greater(t, w.app.ReconcileSendIntents(time.Now()), 0)
	assert.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State))

	// A person checks the conversation (nothing there), settles it, and the case is free.
	ok := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	require.Equal(t, fasthttp.StatusOK, ok.code)
	next := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "depois da decisao"})
	require.Equal(t, fasthttp.StatusOK, next.code)
	assert.Equal(t, 1, w.providerTotal())

	// The owner wakes up far past its deadline. The provider call carries that deadline: it is never made.
	close(g.release)
	_ = answerWithin(t, owner)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, w.providerTotal(), "the frozen owner reached the customer after its deadline: that would be a duplicate")
	assert.Equal(t, 0, w.mockHas("do dono congelado"))
	assert.Equal(t, 1, w.mockHas("depois da decisao"))
}

// 3. The same instant, but the provider's own confirmation arrives before the person decides: the evidence wins
// and the owner's late call is still never made.
func TestFencing_ProviderEvidenceStillBeatsAPerson_AfterAPausedOwner(t *testing.T) {
	w := newMsgWorld(t)
	resolver := w.mkUser(t, "occurrences:read", "occurrences:view_all", "occurrences:resolve_send")
	defer handlers.SetSendFencingForTest(300*time.Millisecond, 100*time.Millisecond)()
	g := newGate()
	defer handlers.SetSendHooksForTest(nil, g.hook())()

	owner := w.ownerSend(t, uuid.NewString(), "evidencia")
	g.waitParked(t)
	row := w.lastIntent(t)
	time.Sleep(500 * time.Millisecond)
	w.app.ReconcileSendIntents(time.Now())
	require.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State))

	// The provider reports the message (a status webhook gives the row its id and advances it).
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", row.MessageID).
		Updates(map[string]any{"whats_app_message_id": "wamid.FROMPROVIDER", "status": models.MessageStatusDelivered}).Error)

	a := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, a.code, "the provider's confirmation wins over a person")
	assert.Equal(t, models.SendIntentSent, string(w.lastIntent(t).State))
	assert.Nil(t, w.lastIntent(t).ResolvedByID)

	close(g.release)
	_ = answerWithin(t, owner)
	assert.Equal(t, 0, w.providerTotal(), "and the paused owner never called the provider")
}

// 4. The point of no return is atomic against the fence: for one row, with the two racing for real in the
// database, exactly one of them wins, never both and never neither.
func TestFencing_DispatchAndFence_RaceForRealInTheDatabase_ExactlyOneWins(t *testing.T) {
	w := newMsgWorld(t)
	const rounds = 120
	dispatchedWins, fencedWins := 0, 0
	for i := 0; i < rounds; i++ {
		id := uuid.New()
		require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: uuid.New(), UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
			MessageID: uuid.New(), ClaimedAt: time.Now().Add(-10 * time.Minute)}).Error)

		var wg sync.WaitGroup
		var dispatched bool
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(rand.Intn(1500)) * time.Microsecond)
			ok, err := w.app.DispatchSendIntentForTest(id)
			assert.NoError(t, err)
			dispatched = ok
		}()
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(rand.Intn(1500)) * time.Microsecond)
			w.app.ReconcileSendIntents(time.Now())
		}()
		close(start)
		wg.Wait()

		var row models.OccurrenceSendIntent
		require.NoError(t, w.app.DB.First(&row, "id = ?", id).Error)
		if dispatched {
			dispatchedWins++
			assert.Equal(t, models.SendIntentProcessing, string(row.State), "round %d: dispatched, so it cannot also have been fenced", i)
			assert.NotNil(t, row.DispatchedAt, "round %d", i)
		} else {
			fencedWins++
			assert.Equal(t, models.SendIntentFailed, string(row.State), "round %d: not dispatched, so it was fenced", i)
			assert.Nil(t, row.DispatchedAt, "round %d", i)
		}
	}
	t.Logf("dispatch won %d, fence won %d of %d real races", dispatchedWins, fencedWins, rounds)
	assert.Equal(t, rounds, dispatchedWins+fencedWins)
}

// 5. Two dispatches of the same row: only the first passes (a request cannot be sent twice from one claim).
func TestFencing_TheSameClaimCanBeDispatchedOnlyOnce(t *testing.T) {
	w := newMsgWorld(t)
	id := uuid.New()
	require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
		OccurrenceID: uuid.New(), UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: time.Now()}).Error)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := w.app.DispatchSendIntentForTest(id)
			wins <- ok
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for ok := range wins {
		if ok {
			n++
		}
	}
	assert.Equal(t, 1, n)
}

// 6. A claim that never dispatched is failed (proven), one that dispatched is uncertain (not proven): the
// difference is exactly what the fencing buys.
func TestFencing_StaleClaim_NeverDispatchedIsFailed_DispatchedIsUncertainWithoutEvidence(t *testing.T) {
	w := newMsgWorld(t)
	mk := func(dispatched bool, occ uuid.UUID) uuid.UUID {
		id := uuid.New()
		row := models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: occ, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
			MessageID: uuid.New(), ClaimedAt: time.Now().Add(-10 * time.Minute)}
		if dispatched {
			d, dl := time.Now().Add(-10*time.Minute), time.Now().Add(-10*time.Minute+40*time.Second)
			row.DispatchedAt, row.DispatchDeadline = &d, &dl
		}
		require.NoError(t, w.app.DB.Create(&row).Error)
		return id
	}
	never, did := mk(false, uuid.New()), mk(true, uuid.New())
	w.app.ReconcileSendIntents(time.Now())
	var a, b models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.First(&a, "id = ?", never).Error)
	require.NoError(t, w.app.DB.First(&b, "id = ?", did).Error)
	assert.Equal(t, models.SendIntentFailed, string(a.State))
	assert.Equal(t, models.SendIntentUncertain, string(b.State))
}
