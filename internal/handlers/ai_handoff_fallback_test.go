package handlers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forceTransferFailure makes every INSERT into agent_transfers of this organization that matches
// cond fail inside Postgres, the way a real database error would. cond is SQL over NEW (for
// example "NEW.team_id IS NOT NULL"). The returned function removes the trigger, so a test can let
// the system recover. The trigger is also removed when the test ends.
func forceTransferFailure(t *testing.T, w *wiring, cond string) (restore func()) {
	t.Helper()
	name := "zz_fail_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	create := fmt.Sprintf(`
		CREATE FUNCTION %[1]s() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'forced transfer failure (test)'; END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER %[1]s BEFORE INSERT ON agent_transfers FOR EACH ROW
		WHEN (NEW.organization_id = '%[2]s' AND (%[3]s)) EXECUTE FUNCTION %[1]s();`, name, w.f.org.ID, cond)
	require.NoError(t, w.app.DB.Exec(create).Error)
	removed := false
	restore = func() {
		if removed {
			return
		}
		removed = true
		_ = w.app.DB.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %[1]s ON agent_transfers; DROP FUNCTION IF EXISTS %[1]s();`, name)).Error
	}
	t.Cleanup(restore)
	return restore
}

// If the transfer to the unit's team fails, the conversation goes to the general queue, marked and
// with everything the agent learned, and the customer is attended and told once.
func TestHandoffFromAI_TeamTransferFails_FallsBackToTheGeneralQueueWithContext(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	forceTransferFailure(t, w, "NEW.team_id IS NOT NULL")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker),
		"the conversation is in human hands, through the general queue")

	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1, "one attendance, not zero and not two")
	tr := ts[0]
	assert.Equal(t, models.TransferStatusActive, tr.Status)
	assert.Nil(t, tr.TeamID, "the general queue: the team insert was the one that failed")
	require.NotNil(t, tr.RoutingReason)
	assert.Equal(t, models.RoutingReasonAINoUnitTeam, *tr.RoutingReason, "marked, so it can be found and fixed")
	assert.Equal(t, models.TransferSourceAIAgent, tr.Source)
	assert.Contains(t, tr.Notes, "Informado pelo cliente", "the context travels with the attendance")
	assert.Contains(t, tr.Notes, "Produto: porcelanato")

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1, "the opportunity was registered before the transfer and is not lost")
	require.NotNil(t, opps[0].SourceTransferID)
	assert.Equal(t, tr.ID, *opps[0].SourceTransferID, "and it points to the attendance that was created")

	sent := w.wa.all()
	require.Len(t, sent, 1, "the customer is told once, and not about the failure")
	assert.Contains(t, sent[0], "atendente")
	assert.NotContains(t, sent[0], "problema")
}

// If not even the general queue works, the customer is told to try again and nothing is half done;
// when the database recovers, the next attempt completes without a second qualification.
func TestHandoffFromAI_BothTransfersFail_CustomerIsToldAndTheNextAttemptRecovers(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	restore := forceTransferFailure(t, w, "true")

	assert.False(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Empty(t, w.transfers(t, contact.ID), "no attendance could be created")
	sent := w.wa.all()
	require.Len(t, sent, 1, "the customer is not left in silence")
	assert.Contains(t, sent[0], "problema")
	assert.NotContains(t, sent[0], "Vou chamar um atendente", "and is not promised something that did not happen")
	assert.Len(t, w.opportunities(t, contact.ID), 1, "the opportunity survives")
	callsBefore := len(w.ai.bodies)
	require.Equal(t, 1, callsBefore, "the qualification ran once")

	restore() // the database recovers; the customer writes again and the agent asks for a human again
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Len(t, w.transfers(t, contact.ID), 1)
	assert.Equal(t, callsBefore, len(w.ai.bodies), "no second extraction: it was kept for this conversation")
	assert.Len(t, w.opportunities(t, contact.ID), 1, "still a single open opportunity")
	assert.Contains(t, w.wa.all()[len(w.wa.all())-1], "atendente")
}

// A human who took over while the handoff was failing is never talked over.
func TestHandoffFromAI_BothTransfersFail_NoErrorMessageOverAHuman(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "quero porcelanato para a sala, uns 40 m²")
	forceTransferFailure(t, w, "NEW.source = 'ai_agent'")
	w.ai.onCall = func(int) { w.openAttendance(t, contact.ID) } // an attendant opens it during the extraction

	assert.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Zero(t, w.wa.count(), "nothing said over the attendant")
}

// Through the processor: the agent asks for a human with the marker alone and the whole database
// refuses the attendance. The customer must not be left with silence.
func TestProcessor_MarkerWhenEverythingFails_CustomerIsNotLeftInSilence(t *testing.T) {
	w := newWiring(t, "[[TRANSFERIR]]", qualHappy)
	forceTransferFailure(t, w, "true")
	const from = "5511900000020"

	w.inbound(t, from, "quero porcelanato para a sala, uns 40 m²")

	sent := w.wa.all()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "problema")
	for _, b := range sent {
		assert.NotContains(t, b, "[[", "no marker, even on the failure path")
	}
	assert.Empty(t, w.transfers(t, w.contactFor(t, from).ID))
}
