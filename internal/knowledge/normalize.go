// Package knowledge is the Fase 8A knowledge base core: text normalization,
// chunking, import of the HTML manuals, and the Retriever contract with its
// PostgreSQL full-text implementation. It does not depend on any LLM provider
// or on AIContext: retrieval works (and is tested) without spending tokens.
package knowledge

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// FoldForSearch is THE accent/case normalization for search. The same function
// feeds the indexed copy of every chunk and every query, so "Orçamento",
// "orcamento" and "ORÇAMENTO" meet. The text shown to users (title, heading,
// content) is never folded. Punctuation is kept on purpose: websearch syntax
// ("exact phrase", -excluded) must survive.
func FoldForSearch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// NormalizeText cleans text for storage and chunking: LF line breaks, no
// control characters, no repeated spaces, at most one blank line in a row.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t' || r == '\u00a0':
			return ' '
		case unicode.IsControl(r) || r == '\ufeff' || r == '\u200b':
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
