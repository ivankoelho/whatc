package knowledge

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxPlainTerms = 12  // terms kept from a customer message
	maxQueryRunes = 500 // the whole composed query
	shortQuery    = 3   // fewer terms than this: the previous customer message is added
)

// PlainTerms reads free text typed by a customer as TERMS, never as syntax: a quote, a "-"
// at the start of a word or the word "or" typed in a message are only punctuation or a word,
// they cannot exclude anything or change the query. The text is accent-folded like the index;
// a hyphen between letters keeps the compound whole ("nao-conformidade"), as SearchForm does;
// stop words, one-character terms and repeats are dropped; at most 12 terms are kept.
func PlainTerms(text string) []string {
	rs := []rune(FoldForSearch(text))
	var terms []string
	seen := map[string]bool{}
	for i := 0; i < len(rs) && len(terms) < maxPlainTerms; {
		if !isWordRune(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && (isWordRune(rs[j]) || (rs[j] == '-' && j > i && j+1 < len(rs) && isWordRune(rs[j+1]))) {
			j++
		}
		tok := string(rs[i:j])
		i = j
		compound := strings.Contains(tok, "-")
		switch {
		case utf8.RuneCountInString(tok) < 2, tok == "or", !compound && plainStopwords[tok], seen[tok]:
			continue
		}
		seen[tok] = true
		terms = append(terms, tok)
	}
	return terms
}

// ComposeQuery builds the text a customer message is searched with. The CURRENT message is
// the main part; when it has fewer than 3 meaningful terms ("e o prazo?"), the previous
// customer message (never an agent's or the AI's; the caller picks it) is appended so the
// question keeps its subject. The 500-character limit applies AFTER the composition.
func ComposeQuery(current, previous string) string {
	text := strings.TrimSpace(current)
	if prev := strings.TrimSpace(previous); prev != "" && len(PlainTerms(text)) < shortQuery {
		text = strings.TrimSpace(text + " " + prev)
	}
	if r := []rune(text); len(r) > maxQueryRunes {
		text = strings.TrimFunc(string(r[:maxQueryRunes]), unicode.IsSpace)
	}
	return text
}
