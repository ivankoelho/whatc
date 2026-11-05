package handlers_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	wausage "github.com/shridarpatil/whatomate/internal/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// A person must never be able to free a case while the ORIGINAL send can still START reaching the customer. These
// tests reproduce the sequence deterministically by stopping the owner of a send at the two instants that matter
// (just before and just after its point of no return) while the reconciliation, a person and a new send act, and
// then letting the owner go on. They are written to hold on a slow or loaded machine: every judgement about "inside
// the window" uses an explicit, simulated clock, and real time is spent only where the owner's own deadline must
// really pass.

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
	case <-time.After(15 * time.Second):
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
	case <-time.After(20 * time.Second):
		t.Fatal("the owner never answered")
		return keyedAnswer{}
	}
}

// waitUntilPast blocks until the wall clock is past t (plus a small cushion): the owner's own deadline has REALLY passed.
func waitUntilPast(t time.Time) {
	for time.Now().Before(t.Add(150 * time.Millisecond)) {
		time.Sleep(20 * time.Millisecond)
	}
}

// 1. The owner stalls BEFORE its point of no return (after the claim, before the dispatch).
//
//	reconciliation fences it -> the case is free -> another send goes out -> the owner wakes up ->
//	its dispatch finds the row fenced -> it sends NOTHING, and everything it left behind says "not sent".
func TestFencing_OwnerPausedBeforeThePointOfNoReturn_NeverSendsAfterBeingFenced(t *testing.T) {
	w := newMsgWorld(t)
	w.app.Usage = wausage.New(w.app.DB, config.UsageConfig{}) // consumption recording on, so a ledger entry is opened per send
	other := w.colleague(t)
	g := newGate()
	defer handlers.SetSendHooksForTest(g.hook(), nil)()

	owner := w.ownerSend(t, uuid.NewString(), "do dono parado")
	g.waitParked(t) // claimed, audit attempt written, message row created: parked right before the dispatch

	row := w.lastIntent(t)
	require.Equal(t, models.SendIntentProcessing, string(row.State))
	require.Nil(t, row.DispatchedAt, "it never reached the point of no return")

	// Inside the wait nothing frees the case (simulated clock: one minute after the claim).
	w.app.ReconcileSendIntents(row.ClaimedAt.Add(time.Minute))
	assert.Equal(t, models.SendIntentProcessing, string(w.lastIntent(t).State))
	blocked := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "cedo demais"})
	assert.Equal(t, fasthttp.StatusConflict, blocked.code)

	// Past the wait the reconciliation FENCES it: proven not sent, not "uncertain".
	assert.Greater(t, w.app.ReconcileSendIntents(row.ClaimedAt.Add(3*time.Minute)), 0)
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

	// What the owner left behind says "not sent": the message row is failed, and the usage ledger entry opened for
	// it is closed as a failed send instead of staying pending forever.
	var msg models.Message
	require.NoError(t, w.app.DB.Where("contact_id = ? AND content = ?", w.contact.ID, "do dono parado").First(&msg).Error)
	assert.Equal(t, models.MessageStatusFailed, msg.Status)
	assert.Empty(t, msg.WhatsAppMessageID)
	var usage []models.MessageUsage
	require.NoError(t, w.app.DB.Where("message_id = ?", msg.ID).Find(&usage).Error)
	require.NotEmpty(t, usage, "the send opened a usage ledger entry (so the check below is not vacuous)")
	for _, u := range usage {
		assert.Equal(t, "send_failed", u.BillingState, "the usage entry of a send that never left must not stay pending")
		if u.Billable != nil {
			assert.False(t, *u.Billable)
		}
	}
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
	const window, margin = 3 * time.Second, 3 * time.Second
	defer handlers.SetSendFencingForTest(window, margin)()
	g := newGate()
	defer handlers.SetSendHooksForTest(nil, g.hook())()

	owner := w.ownerSend(t, uuid.NewString(), "do dono congelado")
	g.waitParked(t) // dispatched (deadline stored), parked right before the provider is called

	row := w.lastIntent(t)
	require.Equal(t, models.SendIntentProcessing, string(row.State))
	require.NotNil(t, row.DispatchedAt)
	require.NotNil(t, row.DispatchDeadline)
	deadline := *row.DispatchDeadline

	// Inside the deadline (simulated clock) the owner may still call the provider: nobody can free the case.
	w.app.ReconcileSendIntents(deadline.Add(-time.Second))
	assert.Equal(t, models.SendIntentProcessing, string(w.lastIntent(t).State))
	early := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, early.code, "a person cannot settle a send that is not uncertain yet")
	assert.Equal(t, "send_not_uncertain", early.env.ErrorType)
	assert.Equal(t, fasthttp.StatusConflict, w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "cedo"}).code)

	// Past the deadline plus the margin (simulated) the owner can no longer START a send: now it is uncertain.
	assert.Greater(t, w.app.ReconcileSendIntents(deadline.Add(margin+time.Second)), 0)
	require.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State))

	// A person checks the conversation (nothing there), settles it, and the case is free.
	ok := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	require.Equal(t, fasthttp.StatusOK, ok.code)
	next := w.sendKeyed(t, other, w.occ, uuid.NewString(), map[string]string{"message": "depois da decisao"})
	require.Equal(t, fasthttp.StatusOK, next.code)
	assert.Equal(t, 1, w.providerTotal())

	// The owner wakes up with its deadline REALLY past. The provider call carries that deadline: it is never made.
	waitUntilPast(deadline)
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
	const window, margin = 2 * time.Second, 2 * time.Second
	defer handlers.SetSendFencingForTest(window, margin)()
	g := newGate()
	defer handlers.SetSendHooksForTest(nil, g.hook())()

	owner := w.ownerSend(t, uuid.NewString(), "evidencia")
	g.waitParked(t)
	row := w.lastIntent(t)
	require.NotNil(t, row.DispatchDeadline)
	w.app.ReconcileSendIntents(row.DispatchDeadline.Add(margin + time.Second))
	require.Equal(t, models.SendIntentUncertain, string(w.lastIntent(t).State))

	// The provider reports the message (a status webhook gives the row its id and advances it).
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("id = ?", row.MessageID).
		Updates(map[string]any{"whats_app_message_id": "wamid.FROMPROVIDER", "status": models.MessageStatusDelivered}).Error)

	a := w.resolve(t, resolver, w.occ, fmt.Sprint(row.ID), goodDecision("not_delivered"))
	assert.Equal(t, fasthttp.StatusConflict, a.code, "the provider's confirmation wins over a person")
	assert.Equal(t, models.SendIntentSent, string(w.lastIntent(t).State))
	assert.Nil(t, w.lastIntent(t).ResolvedByID)

	waitUntilPast(*row.DispatchDeadline)
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
			time.Sleep(time.Duration(rand.Intn(8000)) * time.Microsecond)
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

// 7. The fence races a STALE copy of the row. A reconciler may hold a copy loaded before the owner dispatched (it
// says "never dispatched"); acting on it must not fence an owner that has since dispatched. This is what the
// `dispatched_at IS NULL` condition of the fence UPDATE is for: without it the fence would turn a live, dispatched
// send into "failed" (proven not sent) while its request is about to leave.
func TestFencing_AFenceActingOnAStaleCopy_CannotCatchAnOwnerThatHasSinceDispatched(t *testing.T) {
	w := newMsgWorld(t)
	id := uuid.New()
	require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
		OccurrenceID: w.occ.ID, UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: time.Now().Add(-10 * time.Minute)}).Error)

	var stale models.OccurrenceSendIntent // loaded BEFORE the dispatch: "never dispatched, ten minutes old"
	require.NoError(t, w.app.DB.First(&stale, "id = ?", id).Error)
	require.Nil(t, stale.DispatchedAt)

	ok, err := w.app.DispatchSendIntentForTest(id) // the owner reaches its point of no return
	require.NoError(t, err)
	require.True(t, ok)

	changed := w.app.ReconcileCopyForTest(&stale, time.Now()) // the reconciler acts on its stale copy
	assert.False(t, changed, "the fence must lose against an owner that already dispatched")

	var row models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.First(&row, "id = ?", id).Error)
	assert.Equal(t, models.SendIntentProcessing, string(row.State), "the live send was not turned into failed")
	assert.NotNil(t, row.DispatchedAt)
	assert.Equal(t, models.SendIntentProcessing, string(stale.State), "and the copy was refreshed from the row")
	assert.NotNil(t, stale.DispatchedAt)
}

// 8. Between the deadline and deadline + margin the owner may still be inside its provider call (or the provider may
// still be queueing it): the reconciliation does NOT anticipate "uncertain". Judged on a simulated clock around the
// real deadline stored by the real dispatch, with the real default window (40 s) and margin (30 s).
func TestFencing_BetweenTheDeadlineAndTheDeadlinePlusTheMargin_NothingIsAnticipated(t *testing.T) {
	w := newMsgWorld(t)
	id := uuid.New()
	require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
		OccurrenceID: uuid.New(), UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: time.Now()}).Error)
	ok, err := w.app.DispatchSendIntentForTest(id)
	require.NoError(t, err)
	require.True(t, ok)
	var row models.OccurrenceSendIntent
	require.NoError(t, w.app.DB.First(&row, "id = ?", id).Error)
	require.NotNil(t, row.DispatchedAt)
	require.NotNil(t, row.DispatchDeadline)
	assert.InDelta(t, 40, row.DispatchDeadline.Sub(*row.DispatchedAt).Seconds(), 0.5, "the real window is 40 s")
	deadline := *row.DispatchDeadline

	state := func() string {
		var r models.OccurrenceSendIntent
		require.NoError(t, w.app.DB.First(&r, "id = ?", id).Error)
		return string(r.State)
	}
	for name, now := range map[string]time.Time{
		"right after the dispatch":         deadline.Add(-39 * time.Second),
		"one second before the deadline":   deadline.Add(-time.Second),
		"exactly at the deadline":          deadline,
		"one second after the deadline":    deadline.Add(time.Second),
		"halfway through the margin":       deadline.Add(15 * time.Second),
		"one second before the margin end": deadline.Add(29 * time.Second),
	} {
		w.app.ReconcileSendIntents(now)
		assert.Equal(t, models.SendIntentProcessing, state(), "%s: the owner may still be calling the provider", name)
		// The single-row path (the state query, the claim, the resolve) judges the same way, on its own.
		var cp models.OccurrenceSendIntent
		require.NoError(t, w.app.DB.First(&cp, "id = ?", id).Error)
		assert.False(t, w.app.ReconcileCopyForTest(&cp, now), "%s: the single-row reconciliation must not anticipate it either", name)
		assert.Equal(t, models.SendIntentProcessing, state(), name)
	}
	assert.Greater(t, w.app.ReconcileSendIntents(deadline.Add(31*time.Second)), 0)
	assert.Equal(t, models.SendIntentUncertain, state(), "only after the deadline plus the margin")
}

// 9. The check made immediately before the provider request is handed to the HTTP client: in the DATABASE, with the
// database's clock, and on the process's own monotonic timer.
func TestFencing_Preflight_ChecksTheRowInTheDatabaseRightBeforeTheWrite(t *testing.T) {
	w := newMsgWorld(t)
	id := uuid.New()
	require.NoError(t, w.app.DB.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: w.org.ID, IdempotencyKey: uuid.NewString(),
		OccurrenceID: uuid.New(), UserID: w.agent.ID, Kind: models.SendIntentKindProtocol, RequestHash: "h", State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: time.Now()}).Error)

	// never dispatched: the owner has no right to call the provider yet
	assert.Error(t, w.app.PreflightForTest(id, time.Now()))

	ok, err := w.app.DispatchSendIntentForTest(id)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NoError(t, w.app.PreflightForTest(id, time.Now()), "dispatched, inside the deadline: allowed")

	assert.Error(t, w.app.PreflightForTest(id, time.Now().Add(-41*time.Second)), "the process's own monotonic timer is past the window")

	require.NoError(t, w.app.DB.Exec("UPDATE occurrence_send_intents SET dispatch_deadline = NOW() - interval '1 second' WHERE id = ?", id).Error)
	assert.Error(t, w.app.PreflightForTest(id, time.Now()), "past the deadline on the DATABASE's clock")

	require.NoError(t, w.app.DB.Exec("UPDATE occurrence_send_intents SET dispatch_deadline = NOW() + interval '30 seconds' WHERE id = ?", id).Error)
	assert.NoError(t, w.app.PreflightForTest(id, time.Now()))
	require.NoError(t, w.app.DB.Exec("UPDATE occurrence_send_intents SET state = 'uncertain' WHERE id = ?", id).Error)
	assert.Error(t, w.app.PreflightForTest(id, time.Now()), "the row is not processing any more")
}

// 10. The same check inside a real send: the owner is parked after its dispatch, the DATABASE's deadline is moved to
// the past (while the owner's own timers have not expired), the owner is released: the provider request is not made,
// the send ends as proven-not-sent, and nothing reached the customer.
func TestFencing_Preflight_StopsTheRequestWhenTheDatabaseSaysTheDeadlineIsPast(t *testing.T) {
	w := newMsgWorld(t)
	g := newGate()
	defer handlers.SetSendHooksForTest(nil, g.hook())()

	owner := w.ownerSend(t, uuid.NewString(), "barrada na ultima checagem")
	g.waitParked(t)
	row := w.lastIntent(t)
	require.Equal(t, models.SendIntentProcessing, string(row.State))
	require.NoError(t, w.app.DB.Exec("UPDATE occurrence_send_intents SET dispatch_deadline = NOW() - interval '1 second' WHERE id = ?", row.ID).Error)

	close(g.release)
	got := answerWithin(t, owner)
	assert.Equal(t, 0, w.providerTotal(), "the request was never made")
	assert.Equal(t, 0, w.mockHas("barrada na ultima checagem"))
	assert.Equal(t, "failed", got.env.Data["result"], "the preflight refusal is a proven 'not sent'")
	assert.Equal(t, models.SendIntentFailed, string(w.lastIntent(t).State))
	assert.EqualValues(t, 0, w.openIntents(t, w.occ))
}

// 11. A send with a point of no return cannot be asynchronous: the async branch would call the provider in a goroutine
// with its own context and drop the deadline. It is refused explicitly, before anything is created.
func TestFencing_BeforeSend_RefusesAnAsynchronousSend(t *testing.T) {
	w := newMsgWorld(t)
	called := false
	opts := handlers.DefaultSendOptions()
	opts.Async = true
	opts.BeforeSend = func(ctx context.Context) (context.Context, context.CancelFunc, error) {
		called = true
		return ctx, func() {}, nil
	}
	_, err := w.app.SendOutgoingMessage(context.Background(), handlers.OutgoingMessageRequest{
		MessageID: uuid.New(), Account: w.account, Contact: w.contact, Type: models.MessageTypeText, Content: "assincrona",
	}, opts)
	require.ErrorIs(t, err, handlers.ErrBeforeSendNeedsSync)
	assert.False(t, called, "the point of no return was never even reached")
	assert.Equal(t, 0, w.providerTotal())
	assert.EqualValues(t, 0, w.outgoingMessages(t), "and no message row was created")
}
