package handlers

import "strings"

// Reading the words a customer uses for a product, a category, an environment or an intent.

// textNegators are words that take back a thing the customer named ("não quero porcelanato").
var textNegators = map[string]bool{"nao": true, "nem": true, "nunca": true, "sem": true}

// tokenIs reports whether tok is the word w (or its plural): whole words only, so "sala" is not
// found inside "salada".
func tokenIs(tok, w string) bool { return tok == w || tok == w+"s" }

// negatedAt reports whether the word at tokens[i] is taken back: a negator up to three words
// before it ("não quero porcelanato") or right after it ("porcelanato não, laminado").
func negatedAt(tokens []string, i int) bool {
	for j := i - 3; j < i; j++ {
		if j >= 0 && textNegators[tokens[j]] {
			return true
		}
	}
	return i+1 < len(tokens) && tokens[i+1] == "nao"
}

// validateTextField accepts value only if the quoted evidence is the customer's own words, every
// word of the value is a whole word of that quote, and, in the customer's WHOLE message (not just
// the part the model chose to quote), the term is not taken back before or after.
func validateTextField(value, evidence string, customerMessages []string) bool {
	words := strings.Fields(compact(value))
	e := compact(evidence)
	if len(words) == 0 || !evidenceFromCustomer(evidence, customerMessages) {
		return false
	}
	quote := strings.Fields(e)
	for _, w := range words {
		found := false
		for _, tok := range quote {
			if tokenIs(tok, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, m := range customerMessages {
		cm := compact(m)
		if !strings.Contains(cm, e) {
			continue
		}
		tokens := strings.Fields(cm)
		for i, tok := range tokens {
			if tokenIs(tok, words[0]) && !negatedAt(tokens, i) {
				return true
			}
		}
	}
	return false
}

// A stated intent needs words that really are about buying or asking for a price in the quote:
// the model cannot turn "bom dia" into a purchase intent.
var (
	buyWords    = []string{"comprar", "compra", "quero", "preciso", "fechar", "levar", "pedido", "adquirir", "encomendar"}
	budgetWords = []string{"orcamento", "cotacao", "preco", "valor", "quanto", "custa", "custo"}
)

func quoteHasAny(evidence string, words []string) bool {
	for _, tok := range strings.Fields(compact(evidence)) {
		for _, w := range words {
			if tokenIs(tok, w) {
				return true
			}
		}
	}
	return false
}

// validIntent maps the model's intent to a known one, only when the customer's quote supports it.
func validIntent(value, evidence string) string {
	switch compact(value) {
	case "comprar", "compra":
		if quoteHasAny(evidence, buyWords) {
			return "comprar"
		}
	case "orcamento":
		if quoteHasAny(evidence, budgetWords) {
			return "orçamento"
		}
	}
	return ""
}
