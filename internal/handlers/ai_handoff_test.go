package handlers

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qualJSON is what a well-behaved model answers to the extraction prompt.
func qualJSON(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

var qualHappy = qualJSON(map[string]any{
	"produto":  map[string]any{"valor": "porcelanato", "evidencia": "quero porcelanato"},
	"ambiente": map[string]any{"valor": "sala", "evidencia": "para a sala"},
	"metragem": map[string]any{"valor": 40, "evidencia": "40 m²"},
	"intencao": map[string]any{"valor": "comprar", "evidencia": "quero porcelanato"},
	"resumo":   "quer porcelanato para a sala",
})

// handoffSession creates a contact with a chatbot session whose customer said these messages.
func (w *wiring) handoffSession(t *testing.T, customer ...string) (*models.Contact, *models.ChatbotSession) {
	t.Helper()
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		SessionData: models.JSONB{}, StartedAt: time.Now(), LastActivityAt: time.Now()}
	require.NoError(t, w.app.DB.Create(session).Error)
	for _, m := range customer {
		w.app.logSessionMessage(session.ID, models.DirectionIncoming, m, "x")
	}
	return contact, session
}

func (w *wiring) opportunities(t *testing.T, contactID uuid.UUID) []models.SalesOpportunity {
	t.Helper()
	var opps []models.SalesOpportunity
	require.NoError(t, w.app.DB.Where("organization_id = ? AND contact_id = ?", w.f.org.ID, contactID).Find(&opps).Error)
	return opps
}

func (w *wiring) transfers(t *testing.T, contactID uuid.UUID) []models.AgentTransfer {
	t.Helper()
	var ts []models.AgentTransfer
	require.NoError(t, w.app.DB.Where("organization_id = ? AND contact_id = ?", w.f.org.ID, contactID).Find(&ts).Error)
	return ts
}

func countContaining(msgs []string, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func TestHandoffFromAI_CreatesOpportunityThenTeamTransfer(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "é para a sala, uns 40 m²")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1)
	o := opps[0]
	assert.Contains(t, o.Interest, "Informado pelo cliente")
	assert.Contains(t, o.Interest, "Produto: porcelanato")
	assert.Contains(t, o.Interest, "Metragem: 40 m²")
	assert.Contains(t, o.Interest, "Ainda não coletado")
	require.NotNil(t, o.EstimatedQuantity)
	assert.Equal(t, 40.0, *o.EstimatedQuantity)
	assert.Equal(t, models.SalesUnitMetro2, *o.UnitOfMeasure)

	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	tr := ts[0]
	assert.Equal(t, models.TransferStatusActive, tr.Status)
	assert.Equal(t, models.TransferSourceAIAgent, tr.Source)
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, w.f.team.ID, *tr.TeamID, "the team of the number's unit")
	require.NotNil(t, tr.RoutingReason)
	assert.Equal(t, models.RoutingReasonAIUnitTeam, *tr.RoutingReason)
	assert.Contains(t, tr.Notes, "Informado pelo cliente", "the human sees what the agent learned")
	require.NotNil(t, o.SourceTransferID, "the opportunity points to the attendance")
	assert.Equal(t, tr.ID, *o.SourceTransferID)

	assert.Equal(t, 1, countContaining(w.wa.all(), aiHandoffWaitMessage[:20]), "exactly one waiting message")
}

// The first reply of the model is the extraction; it must have been given the transcript and told
// to quote the customer.
func TestHandoffFromAI_ExtractionPromptCarriesTheConversation(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato")
	w.app.logSessionMessage(session.ID, models.DirectionOutgoing, "Qual ambiente?", "x")
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	require.Len(t, w.ai.bodies, 1)
	assert.Contains(t, w.ai.systemPrompt(0), "LITERAL", "the model is told to quote the customer")
	transcript := w.ai.lastUserMessage(0)
	assert.Contains(t, transcript, "CLIENTE: Oi, quero porcelanato")
	assert.Contains(t, transcript, "ATENDENTE: Qual ambiente?")
}

func TestHandoffFromAI_ExtractionFailureStillTransfers(t *testing.T) {
	w := newWiring(t, "isto não é JSON")
	contact, session := w.handoffSession(t, "quero porcelanato")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1, "the agent said the customer was ready: a bare opportunity beats none")
	assert.Contains(t, opps[0].Interest, "Qualificação não extraída")
	assert.Nil(t, opps[0].EstimatedQuantity)
	assert.Len(t, w.transfers(t, contact.ID), 1)
	assert.Equal(t, 1, countContaining(w.wa.all(), "atendente"))
}

func TestHandoffFromAI_NoPurchaseIntent_NoOpportunity(t *testing.T) {
	// a customer who only wants to talk to a person: no intent is provable
	w := newWiring(t, qualJSON(map[string]any{"resumo": "quer falar com alguém"}))
	contact, session := w.handoffSession(t, "quero falar com um atendente")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Empty(t, w.opportunities(t, contact.ID))
	assert.Len(t, w.transfers(t, contact.ID), 1, "but the conversation still goes to a human")
}

func TestHandoffFromAI_CapSkipsExtraction(t *testing.T) {
	w := newWiring(t, "nunca deve ser chamado")
	contact, session := w.handoffSession(t, "quero porcelanato")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffCap))

	assert.Empty(t, w.ai.bodies, "no extra AI call once the cap is reached")
	assert.Empty(t, w.opportunities(t, contact.ID))
	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	assert.Contains(t, ts[0].Notes, "limite mensal")
	assert.Equal(t, 1, countContaining(w.wa.all(), "atendente"))
}

// Two customer messages arriving together must not produce two transfers, two waiting messages or
// two opportunities.
func TestHandoffFromAI_NoDuplicateOnConcurrentCalls(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	session2 := *session // a second in-memory copy, as a second webhook goroutine would have

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, s := range []*models.ChatbotSession{session, &session2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w.app.handoffFromAI(w.f.account, contact, s, w.f.settings, aiHandoffMarker)
		}()
	}
	close(start)
	wg.Wait()

	assert.Len(t, w.transfers(t, contact.ID), 1, "the unique index allows one active attendance")
	assert.Len(t, w.opportunities(t, contact.ID), 1, "one open opportunity per contact")
	assert.Equal(t, 1, countContaining(w.wa.all(), aiHandoffWaitMessage[:20]), "one waiting message, not two")
}

func TestHandoffFromAI_HumanTakesOverDuringExtraction(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "quero porcelanato")
	w.ai.onCall = func(int) { w.openAttendance(t, contact.ID) }

	assert.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker), "a human owns it: done")

	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1, "only the human's attendance exists")
	assert.Equal(t, models.TransferSourceAgentInitiated, ts[0].Source)
	assert.Zero(t, w.wa.count(), "no waiting message over a human")
}

func TestHandoffFromAI_HumanAlreadyOwnsIt_DoesNothing(t *testing.T) {
	w := newWiring(t, "nunca deve ser chamado")
	contact, session := w.handoffSession(t, "quero porcelanato")
	w.openAttendance(t, contact.ID)

	assert.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Empty(t, w.ai.bodies, "no AI call, no cost")
	assert.Zero(t, w.wa.count())
	assert.Empty(t, w.opportunities(t, contact.ID))
}

func TestHandoffFromAI_InactiveTeamFallsBackToMarkedQueue(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	require.NoError(t, w.app.DB.Model(w.f.team).Update("is_active", false).Error)

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1, "the customer is still attended")
	assert.Nil(t, ts[0].TeamID, "general queue")
	require.NotNil(t, ts[0].RoutingReason)
	assert.Equal(t, models.RoutingReasonAINoUnitTeam, *ts[0].RoutingReason, "marked so it can be found and fixed")
}

func TestHandoffFromAI_OutsideHoursMessageOnce(t *testing.T) {
	w := newWiring(t, qualHappy)
	closed := models.JSONBArray{}
	for d := 0; d < 7; d++ {
		closed = append(closed, map[string]any{"day": float64(d), "enabled": false})
	}
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).Updates(map[string]any{
		"business_hours_enabled": true, "business_hours": closed, "out_of_hours_message": "Voltamos amanhã",
	}).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	w.f.settings.BusinessHours.OutOfHoursMessage = "Voltamos amanhã"

	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")

	assert.False(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker), "no attendance outside hours")
	assert.Len(t, w.opportunities(t, contact.ID), 1, "but the opportunity is registered")
	assert.Empty(t, w.transfers(t, contact.ID))
	assert.Equal(t, 1, countContaining(w.wa.all(), "Voltamos amanhã"))

	// the customer writes again and the agent asks for a human again: no second out-of-hours message
	w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker)
	assert.Equal(t, 1, countContaining(w.wa.all(), "Voltamos amanhã"), "said once per conversation")
	assert.Len(t, w.opportunities(t, contact.ID), 1, "and no second opportunity")
	assert.Equal(t, 1, len(w.ai.bodies), "nor a second extraction")
}

// --- through the processor ----------------------------------------------------------------------

func TestProcessor_MarkerTriggersFullHandoff(t *testing.T) {
	w := newWiring(t, "[[TRANSFERIR]]", qualHappy)
	const from = "5511900000010"
	w.inbound(t, from, "quero porcelanato para a sala, uns 40 m²")

	contact := w.contactFor(t, from)
	sent := w.wa.all()
	require.Len(t, sent, 1, "only the waiting message: the marker alone has no text to send")
	assert.Contains(t, sent[0], "atendente")
	assert.NotContains(t, sent[0], "TRANSFERIR")
	for _, b := range sent {
		assert.NotContains(t, b, "[[", "no technical marker ever reaches the customer")
	}
	assert.Len(t, w.opportunities(t, contact.ID), 1)
	assert.Len(t, w.transfers(t, contact.ID), 1)

	// and from now on the agent is silent
	before := len(w.ai.bodies)
	w.inbound(t, from, "e o prazo?")
	assert.Equal(t, before, len(w.ai.bodies))
	assert.Equal(t, 1, w.wa.count())
}

func TestProcessor_AgentTextThenMarker_SendsTheTextThenHandsOver(t *testing.T) {
	w := newWiring(t, "Perfeito, anotei! [[TRANSFERIR]]", qualHappy)
	const from = "5511900000011"
	w.inbound(t, from, "quero porcelanato para a sala, uns 40 m²")

	sent := w.wa.all()
	require.Len(t, sent, 2)
	assert.Contains(t, sent[0], "Perfeito, anotei!")
	assert.NotContains(t, sent[0], "[[")
	assert.Contains(t, sent[1], "atendente")
}

func TestProcessor_CapReached_HandsOverWithoutCallingTheProvider(t *testing.T) {
	w := newWiring(t, "nunca deve ser chamado")
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).
		Update("ai_monthly_token_limit", 10).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)

	const from = "5511900000012"
	w.inbound(t, from, "quero porcelanato")

	assert.Empty(t, w.ai.bodies, "the provider is never called")
	contact := w.contactFor(t, from)
	assert.Len(t, w.transfers(t, contact.ID), 1)
	require.Equal(t, 1, w.wa.count())
	assert.Contains(t, w.wa.all()[0], "atendente", "a waiting message, never a technical error")
	assert.NotContains(t, w.wa.all()[0], "limit")
}

func TestProcessor_ProviderFailure_NoWaitingMessageThenHandoffOnSecond(t *testing.T) {
	w := newWiring(t)
	w.ai.failStatus = 500
	const from = "5511900000013"

	w.inbound(t, from, "quero porcelanato")
	contact := w.contactFor(t, from)
	assert.Zero(t, w.wa.count(), "one failure: no waiting message, nothing sent")
	assert.Empty(t, w.transfers(t, contact.ID))

	w.inbound(t, from, "alô?")
	assert.Len(t, w.transfers(t, contact.ID), 1, "the second failure in a row hands over")
	require.Equal(t, 1, w.wa.count())
	assert.Contains(t, w.wa.all()[0], "atendente")
	assert.Empty(t, w.opportunities(t, contact.ID), "a failure says nothing about purchase intent")
}

func TestProcessor_ProviderRecovers_FailureCountResets(t *testing.T) {
	w := newWiring(t, "Qual ambiente?")
	const from = "5511900000014"
	w.ai.failStatus = 500
	w.inbound(t, from, "um")
	w.ai.failStatus = 0
	w.inbound(t, from, "dois") // a good answer resets the count
	w.ai.failStatus = 500
	w.inbound(t, from, "três") // failure number one again, not two

	contact := w.contactFor(t, from)
	assert.Empty(t, w.transfers(t, contact.ID), "failures were not consecutive")
}

// A failure of the AI never disturbs a human who is already attending.
func TestProcessor_ProviderFailure_NeverTouchesAnAttendance(t *testing.T) {
	w := newWiring(t)
	w.ai.failStatus = 500
	const from = "5511900000015"
	w.inbound(t, from, "oi") // creates the contact
	contact := w.contactFor(t, from)
	w.openAttendance(t, contact.ID)
	before := w.wa.count()

	w.inbound(t, from, "alô?")
	w.inbound(t, from, "alô???")

	assert.Equal(t, before, w.wa.count())
	assert.Empty(t, w.ai.bodies[1:], "the model is not even called after the takeover")
	assert.Len(t, w.transfers(t, contact.ID), 1, "the human's attendance is the only one")
}
