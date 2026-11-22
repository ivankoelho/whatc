package handlers

import (
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var lawFields = models.QualificationFields{
	{Key: "area", Label: "Área do direito", Type: models.QualFieldChoice, Options: []string{"Trabalhista", "Família", "Cível"}, Required: true},
	{Key: "urgencia", Label: "Urgência", Type: models.QualFieldText, Description: "há prazo correndo?"},
	{Key: "cidade", Label: "Cidade", Type: models.QualFieldText},
	{Key: "processos", Label: "Processos em andamento", Type: models.QualFieldNumber},
}

func TestQualificationFields_AreValidated(t *testing.T) {
	assert.Empty(t, validateQualificationFields(lawFields))
	assert.Empty(t, validateQualificationFields(nil), "no fields is the original qualification")

	bad := func(f models.QualificationField) string { return validateQualificationFields([]models.QualificationField{f}) }
	assert.NotEmpty(t, bad(models.QualificationField{Key: "Area", Label: "x", Type: "text"}), "key is a lowercase slug")
	assert.NotEmpty(t, bad(models.QualificationField{Key: "a", Label: " ", Type: "text"}), "label is required")
	assert.NotEmpty(t, bad(models.QualificationField{Key: "a", Label: "x", Type: "date"}), "unknown type")
	assert.NotEmpty(t, bad(models.QualificationField{Key: "a", Label: "x", Type: "choice", Options: []string{"só uma"}}), "a choice needs two options")
	assert.NotEmpty(t, bad(models.QualificationField{Key: "a", Label: "x", Type: "text", Options: []string{"a", "b"}}), "only a choice has options")
	assert.NotEmpty(t, validateQualificationFields([]models.QualificationField{
		{Key: "a", Label: "x", Type: "text"}, {Key: "a", Label: "y", Type: "text"}}), "keys are unique")

	many := make([]models.QualificationField, 13)
	for i := range many {
		many[i] = models.QualificationField{Key: "k" + strings.Repeat("a", i), Label: "x", Type: "text"}
	}
	assert.NotEmpty(t, validateQualificationFields(many), "at most 12 fields")
}

func TestCustomExtraction_KeepsOnlyWhatTheCustomerProved(t *testing.T) {
	customer := []string{"Fui demitida ontem e quero entrar com uma ação trabalhista", "Moro em Salvador, tenho 2 processos"}
	ans, err := parseCustomExtraction("```json\n" + `{
	  "area": {"valor": "trabalhista", "evidencia": "ação trabalhista"},
	  "urgencia": {"valor": "prazo correndo", "evidencia": "tem um prazo de 5 dias"},
	  "cidade": {"valor": "Salvador", "evidencia": "Moro em Salvador"},
	  "processos": {"valor": 2, "evidencia": "tenho 2 processos"}
	}` + "\n```")
	require.NoError(t, err)

	q := validateCustomExtraction(lawFields, ans, customer)
	require.True(t, q.CustomMode)
	got := map[string]string{}
	for _, f := range q.Custom {
		got[f.Key] = f.Value
	}
	assert.Equal(t, "Trabalhista", got["area"], "a choice is stored with the configured spelling")
	assert.Equal(t, "Salvador", got["cidade"])
	assert.Equal(t, "2", got["processos"])
	assert.NotContains(t, got, "urgencia", "the quote is not in anything the customer wrote")
	assert.Equal(t, []string{"Urgência"}, q.NaoInformado)
}

func TestCustomExtraction_RejectsInventedChoicesNumbersAndAttendantQuotes(t *testing.T) {
	customer := []string{"Preciso de ajuda com uma pensão"}
	ans, err := parseCustomExtraction(`{
	  "area": {"valor": "Penal", "evidencia": "Preciso de ajuda"},
	  "processos": {"valor": 3, "evidencia": "Preciso de ajuda"},
	  "cidade": {"valor": "Recife", "evidencia": "Atendente: de qual cidade?"}
	}`)
	require.NoError(t, err)
	q := validateCustomExtraction(lawFields, ans, customer)
	assert.Empty(t, q.Custom, "a choice outside the options, a number the quote does not hold, a quote from nowhere")
	assert.Len(t, q.NaoInformado, len(lawFields))
}

func TestCustomExtraction_AnUnusableAnswerIsNotAnError(t *testing.T) {
	_, err := parseCustomExtraction("não consigo")
	assert.Error(t, err)
}

func TestAgentRules_ACustomAgentAsksItsOwnFieldsAndKeepsEveryProhibition(t *testing.T) {
	plain := agentRules(&models.AIAgent{})
	assert.Equal(t, aiPlatformRules, plain, "no fields: exactly the original rules")

	rules := agentRules(&models.AIAgent{QualificationFields: lawFields})
	assert.Contains(t, rules, "Área do direito")
	assert.Contains(t, rules, "[opções: Trabalhista, Família, Cível]")
	assert.Contains(t, rules, "insista com educação em: Área do direito")
	assert.NotContains(t, rules, "metragem", "the flooring qualification is gone")
	assert.NotContains(t, rules, "vendedor", "the seller wording becomes the team's")
	for _, kept := range []string{"NUNCA informe, estime ou confirme preços", "NUNCA prometa ou garanta nada", "[[TRANSFERIR]]"} {
		assert.Contains(t, rules, kept)
	}
	assert.Contains(t, agentReminder(&models.AIAgent{QualificationFields: lawFields}), "atendente")
	assert.NotContains(t, agentReminder(&models.AIAgent{QualificationFields: lawFields}), "vendedor")
}

func TestHandoffFromAI_ConfigurableQualificationReachesTheCRM(t *testing.T) {
	answer := qualJSON(map[string]any{
		"area":      map[string]any{"valor": "Trabalhista", "evidencia": "ação trabalhista"},
		"cidade":    map[string]any{"valor": "Salvador", "evidencia": "Moro em Salvador"},
		"processos": nil,
	})
	w := newWiring(t, answer)
	require.NoError(t, w.app.DB.Model(w.f.agent).Update("qualification_fields", lawFields).Error)
	contact, session := w.handoffSession(t, "Quero entrar com uma ação trabalhista", "Moro em Salvador")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1, "a customer who answered what we asked is a lead")
	assert.Contains(t, opps[0].Interest, "Área do direito: Trabalhista")
	assert.Contains(t, opps[0].Interest, "Cidade: Salvador")
	assert.Contains(t, opps[0].Interest, "Ainda não coletado")
	assert.Contains(t, opps[0].Interest, "Urgência")
	assert.NotContains(t, opps[0].Interest, "Metragem", "no flooring fields on a configurable qualification")
	assert.Nil(t, opps[0].EstimatedQuantity, "no product quantity")
	assert.Len(t, w.transfers(t, contact.ID), 1)
}

func TestHandoffFromAI_ConfigurableQualificationWithNothingProvedMakesNoOpportunity(t *testing.T) {
	w := newWiring(t, qualJSON(map[string]any{"area": map[string]any{"valor": "Cível", "evidencia": "frase que não existe"}}))
	require.NoError(t, w.app.DB.Model(w.f.agent).Update("qualification_fields", lawFields).Error)
	contact, session := w.handoffSession(t, "Preciso falar com um advogado")

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Empty(t, w.opportunities(t, contact.ID), "nothing proven, nothing recorded")
	assert.Len(t, w.transfers(t, contact.ID), 1, "the customer is still handed to a person")
}
