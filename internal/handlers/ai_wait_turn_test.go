package handlers

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the model is told in a turn is rebuilt from the messages table (turnCustomerText), not from the
// pending row's list of ids and not from the session history. These tests pin that source of truth.

const (
	msgA = "A: quero porcelanato"
	msgB = "B: para a sala"
	msgC = "C: uns 30 m2"
	msgD = "D: e o prazo?"
	msgE = "E: tem em 60x60?"
)

// userText is the user message of the nth model call: the turn's customer text.
func (w *wiring) userText(n int) string {
	msgs := w.ai.bodies[n]["messages"].([]any)
	return msgs[len(msgs)-1].(map[string]any)["content"].(string)
}

func (w *wiring) setHistory(t *testing.T, on bool) {
	t.Helper()
	w.f.settings.AI.IncludeHistory = on
	require.NoError(t, w.app.DB.Save(w.f.settings).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
}

// refuseMerge makes the database refuse to close a stale row as "merged": the merge fails.
func refuseMerge(t *testing.T, w *wiring) {
	t.Helper()
	require.NoError(t, w.app.DB.Exec(`CREATE OR REPLACE FUNCTION zz_refuse_merge() RETURNS trigger AS $$
		BEGIN IF NEW.outcome = 'merged' THEN RAISE EXCEPTION 'merge refused (test)'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, w.app.DB.Exec(`DROP TRIGGER IF EXISTS zz_refuse_merge ON ai_pending_replies`).Error)
	require.NoError(t, w.app.DB.Exec(`CREATE TRIGGER zz_refuse_merge BEFORE UPDATE ON ai_pending_replies
		FOR EACH ROW EXECUTE FUNCTION zz_refuse_merge()`).Error)
	t.Cleanup(func() {
		w.app.DB.Exec("DROP TRIGGER IF EXISTS zz_refuse_merge ON ai_pending_replies")
		w.app.DB.Exec("DROP FUNCTION IF EXISTS zz_refuse_merge()")
	})
}

// A and B in a row whose worker died, C in the newer row, and the merge fails: the model still gets A, B, C.
func failedMergeScenario(t *testing.T, w *wiring) {
	t.Helper()
	const from = "5511988880001"
	w.inbound(t, from, msgA)
	w.inbound(t, from, msgB)
	_, err := w.app.claimDueAIReplies(later(), 5) // the worker dies holding A and B
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgC)
	refuseMerge(t, w)
	w.app.reclaimStaleAIReplies(time.Now().Add(farFuture))

	rows := w.pendingRows(t)
	require.Len(t, rows, 2)
	require.Equal(t, aiOutcomeReclaimFailed, rows[0].Outcome, "the merge failed and the old row is closed")
	require.Len(t, rows[1].MessageIDs, 1, "the new row knows only C")
}

// --- the failed merge no longer loses A and B -------------------------------------------------

func TestAITurn_FailedMergeStillGivesTheModelAllThreeMessages_HistoryOff(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, false)
	failedMergeScenario(t, w)

	require.Equal(t, 1, w.app.drainAIWait(time.Now().Add(farFuture)))
	require.Len(t, w.ai.bodies, 1)
	assert.Equal(t, msgA+"\n"+msgB+"\n"+msgC, w.userText(0), "in the order they were said, from the messages table")
	assert.Len(t, w.textSends(), 1)
}

func TestAITurn_FailedMergeDoesNotDuplicateWhenHistoryIsOn(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, true)
	failedMergeScenario(t, w)

	require.Equal(t, 1, w.app.drainAIWait(time.Now().Add(farFuture)))
	prompt := w.aiPrompt(0)
	for _, m := range []string{msgA, msgB, msgC} {
		assert.Equal(t, 1, strings.Count(prompt, m), "%q appears once in the whole prompt (history cut before the turn)", m)
	}
	assert.Equal(t, msgA+"\n"+msgB+"\n"+msgC, w.userText(0))
}

// The recovery survives a restart: a new process still finds A, B and C.
func TestAITurn_RecoveryAfterARestartStillHasEverything(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, false)
	failedMergeScenario(t, w)

	restarted := &App{Config: w.app.Config, DB: w.app.DB, Log: w.app.Log, WhatsApp: w.app.WhatsApp, HTTPClient: w.app.HTTPClient, Redis: w.app.Redis}
	require.Equal(t, 1, restarted.drainAIWait(time.Now().Add(farFuture)))
	require.Len(t, w.ai.bodies, 1)
	assert.Equal(t, msgA+"\n"+msgB+"\n"+msgC, w.userText(0))
	assert.Equal(t, aiOutcomeReclaimFailed, w.pendingRows(t)[0].Outcome, "the old row stays terminal; its messages did not depend on it")
}

// --- ordinary grouping with the history off ---------------------------------------------------

func TestAITurn_HistoryOffGroupsAllMessagesAndOnlyThisContacts(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, false)
	other := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID,
		WhatsAppAccount: w.f.account.Name, ContactID: other.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText,
		Content: "mensagem de OUTRO cliente", Status: models.MessageStatusReceived, WhatsAppMessageID: "wamid.outro"}).Error)

	const from = "5511988880001"
	w.inbound(t, from, msgA)
	w.inbound(t, from, msgB)
	w.inbound(t, from, msgC)
	require.Equal(t, 1, w.app.drainAIWait(later()))

	assert.Equal(t, msgA+"\n"+msgB+"\n"+msgC, w.userText(0))
	assert.NotContains(t, w.aiPrompt(0), "OUTRO cliente")
}

// --- messages that arrive while the answer is generated stay in the next turn ------------------

func TestAITurn_MessagesDuringGenerationStayInTheNextTurn(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	const from = "5511988880001"
	w.inbound(t, from, msgA)
	w.ai.onCall = func(i int) {
		if i == 0 {
			time.Sleep(10 * time.Millisecond)
			w.inbound(t, from, msgD) // arrive after the worker claimed the turn
			w.inbound(t, from, msgE)
		}
	}
	require.Equal(t, 1, w.app.drainAIWait(w.pendingRows(t)[0].DueAt), "only the first turn is due")
	require.Len(t, w.ai.bodies, 1)
	assert.Equal(t, msgA, w.userText(0), "D and E were not folded into the answer being generated")
	assert.NotContains(t, w.aiPrompt(0), "D: e o prazo")

	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 2)
	assert.Equal(t, msgD+"\n"+msgE, w.userText(1), "the next turn is D and E, and A (already answered) is not repeated")
	assert.Len(t, w.textSends(), 2)
}

// A message saved but not yet queued when the turn is claimed cannot be answered twice.
func TestAITurn_AMessageSavedBeforeBeingQueuedIsNotAnsweredTwice(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	const from = "5511988880001"
	w.inbound(t, from, msgA)
	contact := w.contactFor(t, from)
	// m1 is stored first but its queuing is late; m2 is stored and queued; the turn (m2) is answered with both
	m1 := models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, WhatsAppAccount: w.f.account.Name,
		ContactID: contact.ID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "m1 salva antes",
		Status: models.MessageStatusReceived, WhatsAppMessageID: "wamid.m1"}
	require.NoError(t, w.app.DB.Create(&m1).Error)
	time.Sleep(5 * time.Millisecond)
	w.inbound(t, from, "m2 enfileirada")
	require.Equal(t, 1, w.app.drainAIWait(later()))
	assert.Contains(t, w.userText(0), "m1 salva antes", "everything said up to the turn's last message is in it")

	// now m1's late queuing happens
	session := &models.ChatbotSession{}
	require.NoError(t, w.app.DB.Where("contact_id = ?", contact.ID).First(session).Error)
	require.NoError(t, w.app.enqueueAIWait(w.f.agent, w.f.account, contact, session, "wamid.m1", m1.CreatedAt, false))
	n := w.app.drainAIWait(later())
	assert.Equal(t, 1, n)
	assert.Len(t, w.ai.bodies, 1, "it was already answered: no second model call")
	assert.Equal(t, aiOutcomeNoText, w.pendingRows(t)[len(w.pendingRows(t))-1].Outcome)
}

// --- earlier turns are not repeated -----------------------------------------------------------

func TestAITurn_AnAnsweredGroupIsNotRepeatedInTheNextOne(t *testing.T) {
	for _, history := range []bool{false, true} {
		w := newWaitWiring(t, "resposta 1", "resposta 2")
		w.setHistory(t, history)
		const from = "5511988880001"
		w.inbound(t, from, msgA)
		w.inbound(t, from, msgB)
		require.Equal(t, 1, w.app.drainAIWait(later()))
		time.Sleep(10 * time.Millisecond)
		w.inbound(t, from, msgC)
		require.Equal(t, 1, w.app.drainAIWait(later()))

		require.Len(t, w.ai.bodies, 2)
		assert.Equal(t, msgA+"\n"+msgB, w.userText(0))
		assert.Equal(t, msgC, w.userText(1), "history=%v: a message sent between the two groups keeps the first group out", history)
	}
}

// A person's message is outgoing: it never becomes something the customer said, and a customer
// message answered by that person is not asked again.
func TestAITurn_PeopleMessagesAreNotCustomerMessages(t *testing.T) {
	w := newWaitWiring(t, "resposta")
	w.setHistory(t, true)
	const from = "5511988880001"
	w.inbound(t, from, "X: pergunta antiga")
	contact := w.contactFor(t, from)
	_, err := w.app.claimDueAIReplies(later(), 5) // close the first row without the agent answering it:
	require.NoError(t, err)
	require.NoError(t, w.app.DB.Model(&models.AIPendingReply{}).Where("organization_id = ?", w.f.org.ID).
		Updates(map[string]any{"status": models.AIPendingStatusDone, "outcome": aiOutcomeHumanActive}).Error)

	user := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID,
		WhatsAppAccount: w.f.account.Name, ContactID: contact.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText,
		Content: "H: resposta do atendente", SentByUserID: &user.ID, Status: models.MessageStatusSent, WhatsAppMessageID: "wamid.h1"}).Error)
	time.Sleep(10 * time.Millisecond)

	w.inbound(t, from, msgA)
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID,
		WhatsAppAccount: w.f.account.Name, ContactID: contact.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText,
		Content: "H2: outra do atendente no meio", SentByUserID: &user.ID, Status: models.MessageStatusSent, WhatsAppMessageID: "wamid.h2"}).Error)
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgB)

	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 1)
	assert.Equal(t, msgA+"\n"+msgB, w.userText(0), "only the customer's new messages: not X (answered by the person), not H or H2")
}

// --- one answer per turn, whoever polls ---------------------------------------------------------

func TestAITurn_SeveralWorkersAnswerEachTurnOnce(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	const from = "5511988880001"
	poll := func() int32 {
		var wg sync.WaitGroup
		var total atomic.Int32
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); total.Add(int32(w.app.drainAIWait(later()))) }()
		}
		wg.Wait()
		return total.Load()
	}
	w.inbound(t, from, msgA)
	w.inbound(t, from, msgB)
	assert.EqualValues(t, 1, poll())
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgC)
	assert.EqualValues(t, 1, poll())

	assert.Len(t, w.ai.bodies, 2, "one model call per turn")
	assert.Len(t, w.textSends(), 2, "one answer per turn")
	assert.Equal(t, msgA+"\n"+msgB, w.userText(0))
	assert.Equal(t, msgC, w.userText(1))
}

// The window the upper bound exists for: a message that arrives after the worker claimed the turn but
// before the text is built belongs to the next turn.
func TestAITurn_AMessageThatArrivesAfterTheClaimStaysInTheNextTurn(t *testing.T) {
	w := newWaitWiring(t, "resposta 1", "resposta 2")
	w.setHistory(t, false)
	const from = "5511988880001"
	w.inbound(t, from, msgA)
	rows, err := w.app.claimDueAIReplies(later(), 5) // the turn is claimed...
	require.NoError(t, err)
	require.Len(t, rows, 1)
	time.Sleep(10 * time.Millisecond)
	w.inbound(t, from, msgD) // ...then the customer writes again, before the worker reads the messages

	w.app.processAIPendingReply(&rows[0])
	require.Len(t, w.ai.bodies, 1)
	assert.Equal(t, msgA, w.userText(0), "D is not folded silently into the turn already claimed")

	require.Equal(t, 1, w.app.drainAIWait(later()))
	require.Len(t, w.ai.bodies, 2)
	assert.Equal(t, msgD, w.userText(1))
}
