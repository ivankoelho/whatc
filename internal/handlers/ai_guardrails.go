package handlers

import (
	"regexp"
	"strings"

	"github.com/shridarpatil/whatomate/internal/knowledge"
)

// Why an AI reply asks for a human (aiReply.Handoff).
const (
	aiHandoffMarker = "marker" // the model emitted [[TRANSFERIR]]
	aiHandoffUnsafe = "unsafe" // the reply kept breaking the commercial-safety rules
)

// handoffMarkerRE matches the technical marker the model emits to hand the conversation to a
// human, in any case and spacing, and also a marker cut short by the token limit. The marker
// must NEVER reach the customer.
var handoffMarkerRE = regexp.MustCompile(`(?i)\[\[\s*transferir\s*(\]\]|$)`)

// extractHandoff removes every handoff marker from text and reports whether there was one.
func extractHandoff(text string) (clean string, handoff bool) {
	if !handoffMarkerRE.MatchString(text) {
		return strings.TrimSpace(text), false
	}
	clean = handoffMarkerRE.ReplaceAllString(text, "")
	return strings.TrimSpace(clean), true
}

// The AI has no trusted source of price or stock in the MVP, so anything it says about them,
// or any promise, is invented unless the organization wrote it into the agent's own text.
// The check is a heuristic on accent- and case-folded text; the known ceiling is that it cannot
// catch every way of promising. A hit costs one regeneration, then a human.
var (
	commitmentRE = regexp.MustCompile(`\b(garanto|garantimos|prometo|prometemos|reservei|reservamos|(esta|fica) (garantid[oa]|reservad[oa]))\b`)
	moneyRE      = regexp.MustCompile(`r\$\s*(\d[\d.]*(?:,\d+)?)|\b(\d[\d.]*(?:,\d+)?)\s*reais\b`)
	percentRE    = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*%`)
	discountRE   = regexp.MustCompile(`desconto|\boff\b|promocao`)
	stockRE      = regexp.MustCompile(`\b(em estoque|pronta entrega|(temos|tem|esta|estao) disponive(l|is)|disponive(l|is) (para entrega|imediatamente|agora))\b`)
	deadlineRE   = regexp.MustCompile(`\b(prazo|entrega|entregamos|entregue|entregar|chega|chegara)\b[^.!?\n]{0,30}?\b(\d+|um|dois|tres|quatro|cinco|seis|sete|oito|nove|dez)\s*(dias?|horas?|semanas?)\b|\bentreg\w*\s+(amanha|hoje)\b`)
	digitsOnlyRE = regexp.MustCompile(`\d+`)
)

// words that make a nearby claim a denial, a hedge or a condition instead of an assertion
var (
	negationWords = map[string]bool{"nao": true, "nem": true, "nunca": true, "sem": true}
	hedgeWords    = map[string]bool{
		"nao": true, "nem": true, "nunca": true, "sem": true, "se": true, "caso": true, "quando": true,
		"confirmar": true, "confirma": true, "confirmo": true, "verificar": true, "verifica": true,
		"consultar": true, "consulta": true, "checar": true, "saber": true, "ver": true,
	}
)

// precededBy reports whether any of the up to n words right before pos is in set.
func precededBy(folded string, pos, n int, set map[string]bool) bool {
	words := strings.Fields(folded[:pos])
	if len(words) > n {
		words = words[len(words)-n:]
	}
	for _, w := range words {
		if set[strings.Trim(w, ".,;:!?()\"'")] {
			return true
		}
	}
	return false
}

// numberAllowed reports whether the number (as written, e.g. "25,99") appears in the allowed
// text, which is only what the organization configured plus what the customer said.
func numberAllowed(allowedFolded, number string) bool {
	return strings.Contains(allowedFolded, strings.TrimRight(number, ".,"))
}

// violatesCommercialSafety reports whether text states, as if confirmed, something the agent
// cannot know: a promise or reservation, a price, a discount, stock or availability, or a
// delivery deadline. Numbers that appear in allowed (the agent's configured text, the
// organization's AI contexts and knowledge, and the customer's own message) are not violations.
func violatesCommercialSafety(text, allowed string) bool {
	f := knowledge.FoldForSearch(text)
	a := knowledge.FoldForSearch(allowed)

	for _, m := range commitmentRE.FindAllStringIndex(f, -1) {
		if !precededBy(f, m[0], 3, negationWords) {
			return true
		}
	}
	for _, m := range moneyRE.FindAllStringSubmatch(f, -1) {
		n := m[1]
		if n == "" {
			n = m[2]
		}
		if !numberAllowed(a, n) {
			return true
		}
	}
	for _, m := range percentRE.FindAllStringSubmatchIndex(f, -1) {
		num := f[m[2]:m[3]]
		lo, hi := m[0]-30, m[1]+30
		if lo < 0 {
			lo = 0
		}
		if hi > len(f) {
			hi = len(f)
		}
		if discountRE.MatchString(f[lo:hi]) && !numberAllowed(a, num) {
			return true
		}
	}
	for _, m := range stockRE.FindAllStringIndex(f, -1) {
		if !precededBy(f, m[0], 6, hedgeWords) {
			return true
		}
	}
	for _, m := range deadlineRE.FindAllStringSubmatchIndex(f, -1) {
		if precededBy(f, m[0], 6, hedgeWords) {
			continue
		}
		whole := f[m[0]:m[1]]
		if digits := digitsOnlyRE.FindString(whole); digits != "" &&
			regexp.MustCompile(`\b`+digits+`\s*(dias?|horas?|semanas?)\b`).MatchString(a) {
			continue // the organization itself wrote that deadline
		}
		return true
	}
	return false
}
