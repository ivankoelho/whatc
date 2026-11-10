package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Qualification is what the agent learned from the customer, validated field by field. A field
// the customer did not state, or that cannot be proven from what the customer wrote, is left
// empty and listed in NaoInformado: nothing is ever filled in by guessing.
type Qualification struct {
	Produto, Categoria, Ambiente, Intencao string
	Quantidade                             *float64
	QuantidadeUnidade                      string // as the customer wrote it ("caixas")
	Metragem                               *float64
	Resumo                                 string   // written by the model: label it, never trust it as data
	NaoInformado                           []string // human labels of the fields not informed
	ExtractionFailed                       bool     // the extraction call or its answer was unusable
}

const (
	qualProduto    = "Produto"
	qualCategoria  = "Categoria"
	qualQuantidade = "Quantidade"
	qualMetragem   = "Metragem"
	qualAmbiente   = "Ambiente"
	qualIntencao   = "Intenção de compra"
)

var qualFieldOrder = []string{qualProduto, qualCategoria, qualQuantidade, qualMetragem, qualAmbiente, qualIntencao}

// --- reading the customer's numbers -----------------------------------------------------------------

type numericKind int

const (
	kindArea  numericKind = iota // square metres
	kindCount                    // boxes, pieces, packs, linear metres...
)

const (
	areaUnitPattern  = `(?:m²|m2|mt²|mts²|metros? quadrados?|mts? quadrados?)`
	countUnitPattern = `(?:caixas?|cx|pecas?|pacotes?|pct|unidades?|un|sacos?|metros?|mts?)`
	numberPattern    = `\d[\d.,]*`
)

// mention = NUMBER [(ou|a|ate|e|/|-) NUMBER] UNIT, then any non-letter. A "40 ou 50 m²" gives two
// numbers: the customer did not say one value.
var (
	areaMentionRE  = regexp.MustCompile(`(` + numberPattern + `)(?:\s*(?:ou|a|ate|e|/|-)\s*(` + numberPattern + `))?\s*` + areaUnitPattern + `(?:$|[^a-z0-9])`)
	countMentionRE = regexp.MustCompile(`(` + numberPattern + `)(?:\s*(?:ou|a|ate|e|/|-)\s*(` + numberPattern + `))?\s*` + countUnitPattern + `(?:$|[^a-z0-9])`)

	// words right before a number that take it back or make it a bound, not a statement
	valueNegators = map[string]bool{"nao": true, "nem": true, "nunca": true, "sem": true, "menos": true, "mais": true}
	// words that take back a thing the customer named ("não quero porcelanato")
	textNegators    = map[string]bool{"nao": true, "nem": true, "nunca": true, "sem": true}
	replacedPhrases = []string{"em vez de", "ao inves de", "no lugar de", "em lugar de", "ao inves"}
)

// parsePTNumber reads "40", "40,5", "1.200" and "1.200,50" the Brazilian way.
func parsePTNumber(s string) (float64, bool) {
	s = strings.Trim(strings.TrimSpace(s), ".,")
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if parts := strings.Split(s, "."); len(parts) > 1 {
		thousands := true
		for _, p := range parts[1:] {
			if len(p) != 3 {
				thousands = false
			}
		}
		if thousands {
			s = strings.ReplaceAll(s, ".", "")
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

// negatedBefore reports whether the text right before pos takes the number back.
func negatedBefore(folded string, pos int) bool {
	if precededBy(folded, pos, 3, valueNegators) {
		return true
	}
	tail := folded[:pos]
	if len(tail) > 28 {
		tail = tail[len(tail)-28:]
	}
	tail = strings.TrimSpace(tail)
	for _, ph := range replacedPhrases {
		if strings.HasSuffix(tail, ph) {
			return true
		}
	}
	return false
}

// numericMentions returns the distinct values of kind that the customer stated without taking
// them back, over all the customer's messages.
func numericMentions(kind numericKind, customerMessages []string) []float64 {
	var out []float64
	add := func(v float64) {
		for _, x := range out {
			if math.Abs(x-v) < 1e-9 {
				return
			}
		}
		out = append(out, v)
	}
	for _, msg := range customerMessages {
		f := knowledge.FoldForSearch(msg)
		if kind == kindCount { // "40 metros quadrados" is an area, not 40 linear metres
			f = areaMentionRE.ReplaceAllString(f, " ")
		}
		re := areaMentionRE
		if kind == kindCount {
			re = countMentionRE
		}
		for _, m := range re.FindAllStringSubmatchIndex(f, -1) {
			if negatedBefore(f, m[0]) {
				continue
			}
			for g := 1; g <= 2; g++ {
				if m[2*g] < 0 {
					continue
				}
				if v, ok := parsePTNumber(f[m[2*g]:m[2*g+1]]); ok && v > 0 {
					add(v)
				}
			}
		}
	}
	return out
}

// validateNumericField accepts value only if, among everything the customer stated for this kind
// of quantity (negated or corrected values removed), it is the one and only value. "40 m², não
// 20 m²" gives 40; "40 m² ou 50 m²" is ambiguous and gives nothing; a number in the wrong unit
// ("40 caixas" as an area) is never found.
func validateNumericField(kind numericKind, value float64, customerMessages []string) bool {
	m := numericMentions(kind, customerMessages)
	return len(m) == 1 && math.Abs(m[0]-value) < 1e-9
}

// --- reading the customer's words ---------------------------------------------------------------

// compact folds text and turns every non-alphanumeric run into one space, so a quote and the
// message it came from compare equal despite punctuation.
func compact(s string) string {
	f := knowledge.FoldForSearch(s)
	var b strings.Builder
	for _, r := range f {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '²' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// evidenceFromCustomer reports whether evidence is a literal part of something the CUSTOMER wrote.
func evidenceFromCustomer(evidence string, customerMessages []string) bool {
	e := compact(evidence)
	if len(e) < 3 {
		return false
	}
	for _, m := range customerMessages {
		if strings.Contains(compact(m), e) {
			return true
		}
	}
	return false
}

// validateTextField accepts value (product, category, environment) only if the quoted evidence is
// the customer's own words, every word of the value appears in that evidence, and the customer
// did not take it back ("não quero porcelanato").
func validateTextField(value, evidence string, customerMessages []string) bool {
	v, e := compact(value), compact(evidence)
	if v == "" || !evidenceFromCustomer(evidence, customerMessages) {
		return false
	}
	words := strings.Fields(v)
	for _, w := range words {
		if !strings.Contains(e, w) {
			return false
		}
	}
	// at least one occurrence of the first word that is not negated
	first := words[0]
	for from := 0; ; {
		i := strings.Index(e[from:], first)
		if i < 0 {
			return false
		}
		pos := from + i
		if !precededBy(e, pos, 3, textNegators) {
			return true
		}
		from = pos + len(first)
	}
}

// --- the extraction call ---------------------------------------------------------------------------

const aiQualificationSystem = `Você extrai dados de uma conversa de vendas de pisos e revestimentos. Responda SOMENTE com um objeto JSON, sem texto antes ou depois.

Campos (cada um é null se o CLIENTE não informou): "produto", "categoria", "ambiente", "intencao" têm a forma {"valor": texto, "evidencia": trecho}; "quantidade" tem a forma {"valor": número, "unidade": texto, "evidencia": trecho}; "metragem" tem a forma {"valor": número em m², "evidencia": trecho}. Mais "resumo": texto curto com o que o cliente quer.

Regras:
- "evidencia" é uma cópia LITERAL de um trecho de mensagem do CLIENTE (linhas "CLIENTE:"), nunca do atendente. Sem trecho literal, o campo é null.
- Nunca deduza, estime nem complete. Se o cliente corrigiu um valor, use null quando não for claro qual vale.
- "intencao": só se o cliente demonstrou querer comprar ou pediu orçamento; use "comprar" ou "orcamento" como valor.
- "metragem" é área em m². "quantidade" é contagem (caixas, peças, pacotes, unidades).`

type extractedText struct {
	Valor     string `json:"valor"`
	Evidencia string `json:"evidencia"`
}

type extractedNumber struct {
	Valor     *float64 `json:"valor"`
	Unidade   string   `json:"unidade"`
	Evidencia string   `json:"evidencia"`
}

type extractionAnswer struct {
	Produto    *extractedText   `json:"produto"`
	Categoria  *extractedText   `json:"categoria"`
	Ambiente   *extractedText   `json:"ambiente"`
	Intencao   *extractedText   `json:"intencao"`
	Quantidade *extractedNumber `json:"quantidade"`
	Metragem   *extractedNumber `json:"metragem"`
	Resumo     string           `json:"resumo"`
}

// parseExtraction reads the model's answer, tolerating a markdown fence around the JSON.
func parseExtraction(raw string) (*extractionAnswer, error) {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("no JSON object in the extraction answer")
	}
	var ans extractionAnswer
	if err := json.Unmarshal([]byte(s[i:j+1]), &ans); err != nil {
		return nil, err
	}
	return &ans, nil
}

// validateExtraction turns the model's answer into a Qualification, keeping only what is proven by
// the customer's own messages. Everything else is listed as not informed.
func validateExtraction(ans *extractionAnswer, customerMessages []string) *Qualification {
	q := &Qualification{Resumo: strings.TrimSpace(ans.Resumo)}
	ok := map[string]bool{}

	text := func(label string, f *extractedText, dst *string) {
		if f != nil && validateTextField(f.Valor, f.Evidencia, customerMessages) {
			*dst, ok[label] = strings.TrimSpace(f.Valor), true
		}
	}
	text(qualProduto, ans.Produto, &q.Produto)
	text(qualCategoria, ans.Categoria, &q.Categoria)
	text(qualAmbiente, ans.Ambiente, &q.Ambiente)

	// the intent is an interpretation of the customer's words: it needs the customer's own quote
	// and a known value, but not the value's word inside the quote
	if f := ans.Intencao; f != nil && evidenceFromCustomer(f.Evidencia, customerMessages) {
		switch v := compact(f.Valor); v {
		case "comprar", "compra":
			q.Intencao, ok[qualIntencao] = "comprar", true
		case "orcamento":
			q.Intencao, ok[qualIntencao] = "orçamento", true
		}
	}
	if f := ans.Metragem; f != nil && f.Valor != nil && evidenceFromCustomer(f.Evidencia, customerMessages) &&
		validateNumericField(kindArea, *f.Valor, customerMessages) {
		v := *f.Valor
		q.Metragem, ok[qualMetragem] = &v, true
	}
	if f := ans.Quantidade; f != nil && f.Valor != nil && evidenceFromCustomer(f.Evidencia, customerMessages) &&
		validateNumericField(kindCount, *f.Valor, customerMessages) {
		v := *f.Valor
		q.Quantidade, q.QuantidadeUnidade, ok[qualQuantidade] = &v, strings.TrimSpace(f.Unidade), true
	}
	for _, label := range qualFieldOrder {
		if !ok[label] {
			q.NaoInformado = append(q.NaoInformado, label)
		}
	}
	return q
}

// qualificationTranscript is the conversation as the extraction prompt sees it, plus the
// customer's messages alone (the only ones evidence may come from).
func (a *App) qualificationTranscript(session *models.ChatbotSession) (transcript string, customer []string) {
	var b strings.Builder
	for _, m := range a.getSessionHistory(session.ID, 40) {
		who := "ATENDENTE"
		if m.Direction == models.DirectionIncoming {
			who = "CLIENTE"
			customer = append(customer, m.Message)
		}
		b.WriteString(who + ": " + strings.ReplaceAll(m.Message, "\n", " ") + "\n")
	}
	return b.String(), customer
}

// extractQualification asks the model for the structured data (one extra AI call, counted against
// the monthly cap) and validates every field against the customer's own messages. A failed call
// or an unusable answer is reported as ExtractionFailed, never as an error that stops the handoff.
func (a *App) extractQualification(ctx context.Context, settings *models.ChatbotSettings, account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession) *Qualification {
	transcript, customer := a.qualificationTranscript(session)
	if len(customer) == 0 {
		return &Qualification{ExtractionFailed: true}
	}
	contactID, sessionID := contact.ID, session.ID
	meta := aiCallMeta{Feature: aiFeatureQualification, OrgID: settings.OrganizationID, Account: account.Name,
		ContactID: &contactID, SessionID: &sessionID, Agent: true}
	resp, err := a.completeAI(ctx, settings, meta, ai.Request{
		Model:       settings.AI.Model,
		System:      aiQualificationSystem,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: transcript}},
		MaxTokens:   600,
		Temperature: 0,
	})
	if err != nil {
		a.Log.Warn("Qualification extraction failed", "error", err, "contact_id", contact.ID)
		return &Qualification{ExtractionFailed: true}
	}
	ans, err := parseExtraction(resp.Text)
	if err != nil {
		a.Log.Warn("Qualification extraction returned an unusable answer", "error", err, "contact_id", contact.ID)
		return &Qualification{ExtractionFailed: true}
	}
	return validateExtraction(ans, customer)
}

// --- what goes into the CRM ---------------------------------------------------------------------

func formatNumber(v float64, unit string) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	s = strings.Replace(s, ".", ",", 1)
	if unit != "" {
		s += " " + unit
	}
	return s
}

// formatInterest is the text stored in SalesOpportunity.Interest: what the customer said, what is
// still missing, and the model's summary labelled as such.
func formatInterest(q *Qualification) string {
	if q == nil || q.ExtractionFailed {
		return "Atendimento qualificado pelo agente de IA.\nQualificação não extraída: confira a conversa."
	}
	var b strings.Builder
	b.WriteString("Qualificação feita pelo agente de IA.\n\nInformado pelo cliente:\n")
	line := func(label, val string) {
		if val != "" {
			b.WriteString("- " + label + ": " + val + "\n")
		}
	}
	line(qualProduto, q.Produto)
	line(qualCategoria, q.Categoria)
	if q.Quantidade != nil {
		line(qualQuantidade, formatNumber(*q.Quantidade, q.QuantidadeUnidade))
	}
	if q.Metragem != nil {
		line(qualMetragem, formatNumber(*q.Metragem, "m²"))
	}
	line(qualAmbiente, q.Ambiente)
	line(qualIntencao, q.Intencao)
	if len(q.NaoInformado) == len(qualFieldOrder) {
		b.WriteString("- (nada informado de forma verificável)\n")
	}
	if len(q.NaoInformado) > 0 {
		b.WriteString("\nAinda não coletado:\n")
		for _, l := range q.NaoInformado {
			b.WriteString("- " + l + "\n")
		}
	}
	if q.Resumo != "" {
		b.WriteString("\nResumo automático (gerado por IA, confira a conversa): " + q.Resumo + "\n")
	}
	return strings.TrimSpace(b.String())
}

// salesUnitForCount maps what the customer wrote to the funnel's closed list of units, or "" when
// there is no safe mapping.
func salesUnitForCount(unit string) models.SalesUnitOfMeasure {
	switch compact(unit) {
	case "caixa", "caixas", "cx":
		return models.SalesUnitCaixa
	case "peca", "pecas", "unidade", "unidades", "un":
		return models.SalesUnitUnidade
	case "pacote", "pacotes", "pct":
		return models.SalesUnitPacote
	case "metro", "metros", "mt", "mts":
		return models.SalesUnitMetro
	}
	return ""
}

// applyQualification fills the quantity and unit of a NEW opportunity, only with validated values
// that the funnel accepts (more than zero, at most three decimals).
func applyQualification(q *Qualification) func(*models.SalesOpportunity) {
	return func(o *models.SalesOpportunity) {
		if q == nil || q.ExtractionFailed {
			return
		}
		set := func(v float64, unit models.SalesUnitOfMeasure) {
			if v <= 0 || math.Abs(v*1000-math.Round(v*1000)) > 1e-6 || unit == "" {
				return
			}
			o.EstimatedQuantity = &v
			o.UnitOfMeasure = &unit
		}
		switch {
		case q.Metragem != nil:
			set(*q.Metragem, models.SalesUnitMetro2)
		case q.Quantidade != nil:
			set(*q.Quantidade, salesUnitForCount(q.QuantidadeUnidade))
		}
	}
}
