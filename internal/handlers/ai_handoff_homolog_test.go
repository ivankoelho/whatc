package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Findings of the real homologation (Meta number, Groq): a failed extraction used to promise "I already passed
// him what we talked about", and a later good qualification never reached the opportunity the failed one had
// left empty.

func TestHandoffFromAI_FailedExtractionDoesNotPromiseASummary(t *testing.T) {
	w := newWiring(t, "isto não é um JSON")
	contact, session := w.handoffSession(t, "quero porcelanato para a sala, uns 40 m²")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	sent := w.wa.all()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "Vou chamar um atendente")
	assert.NotContains(t, sent[0], "Já passei", "nothing was extracted: no promise that a summary was passed")
	require.Len(t, w.opportunities(t, contact.ID), 1, "the bare opportunity is still opened")
}

func TestHandoffFromAI_LaterGoodQualificationCompletesTheBareOpportunity(t *testing.T) {
	w := newWiring(t, "isto não é um JSON", qualHappy)
	contact, session := w.handoffSession(t, "quero porcelanato", "para a sala, uns 40 m²")
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	bare := w.opportunities(t, contact.ID)
	require.Len(t, bare, 1)
	assert.Contains(t, bare[0].Interest, "Qualificação não extraída")
	// the attendance ends (the chatbot is resumed) and the customer is handed over again
	require.NoError(t, w.app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).
		Update("status", models.TransferStatusResumed).Error)

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1, "still one opportunity, never two")
	assert.Equal(t, bare[0].ID, opps[0].ID)
	assert.Contains(t, opps[0].Interest, "Qualificação feita pelo agente de IA")
	assert.NotContains(t, opps[0].Interest, "não extraída")
	require.NotNil(t, opps[0].EstimatedQuantity)
	assert.EqualValues(t, 40, *opps[0].EstimatedQuantity)
	assert.Contains(t, w.wa.all()[1], "Já passei", "this time a summary exists, so the long message is true")
}

func TestHandoffFromAI_OpportunityEditedByAPersonIsNeverOverwritten(t *testing.T) {
	w := newWiring(t, "isto não é um JSON", qualHappy)
	contact, session := w.handoffSession(t, "quero porcelanato", "para a sala, uns 40 m²")
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	opp := w.opportunities(t, contact.ID)[0]
	require.NoError(t, w.app.DB.Model(&models.SalesOpportunity{}).Where("id = ?", opp.ID).
		Update("interest", "Cliente de obra grande, ligar hoje").Error)
	require.NoError(t, w.app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).
		Update("status", models.TransferStatusResumed).Error)

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	after := w.opportunities(t, contact.ID)
	require.Len(t, after, 1)
	assert.Equal(t, "Cliente de obra grande, ligar hoje", after[0].Interest, "what a person typed stays")
	assert.Nil(t, after[0].EstimatedQuantity)
}

// The extraction must leave room for a model that reasons before answering: 600 tokens were used up by the
// reasoning and the answer came back empty (homologation, gpt-oss).
func TestHandoffFromAI_ExtractionRequestsRoomForReasoningModels(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "quero porcelanato", "para a sala, uns 40 m²")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	require.Len(t, w.ai.bodies, 1, "one extraction call")
	var got float64
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if v, ok := w.ai.bodies[0][k].(float64); ok {
			got = v
		}
	}
	assert.EqualValues(t, aiQualificationMaxTokens, got)
	assert.GreaterOrEqual(t, got, float64(2000))
}
