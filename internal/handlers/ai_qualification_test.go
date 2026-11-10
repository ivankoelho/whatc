package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateNumericField(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind numericKind
		val  float64
		msgs []string
		want bool
	}{
		{"correction: the number kept", kindArea, 40, []string{"preciso de 40 m², não 20 m²"}, true},
		{"correction: the number taken back", kindArea, 20, []string{"preciso de 40 m², não 20 m²"}, false},
		{"plain", kindArea, 40, []string{"quero 40 m2"}, true},
		{"unit spelled out", kindArea, 40, []string{"uns 40 metros quadrados"}, true},
		{"two explicit values are ambiguous", kindArea, 40, []string{"40 m² ou 50 m²"}, false},
		{"two explicit values are ambiguous (other)", kindArea, 50, []string{"40 m² ou 50 m²"}, false},
		{"range with one unit is ambiguous", kindArea, 50, []string{"entre 40 ou 50 m²"}, false},
		{"replaced value", kindArea, 40, []string{"em vez de 20 m² vai ser 40 m²"}, true},
		{"replaced value taken back", kindArea, 20, []string{"em vez de 20 m² vai ser 40 m²"}, false},
		{"two messages two values", kindArea, 40, []string{"40 m²", "na verdade 50 m²"}, false},
		{"same value twice is fine", kindArea, 40, []string{"40 m²", "sim, 40 m² mesmo"}, true},
		{"count in the area slot", kindArea, 40, []string{"preciso de 40 caixas"}, false},
		{"boxes", kindCount, 40, []string{"preciso de 40 caixas"}, true},
		{"area is not a count", kindCount, 40, []string{"preciso de 40 m²"}, false},
		{"metres squared is not linear metres", kindCount, 40, []string{"quero 40 metros quadrados"}, false},
		{"model value absent from the messages", kindArea, 60, []string{"quero 40 m²"}, false},
		{"no number at all", kindArea, 40, []string{"quero porcelanato"}, false},
		{"decimal comma", kindArea, 12.5, []string{"12,5 m²"}, true},
		{"thousands dot", kindArea, 1200, []string{"1.200 m²"}, true},
		{"a bound is not a statement", kindArea, 40, []string{"menos de 40 m²"}, false},
		{"somebody else's number", kindArea, 60, []string{"meu vizinho tem 60 m2"}, false},
		{"half of something", kindArea, 80, []string{"quero a metade de 80 m²"}, false},
		{"dimensions are not an area", kindArea, 50, []string{"a sala é 40x50 m2"}, false},
		{"bought in the past", kindArea, 30, []string{"comprei 30 m2 ano passado"}, false},
		{"upper bound", kindArea, 40, []string{"no máximo 40 m2"}, false},
		{"upper bound (até)", kindArea, 40, []string{"até 40 m2"}, false},
		{"area of the box is not the order's area", kindArea, 2.5, []string{"quero 10 caixas, a caixa tem 2,5 m²"}, false},
		{"box count next to the box area", kindCount, 10, []string{"quero 10 caixas, a caixa tem 2,5 m²"}, true},
		{"area per package", kindArea, 2, []string{"4 caixas de 2 m²"}, false},
		{"area per box (cada)", kindArea, 2, []string{"cada caixa 2 m²"}, false},
		{"packages", kindCount, 4, []string{"4 caixas de 2 m²"}, true},
		{"correction without a unit", kindArea, 20, []string{"20 m² não, 40"}, false},
		{"correction without a unit (other value)", kindArea, 40, []string{"20 m² não, 40"}, false},
		{"correction in the next message", kindArea, 20, []string{"20 m2", "não, 40"}, false},
		{"correction in the next message (errei)", kindArea, 20, []string{"20 m2", "errei, é 40"}, false},
		{"a sum is not one value", kindArea, 40, []string{"40m2 mais 10m2 de varanda"}, false},
		{"'metros' alone is not safe as an area", kindArea, 40, []string{"preciso de 40 metros de piso"}, false},
		{"'metros' alone is not a count either", kindCount, 40, []string{"preciso de 40 metros de piso"}, false},
		{"negated by 'nem'", kindArea, 30, []string{"nem 30 m² nem 50 m²"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validateNumericField(tc.kind, tc.val, tc.msgs))
		})
	}
}

func TestValidateTextField(t *testing.T) {
	msgs := []string{"Oi, quero porcelanato retificado para a cozinha", "não quero madeira", "não quero porcelanato, quero vinílico", "porcelanato não, laminado", "vou fazer uma salada"}
	for _, tc := range []struct {
		name, value, evidence string
		want                  bool
	}{
		{"stated", "porcelanato", "quero porcelanato retificado", true},
		{"all words of the value must be in the quote", "porcelanato retificado", "quero porcelanato retificado", true},
		{"value word missing from the quote", "porcelanato polido", "quero porcelanato retificado", false},
		{"quote not from the customer", "porcelanato", "recomendo porcelanato", false},
		{"quote from the customer, other thing", "cozinha", "quero porcelanato retificado para a cozinha", true},
		{"negated by the customer", "madeira", "não quero madeira", false},
		{"taken back outside the quote", "vinílico", "quero vinílico", true},
		{"negation before the term, quote without it", "porcelanato", "porcelanato, quero vinílico", false},
		{"taken back after the term", "laminado", "laminado", false},
		{"whole word only", "sala", "sala", false},
		{"empty value", "", "quero porcelanato", false},
		{"empty quote", "porcelanato", "", false},
		{"punctuation and accents do not matter", "cozinha", "Oi, QUERO porcelanato retificado para a cozinha!", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validateTextField(tc.value, tc.evidence, msgs))
		})
	}
}

func TestParseExtraction(t *testing.T) {
	ans, err := parseExtraction("```json\n{\"produto\":{\"valor\":\"porcelanato\",\"evidencia\":\"quero porcelanato\"},\"resumo\":\"x\"}\n```")
	require.NoError(t, err)
	require.NotNil(t, ans.Produto)
	assert.Equal(t, "porcelanato", ans.Produto.Valor)

	_, err = parseExtraction("não sei")
	assert.Error(t, err)
	_, err = parseExtraction("{ quebrado")
	assert.Error(t, err)
}

func f64(v float64) *float64 { return &v }

// The product owner's example: the customer said "40 m², não 20 m²"; the model must never get 20 in.
func TestValidateExtraction_CorrectionKeepsTheRightMetragem(t *testing.T) {
	customer := []string{"quero porcelanato para a sala", "preciso de 40 m², não 20 m²"}

	wrong := validateExtraction(&extractionAnswer{
		Produto:  &extractedText{Valor: "porcelanato", Evidencia: "quero porcelanato para a sala"},
		Metragem: &extractedNumber{Valor: f64(20), Evidencia: "não 20 m²"},
	}, customer)
	assert.Nil(t, wrong.Metragem, "20 m² was taken back by the customer")
	assert.Contains(t, wrong.NaoInformado, qualMetragem)
	assert.Equal(t, "porcelanato", wrong.Produto)

	right := validateExtraction(&extractionAnswer{
		Metragem: &extractedNumber{Valor: f64(40), Evidencia: "preciso de 40 m²"},
	}, customer)
	require.NotNil(t, right.Metragem)
	assert.Equal(t, 40.0, *right.Metragem)
}

func TestValidateExtraction_NothingIsInvented(t *testing.T) {
	customer := []string{"quero porcelanato"}
	q := validateExtraction(&extractionAnswer{
		Produto:    &extractedText{Valor: "porcelanato", Evidencia: "quero porcelanato"},
		Ambiente:   &extractedText{Valor: "cozinha", Evidencia: "para a cozinha"}, // never said
		Categoria:  &extractedText{Valor: "revestimento", Evidencia: ""},          // no quote
		Quantidade: &extractedNumber{Valor: f64(10), Unidade: "caixas", Evidencia: "10 caixas"},
		Metragem:   &extractedNumber{Valor: f64(40), Evidencia: "40 m²"},
		Intencao:   &extractedText{Valor: "comprar", Evidencia: "recomendo comprar"}, // not the customer's words
	}, customer)

	assert.Equal(t, "porcelanato", q.Produto)
	assert.Empty(t, q.Ambiente)
	assert.Empty(t, q.Categoria)
	assert.Nil(t, q.Quantidade)
	assert.Nil(t, q.Metragem)
	assert.Empty(t, q.Intencao)
	assert.ElementsMatch(t, []string{qualCategoria, qualQuantidade, qualMetragem, qualAmbiente, qualIntencao}, q.NaoInformado)
}

func TestValidateExtraction_IntentNeedsTheCustomersQuote(t *testing.T) {
	q := validateExtraction(&extractionAnswer{
		Intencao: &extractedText{Valor: "comprar", Evidencia: "quero comprar hoje"},
	}, []string{"Oi, quero comprar hoje"})
	assert.Equal(t, "comprar", q.Intencao)

	q = validateExtraction(&extractionAnswer{
		Intencao: &extractedText{Valor: "talvez", Evidencia: "quero comprar hoje"}, // not a known intent
	}, []string{"Oi, quero comprar hoje"})
	assert.Empty(t, q.Intencao)
}

func TestValidateExtraction_IntentNeedsBuyingWords(t *testing.T) {
	q := validateExtraction(&extractionAnswer{
		Intencao: &extractedText{Valor: "comprar", Evidencia: "bom dia"},
	}, []string{"bom dia"})
	assert.Empty(t, q.Intencao, "a greeting is not a purchase intent")

	q = validateExtraction(&extractionAnswer{
		Intencao: &extractedText{Valor: "orcamento", Evidencia: "quanto custa o m2"},
	}, []string{"quanto custa o m2"})
	assert.Equal(t, "orçamento", q.Intencao)
}

func TestValidateExtraction_NumberEvidenceMustContainTheNumber(t *testing.T) {
	customer := []string{"quero porcelanato para a sala, uns 40 m²"}
	q := validateExtraction(&extractionAnswer{
		Metragem: &extractedNumber{Valor: f64(40), Evidencia: "quero porcelanato para a sala"}, // the quote does not carry the number
	}, customer)
	assert.Nil(t, q.Metragem)

	q = validateExtraction(&extractionAnswer{
		Metragem: &extractedNumber{Valor: f64(40), Evidencia: "uns 40 m²"},
	}, customer)
	require.NotNil(t, q.Metragem)
}

func TestValidateExtraction_UnitComesFromTheCustomerNotTheModel(t *testing.T) {
	q := validateExtraction(&extractionAnswer{
		Quantidade: &extractedNumber{Valor: f64(10), Unidade: "caixas", Evidencia: "preciso de 10 peças"},
	}, []string{"preciso de 10 peças"})
	require.NotNil(t, q.Quantidade)
	assert.Equal(t, "pecas", q.QuantidadeUnidade, "the unit is the customer's, not the model's")
	assert.Equal(t, models.SalesUnitUnidade, salesUnitForCount(q.QuantidadeUnidade))
}

func TestFormatInterest_SeparatesProvidedFromMissing(t *testing.T) {
	q := &Qualification{Produto: "porcelanato", Metragem: f64(40), Intencao: "comprar",
		NaoInformado: []string{qualCategoria, qualQuantidade, qualAmbiente}}
	s := formatInterest(q)

	provided := s[:indexOf(s, "Ainda não coletado")]
	assert.Contains(t, provided, "Informado pelo cliente")
	assert.Contains(t, provided, "Produto: porcelanato")
	assert.Contains(t, provided, "Metragem: 40 m²")
	assert.NotContains(t, provided, "Ambiente", "a field not informed never appears among the informed ones")

	missing := s[indexOf(s, "Ainda não coletado"):]
	assert.Contains(t, missing, "Ambiente")
	assert.Contains(t, missing, "Quantidade")
	assert.NotContains(t, s, "Resumo", "the unvalidated summary of the model never reaches the CRM")

	failed := formatInterest(&Qualification{ExtractionFailed: true})
	assert.Contains(t, failed, "Qualificação não extraída")
	assert.NotContains(t, failed, "Informado pelo cliente")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return len(s)
}

func TestApplyQualification(t *testing.T) {
	apply := func(q *Qualification) models.SalesOpportunity {
		var o models.SalesOpportunity
		applyQualification(q)(&o)
		return o
	}
	o := apply(&Qualification{Metragem: f64(40.5), Quantidade: f64(10), QuantidadeUnidade: "caixas"})
	require.NotNil(t, o.EstimatedQuantity)
	assert.Equal(t, 40.5, *o.EstimatedQuantity)
	assert.Equal(t, models.SalesUnitMetro2, *o.UnitOfMeasure, "an area wins over a count")

	o = apply(&Qualification{Quantidade: f64(10), QuantidadeUnidade: "Caixas"})
	require.NotNil(t, o.EstimatedQuantity)
	assert.Equal(t, models.SalesUnitCaixa, *o.UnitOfMeasure)

	assert.Nil(t, apply(&Qualification{Quantidade: f64(10), QuantidadeUnidade: "sacolas"}).EstimatedQuantity, "no safe unit, no quantity")
	assert.Nil(t, apply(&Qualification{Metragem: f64(1.2345)}).EstimatedQuantity, "more than three decimals is refused, not rounded")
	assert.Nil(t, apply(&Qualification{ExtractionFailed: true, Metragem: f64(40)}).EstimatedQuantity)
	assert.Nil(t, apply(nil).EstimatedQuantity)
}
