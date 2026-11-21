package handlers

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-01: the agent's farewell promises a person. These tests kill the "process" (a panic at a named
// point, with no cleanup that a real kill would not also skip: the recovery works from the database
// alone) at every moment of the handoff and prove that recovery completes it exactly once.
//
// They simulate the crash inside one process; they are NOT a process-kill proof (that needs the
// multi-process harness, see the report).

const (
	durableFarewell = "Perfeito, vou chamar um vendedor para continuar com você. [[TRANSFERIR]]"
	durableCustomer = "quero porcelanato para a sala, uns 40 m²"
)

// newDurableWiring starts from an empty intents table: recovery claims across organizations, as in
// production, so rows left by other tests would be recovered too.
func newDurableWiring(t *testing.T) *wiring {
	t.Helper()
	w := newWiring(t, durableFarewell, qualHappy)
	require.NoError(t, w.app.DB.Exec("DELETE FROM ai_handoff_intents").Error)
	return w
}

// crashAt makes the handoff panic at the named point, once armed, and restores the seam afterwards.
func crashAt(t *testing.T, point string) {
	t.Helper()
	aiHandoffFault = func(p string) {
		if p == point {
			panic("simulated crash at " + p)
		}
	}
	t.Cleanup(func() { aiHandoffFault = nil })
}

// inboundSurvivingCrash delivers a message and swallows the simulated crash.
func (w *wiring) inboundSurvivingCrash(t *testing.T, from, text string) {
	t.Helper()
	func() {
		defer func() { _ = recover() }()
		w.inbound(t, from, text)
	}()
}

func (w *wiring) intents(t *testing.T, contactID uuid.UUID) []models.AIHandoffIntent {
	t.Helper()
	var in []models.AIHandoffIntent
	require.NoError(t, w.app.DB.Where("contact_id = ?", contactID).Order("created_at").Find(&in).Error)
	return in
}

func (w *wiring) aiTransfers(t *testing.T, contactID uuid.UUID) []models.AgentTransfer {
	t.Helper()
	var out []models.AgentTransfer
	for _, tr := range w.transfers(t, contactID) {
		if tr.Source == models.TransferSourceAIAgent {
			out = append(out, tr)
		}
	}
	return out
}

// afterLease is a moment when every live run's lease has certainly expired.
func afterLease() time.Time { return time.Now().Add(aiHandoffLease + time.Minute) }

func countWaitMessages(msgs []string) int {
	return countContaining(msgs, "Vou chamar um atendente")
}

// No crash: the intent is recorded first and closed by the live run itself; recovery has nothing to do.
func TestDurableHandoff_NoCrash_IntentRecordedThenClosedByTheLiveRun(t *testing.T) {
	w := newDurableWiring(t)
	const from = "5511900001001"

	w.inbound(t, from, durableCustomer)
	c := w.contactFor(t, from)

	in := w.intents(t, c.ID)
	require.Len(t, in, 1)
	assert.Equal(t, models.AIHandoffIntentDone, in[0].Status)
	assert.Equal(t, aiHandoffTransferred, in[0].Outcome)
	assert.Nil(t, in[0].RecoveredAt, "the live run finished it; nobody recovered anything")
	assert.Len(t, w.aiTransfers(t, c.ID), 1)
	assert.Zero(t, w.app.recoverAIHandoffs(afterLease()), "nothing left to recover")
	assert.Len(t, w.aiTransfers(t, c.ID), 1)
}

// Crash 1: after the promise was recorded, before it was made. The customer read nothing yet; recovery still
// hands the conversation over (and tells the customer once, after the attendance exists).
func TestDurableHandoff_CrashBeforeTheFarewell_RecoveryHandsOverAndTellsTheCustomerOnce(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "before_farewell")
	const from = "5511900001002"

	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	require.Empty(t, w.textSends(), "the farewell had not left")
	require.Empty(t, w.aiTransfers(t, c.ID))
	in := w.intents(t, c.ID)
	require.Len(t, in, 1)
	require.Equal(t, models.AIHandoffIntentPending, in[0].Status)

	aiHandoffFault = nil
	assert.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Len(t, w.aiTransfers(t, c.ID), 1)
	assert.Equal(t, 1, countWaitMessages(w.textSends()), "one waiting message, sent only after the attendance exists")
	in = w.intents(t, c.ID)
	assert.Equal(t, models.AIHandoffIntentDone, in[0].Status)
	assert.NotNil(t, in[0].RecoveredAt)
}

// Crash 2 (the P1-01 case): the farewell reached the customer; the process died before the transfer.
func TestDurableHandoff_CrashAfterTheFarewell_RecoveryCreatesTheAttendance(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_farewell")
	const from = "5511900001003"

	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	require.Len(t, w.textSends(), 1, "the customer read the farewell")
	require.Empty(t, w.aiTransfers(t, c.ID), "…and nothing recoverable existed before the fix: now the intent does")
	require.Equal(t, models.AIHandoffIntentPending, w.intents(t, c.ID)[0].Status)

	aiHandoffFault = nil
	require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Len(t, w.aiTransfers(t, c.ID), 1, "somebody now owns the conversation")
	assert.Len(t, w.opportunities(t, c.ID), 1)
	assert.Equal(t, 1, countWaitMessages(w.textSends()))
	assert.Equal(t, aiHandoffTransferred, w.intents(t, c.ID)[0].Outcome)

	// idempotent: a second recovery run changes nothing
	assert.Zero(t, w.app.recoverAIHandoffs(afterLease()))
	assert.Len(t, w.aiTransfers(t, c.ID), 1)
}

// Crash 3: after the qualification and the CRM work, before the transfer. Recovery must not pay for the
// extraction again nor open a second opportunity.
func TestDurableHandoff_CrashAfterQualification_NoSecondExtractionNoSecondOpportunity(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_qualification")
	const from = "5511900001004"

	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	callsBefore := len(w.ai.bodies)
	require.Equal(t, 2, callsBefore, "the agent reply and one extraction")
	require.Len(t, w.opportunities(t, c.ID), 1)
	require.Empty(t, w.aiTransfers(t, c.ID))

	aiHandoffFault = nil
	require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Len(t, w.ai.bodies, callsBefore, "the qualification is not paid for twice")
	assert.Len(t, w.opportunities(t, c.ID), 1, "no duplicate opportunity")
	assert.Len(t, w.aiTransfers(t, c.ID), 1)
}

// Crash 4: the attendance exists but the waiting message was never sent. Recovery sees it and adds nothing.
func TestDurableHandoff_CrashAfterTheTransfer_RecoveryFindsItAndDuplicatesNothing(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_transfer")
	const from = "5511900001005"

	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	require.Len(t, w.aiTransfers(t, c.ID), 1)
	sentBefore := len(w.textSends())

	aiHandoffFault = nil
	require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Len(t, w.aiTransfers(t, c.ID), 1, "never a second attendance")
	assert.Len(t, w.textSends(), sentBefore, "never a second message")
	in := w.intents(t, c.ID)[0]
	assert.Equal(t, models.AIHandoffIntentDone, in.Status)
	// the attendance exists, so recovery ends quietly (reported as already active or as a person owning it)
	assert.Contains(t, []string{aiHandoffAlreadyActive, aiHandoffHumanActive}, in.Outcome)
}

// A live run that is still alive (lease not expired) is never taken over.
func TestDurableHandoff_RecoveryRespectsTheLiveRunsLease(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_farewell")
	const from = "5511900001006"
	w.inboundSurvivingCrash(t, from, durableCustomer)
	aiHandoffFault = nil

	assert.Zero(t, w.app.recoverAIHandoffs(time.Now()), "the lease has not passed: the run may still be alive")
	assert.Empty(t, w.aiTransfers(t, w.contactFor(t, from).ID))
}

// Agent versus human: an attendant took the conversation while the intent was pending. Recovery ends quietly.
func TestDurableHandoff_HumanTookOverWhilePending_RecoveryDoesNotInterfere(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_farewell")
	const from = "5511900001007"
	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	aiHandoffFault = nil
	w.openAttendance(t, c.ID) // a person opens the conversation
	sentBefore := len(w.textSends())

	require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Empty(t, w.aiTransfers(t, c.ID), "the agent never competes with a person")
	assert.Len(t, w.textSends(), sentBefore, "nothing said over the attendant")
	assert.Equal(t, aiHandoffHumanActive, w.intents(t, c.ID)[0].Outcome)
}

// Concurrency: several instances recover the same intent at the same time (and a person opens the
// conversation in the middle). Exactly one active attendance, at most one waiting message.
func TestDurableHandoff_ConcurrentRecoveryAndAHuman_OneAttendanceOneMessage(t *testing.T) {
	for _, withHuman := range []bool{false, true} {
		t.Run(fmt.Sprintf("human=%v", withHuman), func(t *testing.T) {
			w := newDurableWiring(t)
			crashAt(t, "after_farewell")
			from := fmt.Sprintf("551190000200%d", map[bool]int{false: 1, true: 2}[withHuman])
			w.inboundSurvivingCrash(t, from, durableCustomer)
			c := w.contactFor(t, from)
			aiHandoffFault = nil
			sentBefore := len(w.textSends())

			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); w.app.recoverAIHandoffs(afterLease()) }()
			}
			if withHuman {
				wg.Add(1)
				go func() { defer wg.Done(); w.openAttendance(t, c.ID) }()
			}
			wg.Wait()

			var active int64
			w.app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ? AND status = ?", c.ID, models.TransferStatusActive).Count(&active)
			assert.EqualValues(t, 1, active, "one active attendance, never two")
			assert.LessOrEqual(t, countWaitMessages(w.textSends()[sentBefore:]), 1, "at most one waiting message")
			in := w.intents(t, c.ID)
			require.Len(t, in, 1)
			assert.Equal(t, models.AIHandoffIntentDone, in[0].Status)
		})
	}
}

// A handoff that keeps failing is retried a bounded number of times and then abandoned, loudly, for a person.
func TestDurableHandoff_RepeatedFailureIsBoundedAndAbandoned(t *testing.T) {
	w := newDurableWiring(t)
	crashAt(t, "after_farewell")
	const from = "5511900001008"
	w.inboundSurvivingCrash(t, from, durableCustomer)
	c := w.contactFor(t, from)
	crashAt(t, "after_qualification") // every recovery attempt dies too

	at := time.Now()
	for i := 0; i < aiHandoffMaxAttempts; i++ {
		at = at.Add(aiHandoffLease + time.Minute)
		assert.Equal(t, 1, w.app.recoverAIHandoffs(at), "attempt %d", i+1)
	}
	at = at.Add(aiHandoffLease + time.Minute)
	w.app.recoverAIHandoffs(at) // the next pass gives up instead of trying forever

	in := w.intents(t, c.ID)[0]
	assert.Equal(t, models.AIHandoffIntentAbandoned, in.Status)
	assert.Equal(t, aiHandoffMaxAttempts, in.Attempts)
	assert.Empty(t, w.aiTransfers(t, c.ID))
}

// The intent is idempotent: recording it twice for one contact keeps one pending row.
func TestDurableHandoff_RecordingTheIntentTwiceKeepsOne(t *testing.T) {
	w := newDurableWiring(t)
	contact, session := w.handoffSession(t, durableCustomer)

	a, err := w.app.recordHandoffIntent(w.f.account, contact, session, aiHandoffMarker)
	require.NoError(t, err)
	b, err := w.app.recordHandoffIntent(w.f.account, contact, session, aiHandoffMarker)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.Len(t, w.intents(t, contact.ID), 1)
}

// If the promise cannot be written, the farewell is NOT sent: the customer is never promised anything that
// is not recoverable. The handoff is made first and the customer told after.
func TestDurableHandoff_IntentCannotBeRecorded_FarewellIsNotSentHandoffIsMadeFirst(t *testing.T) {
	w := newDurableWiring(t)
	require.NoError(t, w.app.DB.Exec("ALTER TABLE ai_handoff_intents RENAME TO ai_handoff_intents_off").Error)
	t.Cleanup(func() { _ = w.app.DB.Exec("ALTER TABLE ai_handoff_intents_off RENAME TO ai_handoff_intents").Error })
	const from = "5511900001009"

	w.inbound(t, from, durableCustomer)
	c := w.contactFor(t, from)

	assert.Len(t, w.aiTransfers(t, c.ID), 1, "the attendance was created")
	sent := w.textSends()
	for _, s := range sent {
		assert.False(t, strings.Contains(s, "vou chamar um vendedor"), "the agent's own farewell must not have been sent without a recoverable intent")
	}
	assert.Equal(t, 1, countWaitMessages(sent), "the customer is told only after the attendance exists")
}

// Recovery claims across organizations (one worker serves all), but every handoff it finishes stays inside its
// own organization: its own account, its own unit team, its own contact.
func TestDurableHandoff_RecoveryKeepsEachHandoffInsideItsOrganization(t *testing.T) {
	w1 := newDurableWiring(t)
	w2 := newWiring(t, durableFarewell, qualHappy) // another organization, same database; intents of both stay
	crashAt(t, "after_farewell")
	w1.inboundSurvivingCrash(t, "5511900005001", durableCustomer)
	w2.inboundSurvivingCrash(t, "5511900005002", durableCustomer)
	aiHandoffFault = nil
	c1, c2 := w1.contactFor(t, "5511900005001"), w2.contactFor(t, "5511900005002")

	require.Equal(t, 2, w1.app.recoverAIHandoffs(afterLease()), "one worker recovers both organizations")

	for _, tc := range []struct {
		w *wiring
		c *models.Contact
	}{{w1, c1}, {w2, c2}} {
		tr := tc.w.aiTransfers(t, tc.c.ID)
		require.Len(t, tr, 1)
		assert.Equal(t, tc.w.f.org.ID, tr[0].OrganizationID, "the attendance belongs to the contact's organization")
		assert.Equal(t, tc.w.f.account.Name, tr[0].WhatsAppAccount)
		require.NotNil(t, tr[0].TeamID)
		assert.Equal(t, tc.w.f.team.ID, *tr[0].TeamID, "routed to ITS unit team, never the other organization's")
	}
}
