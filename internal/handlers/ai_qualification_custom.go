package handlers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Configurable qualification. An organization that is not selling flooring tells its agent what to find
// out (the area of law, the urgency, the city...) by listing fields. The rules of the original
// qualification hold for every field: a value is kept only when the CUSTOMER's own words prove it.

const (
	qualFieldsMax       = 12
	qualFieldLabelMax   = 60
	qualFieldDescMax    = 200
	qualFieldOptionsMax = 10
	qualFieldOptionMax  = 40
	qualValueMax        = 200
)

var qualFieldKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)

// validateQualificationFields returns a message for the first problem, or "" when the list is usable.
func validateQualificationFields(fields []models.QualificationField) string {
	if len(fields) > qualFieldsMax {
		return "At most 12 qualification fields"
	}
	seen := map[string]bool{}
	for _, f := range fields {
		switch {
		case !qualFieldKeyRe.MatchString(f.Key):
			return "A qualification field key uses a-z, 0-9 and _, starting with a letter (max 30)"
		case seen[f.Key]:
			return "Qualification field keys must be unique"
		case strings.TrimSpace(f.Label) == "" || len([]rune(f.Label)) > qualFieldLabelMax:
			return "A qualification field needs a label (max 60 characters)"
		case len([]rune(f.Description)) > qualFieldDescMax:
			return "A qualification field description accepts at most 200 characters"
		case f.Type != models.QualFieldText && f.Type != models.QualFieldNumber && f.Type != models.QualFieldChoice:
			return "A qualification field type is text, number or choice"
		}
		seen[f.Key] = true
		if f.Type == models.QualFieldChoice {
			if len(f.Options) < 2 || len(f.Options) > qualFieldOptionsMax {
				return "A choice field needs between 2 and 10 options"
			}
			for _, o := range f.Options {
				if strings.TrimSpace(o) == "" || len([]rune(o)) > qualFieldOptionMax {
					return "A choice option needs text (max 40 characters)"
				}
			}
		} else if len(f.Options) > 0 {
			return "Only a choice field has options"
		}
	}
	return ""
}

// normalizeQualificationFields trims what people typed, so a label or an option never carries stray spaces.
func normalizeQualificationFields(in []models.QualificationField) models.QualificationFields {
	if len(in) == 0 {
		return nil
	}
	out := make(models.QualificationFields, 0, len(in))
	for _, f := range in {
		f.Label, f.Description = strings.TrimSpace(f.Label), strings.TrimSpace(f.Description)
		opts := make([]string, len(f.Options))
		for i, o := range f.Options {
			opts[i] = strings.TrimSpace(o)
		}
		f.Options = opts
		if len(opts) == 0 {
			f.Options = nil
		}
		out = append(out, f)
	}
	return out
}

// QualifiedField is one validated answer of a configurable qualification.
type QualifiedField struct{ Key, Label, Value string }

// --- the extraction for a configurable agent --------------------------------------------------------

// customExtractionSystem is the extraction prompt for an agent that lists its own fields.
func customExtractionSystem(fields models.QualificationFields) string {
	var b strings.Builder
	b.WriteString("Você extrai dados de uma conversa de atendimento. Responda SOMENTE com um objeto JSON, sem texto antes ou depois.\n\n")
	b.WriteString("Cada chave abaixo é null se o CLIENTE não informou, ou {\"valor\": ..., \"evidencia\": trecho}.\nCampos:\n")
	for _, f := range fields {
		b.WriteString("- \"" + f.Key + "\" (" + f.Label + "): ")
		switch f.Type {
		case models.QualFieldNumber:
			b.WriteString("número (\"valor\" é um número)")
		case models.QualFieldChoice:
			b.WriteString("uma destas opções, escrita exatamente assim: " + strings.Join(f.Options, " | "))
		default:
			b.WriteString("texto curto")
		}
		if f.Description != "" {
			b.WriteString(". " + f.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nRegras:\n- \"evidencia\" é uma cópia LITERAL de um trecho de mensagem do CLIENTE (linhas \"CLIENTE:\"), nunca do atendente. Sem trecho literal, o campo é null.\n- Nunca deduza, estime nem complete. Se o cliente corrigiu um valor e não está claro qual vale, use null.\n- Não registre opinião, diagnóstico nem análise: apenas o que o cliente disse.")
	return b.String()
}

type customExtracted struct {
	Valor     json.RawMessage `json:"valor"`
	Evidencia string          `json:"evidencia"`
}

// parseCustomExtraction reads the model's JSON object (tolerating a markdown fence) into per-key answers.
func parseCustomExtraction(raw string) (map[string]*customExtracted, error) {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("no JSON object in the extraction answer")
	}
	out := map[string]*customExtracted{}
	if err := json.Unmarshal([]byte(s[i:j+1]), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateCustomExtraction keeps only what the customer's own messages prove.
func validateCustomExtraction(fields models.QualificationFields, ans map[string]*customExtracted, customer []string) *Qualification {
	q := &Qualification{CustomMode: true}
	for _, f := range fields {
		if v := validateCustomField(f, ans[f.Key], customer); v != "" {
			q.Custom = append(q.Custom, QualifiedField{Key: f.Key, Label: f.Label, Value: v})
		} else {
			q.NaoInformado = append(q.NaoInformado, f.Label)
		}
	}
	return q
}

func validateCustomField(f models.QualificationField, e *customExtracted, customer []string) string {
	if e == nil || len(e.Valor) == 0 || string(e.Valor) == "null" || !evidenceFromCustomer(e.Evidencia, customer) {
		return ""
	}
	switch f.Type {
	case models.QualFieldNumber:
		var n float64
		if json.Unmarshal(e.Valor, &n) != nil || !evidenceHasNumber(e.Evidencia, n) {
			return ""
		}
		return formatNumber(n, "")
	case models.QualFieldChoice:
		var s string
		if json.Unmarshal(e.Valor, &s) != nil {
			return ""
		}
		for _, o := range f.Options {
			if compact(o) == compact(s) {
				return o // the configured spelling, never the model's
			}
		}
		return ""
	default:
		var s string
		if json.Unmarshal(e.Valor, &s) != nil {
			return ""
		}
		s = strings.TrimSpace(s)
		if s == "" || len([]rune(s)) > qualValueMax || !validateTextField(s, e.Evidencia, customer) {
			return ""
		}
		return s
	}
}

// extractCustomQualification is extractQualification for an agent that lists its own fields. call makes
// the (cap-counted) model call.
func extractCustomQualification(req ai.Request, fields models.QualificationFields, customer []string,
	call func(ai.Request) (*ai.Response, error)) *Qualification {
	req.System = customExtractionSystem(fields)
	resp, err := call(req)
	if err != nil {
		return &Qualification{ExtractionFailed: true, CustomMode: true}
	}
	ans, err := parseCustomExtraction(resp.Text)
	if err != nil {
		return &Qualification{ExtractionFailed: true, CustomMode: true}
	}
	return validateCustomExtraction(fields, ans, customer)
}

// formatCustomInterest is the CRM text of a configurable qualification.
func formatCustomInterest(q *Qualification) string {
	var b strings.Builder
	b.WriteString("Qualificação feita pelo agente de IA.\n\nInformado pelo cliente:\n")
	for _, f := range q.Custom {
		b.WriteString("- " + f.Label + ": " + f.Value + "\n")
	}
	if len(q.Custom) == 0 {
		b.WriteString("- (nada informado de forma verificável)\n")
	}
	if len(q.NaoInformado) > 0 {
		b.WriteString("\nAinda não coletado:\n")
		for _, l := range q.NaoInformado {
			b.WriteString("- " + l + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// --- what the agent is told to ask ------------------------------------------------------------------

// productQualificationLine is the line of the platform rules that is about flooring.
const productQualificationLine = "- Qualificação: descubra, conforme couber, o produto, a categoria, a quantidade, a metragem, o ambiente e a intenção de compra."

// agentRules are the platform rules for this agent. For an agent with its own qualification fields the
// product qualification line is replaced by that list, and the seller wording becomes the team's; every
// prohibition (price, deadline, promises) is untouched.
func agentRules(agent *models.AIAgent) string {
	if agent == nil || len(agent.QualificationFields) == 0 {
		return aiPlatformRules
	}
	rules := strings.Replace(aiPlatformRules, productQualificationLine, customQualificationLine(agent.QualificationFields), 1)
	return strings.ReplaceAll(rules, "vendedor", "atendente")
}

func customQualificationLine(fields models.QualificationFields) string {
	var items, required []string
	for _, f := range fields {
		it := f.Label
		if f.Description != "" {
			it += " (" + f.Description + ")"
		}
		if f.Type == models.QualFieldChoice {
			it += " [opções: " + strings.Join(f.Options, ", ") + "]"
		}
		items = append(items, it)
		if f.Required {
			required = append(required, f.Label)
		}
	}
	s := "- Qualificação: descubra, conforme couber, estas informações do cliente: " + strings.Join(items, "; ") + "."
	if len(required) > 0 {
		s += " Antes de passar o atendimento, insista com educação em: " + strings.Join(required, ", ") + "."
	}
	return s
}

// agentReminder is the closing reminder, worded for the agent's team.
func agentReminder(agent *models.AIAgent) string {
	if agent == nil || len(agent.QualificationFields) == 0 {
		return aiPlatformReminder
	}
	return strings.ReplaceAll(aiPlatformReminder, "vendedor", "atendente")
}
