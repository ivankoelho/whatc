package handlers

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/shridarpatil/whatomate/internal/knowledge"
)

// Reading the quantities a customer states ("40 m²", "10 caixas"). The rule behind all of it: a
// value is accepted only if it is the ONE value the customer stated for that kind of quantity, in
// their own unit. Anything that makes the statement unclear leaves the field "not informed".

type numericKind int

const (
	kindArea  numericKind = iota // square metres
	kindCount                    // boxes, pieces, packs...
)

const (
	areaUnitPattern = `(m²|m2|mt²|mts²|metros? quadrados?|mts? quadrados?)`
	// "metros" alone is deliberately NOT a count unit: in flooring "40 metros de piso" usually means
	// square metres, and recording it as linear metres would be a wrong value.
	countUnitPattern = `(caixas?|cx|pecas?|pacotes?|pct|unidades?|un|sacos?)`
	numberPattern    = `\d[\d.,]*`
	rangeConnector   = `(?:\s*(?:ou|a|ate|e|/|-)\s*(` + numberPattern + `))?`
)

// mention = NUMBER [(ou|a|ate|e|/|-) NUMBER] UNIT, then any non-letter. "40 ou 50 m²" has two
// numbers: the customer did not say one value. Group 3 is the unit, taken from the customer's own
// words, never from the model.
var (
	areaMentionRE  = regexp.MustCompile(`(` + numberPattern + `)` + rangeConnector + `\s*` + areaUnitPattern + `(?:$|[^a-z0-9])`)
	countMentionRE = regexp.MustCompile(`(` + numberPattern + `)` + rangeConnector + `\s*` + countUnitPattern + `(?:$|[^a-z0-9])`)
	numberRE       = regexp.MustCompile(numberPattern)

	// a package unit right before the number ("caixa de 2,5 m²") or right after the unit
	// ("2,5 m² por caixa", "2,5 m² cada"): that area belongs to the package, not to the order
	packageBeforeRE = regexp.MustCompile(`(caixas?|cx|pacotes?|pct|pecas?|sacos?)(\s+(de|com|tem|tendo))?\s*$`)
	packageAfterRE  = regexp.MustCompile(`^\s*(por|a|de|em|na|no|cada)\s*(caixa|cx|pacote|peca|saco)|^\s*cada\b`)
	// "40x50 m²" is width x length, not an area
	dimensionBeforeRE = regexp.MustCompile(`\d\s*x\s*$`)

	// a correction in the same message: a value stated before it cannot be trusted
	correctionMarkers = []string{"errei", "na verdade", "corrigindo", "correcao", "quer dizer", "melhor dizendo", "desculpa"}

	// words right before a number that take it back, make it a bound or make it somebody else's
	skipBefore = map[string]bool{
		"nao": true, "nem": true, "nunca": true, "sem": true, "menos": true, "ate": true, "maximo": true, "minimo": true,
		"acima": true, "abaixo": true, "metade": true, "terco": true, "dobro": true, "vizinho": true, "vizinha": true,
		"amigo": true, "amiga": true, "comprei": true, "ontem": true, "passado": true, "tinha": true,
	}
	// "40 m² mais 10 m²" is a sum: neither a correction nor one single value
	sumBefore       = map[string]bool{"mais": true}
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

func replacedBefore(folded string, pos int) bool {
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

// isCorrectionCue reports whether a message reads as a correction: it opens with "não" or contains
// a correction marker.
func isCorrectionCue(folded string) bool {
	if strings.HasPrefix(strings.TrimLeft(folded, " ,;."), "nao") {
		return true
	}
	for _, mk := range correctionMarkers {
		if strings.Contains(folded, mk) {
			return true
		}
	}
	return false
}

// mention is one quantity the customer stated, with the unit as the customer wrote it.
type mention struct {
	value float64
	unit  string
}

// mentionSet is everything the customer stated for one kind of quantity. ambiguous means that it
// cannot be reduced to one value with certainty (a range, a sum, a correction whose new value has
// no unit...): the field must then stay "not informed".
type mentionSet struct {
	items     []mention
	ambiguous bool
}

func (m *mentionSet) add(v float64, unit string) {
	for _, x := range m.items {
		if math.Abs(x.value-v) < 1e-9 {
			return
		}
	}
	m.items = append(m.items, mention{value: v, unit: unit})
}

// correctedWithoutUnit reports the form "20 m² não, 40": after the mention comes a "não" and then
// a number that is not itself a mention (it has no unit), so the new value cannot be read.
func correctedWithoutUnit(after string, end int, mentionStarts map[int]bool) bool {
	rest := strings.TrimLeft(after, " ,;.")
	if !strings.HasPrefix(rest, "nao") {
		return false
	}
	next := strings.TrimLeft(strings.TrimPrefix(rest, "nao"), " ,;.")
	if next == "" || next[0] < '0' || next[0] > '9' {
		return false
	}
	pos := end + (len(after) - len(next))
	return !mentionStarts[pos]
}

// numericMentions reads every message of the customer for quantities of kind, dropping the ones
// that are not a statement about the order (taken back, replaced, a bound, somebody else's, a
// package's own area, a dimension) and flagging what is ambiguous.
func numericMentions(kind numericKind, customerMessages []string) mentionSet {
	var set mentionSet
	for _, msg := range customerMessages {
		f := knowledge.FoldForSearch(msg)
		re := areaMentionRE
		if kind == kindCount {
			f = areaMentionRE.ReplaceAllString(f, " ") // "40 metros quadrados" is an area, not a count
			re = countMentionRE
		}
		matches := re.FindAllStringSubmatchIndex(f, -1)
		if len(matches) == 0 {
			// "20 m²" in one message and "não, 40" / "errei, é 40" in the next: a correction whose new
			// value has no unit. It cannot be read, and the earlier value can no longer be trusted.
			if len(set.items) > 0 && numberRE.MatchString(f) && isCorrectionCue(f) {
				set.ambiguous = true
			}
			continue
		}
		for _, mk := range correctionMarkers {
			if strings.Contains(f, mk) { // the customer is correcting something in this very message
				set.ambiguous = true
			}
		}
		starts := map[int]bool{}
		for _, m := range matches {
			starts[m[0]] = true
		}
		for _, m := range matches {
			start, end := m[0], m[7] // end of the unit: the punctuation after it belongs to the next clause
			before, after := f[:start], f[end:]
			if precededBy(f, start, 4, skipBefore) || replacedBefore(f, start) ||
				packageBeforeRE.MatchString(strings.TrimSpace(before)) ||
				dimensionBeforeRE.MatchString(before) || packageAfterRE.MatchString(after) {
				continue
			}
			if precededBy(f, start, 2, sumBefore) {
				set.ambiguous = true
				continue
			}
			if correctedWithoutUnit(after, end, starts) {
				set.ambiguous = true
			}
			if m[4] >= 0 { // a range or a choice
				set.ambiguous = true
			}
			unit := f[m[6]:m[7]]
			for g := 1; g <= 2; g++ {
				if m[2*g] < 0 {
					continue
				}
				if v, ok := parsePTNumber(f[m[2*g]:m[2*g+1]]); ok && v > 0 {
					set.add(v, unit)
				}
			}
		}
	}
	return set
}

// acceptedNumeric accepts value only if, among everything the customer stated for this kind of
// quantity (negated, replaced or foreign values removed), it is the one and only value and nothing
// ambiguous was said. The unit returned is the one the customer wrote. "40 m², não 20 m²" gives 40;
// "40 m² ou 50 m²" gives nothing; "20 m² não, 40" gives nothing; "40 caixas" is never an area.
func acceptedNumeric(kind numericKind, value float64, customerMessages []string) (unit string, ok bool) {
	set := numericMentions(kind, customerMessages)
	if set.ambiguous || len(set.items) != 1 || math.Abs(set.items[0].value-value) > 1e-9 {
		return "", false
	}
	return set.items[0].unit, true
}

// validateNumericField is acceptedNumeric without the unit.
func validateNumericField(kind numericKind, value float64, customerMessages []string) bool {
	_, ok := acceptedNumeric(kind, value, customerMessages)
	return ok
}

// evidenceHasNumber reports whether the quoted evidence contains the number itself, so the quote
// is really about that quantity.
func evidenceHasNumber(evidence string, value float64) bool {
	for _, tok := range numberRE.FindAllString(knowledge.FoldForSearch(evidence), -1) {
		if v, ok := parsePTNumber(tok); ok && math.Abs(v-value) < 1e-9 {
			return true
		}
	}
	return false
}
