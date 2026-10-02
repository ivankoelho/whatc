package knowledge

import (
	"regexp"
	"strings"
	"unicode"
)

// IndexVersion is the version of the INDEXING STRATEGY (what is stored in
// search_heading/search_text and how the query is prepared), not a document
// revision. Editing a document rebuilds its chunks at the current version; only a
// change of the strategy raises this number, which makes older chunks "stale"
// until `knowledge reindex` rebuilds them.
//
//	1 = accent folding only (8A)
//	2 = accent folding + removal of accented stop words (8B-1, M1)
const IndexVersion = 2

// accentedStopwords are the words of the Portuguese stop list (Snowball, the list
// PostgreSQL's 'portuguese' configuration uses) that carry a diacritic. PostgreSQL
// drops them from accented text, but our search copy is folded BEFORE PostgreSQL
// sees it ("não" becomes "nao"), so they would be indexed as real terms. They are
// removed here, symmetrically from the indexed copy and from the query.
// TestStopwords_AgreeWithPostgres checks every entry against the database.
var accentedStopwords = []string{
	"à", "às", "até", "éramos", "está", "estão", "estávamos", "estivéramos", "estivéssemos",
	"fôramos", "fôssemos", "há", "hão", "houverá", "houvéramos", "houverão", "houveríamos", "houvéssemos",
	"já", "não", "nós", "são", "será", "serão", "seríamos", "só", "também", "tém", "terá", "terão",
	"teríamos", "tínhamos", "tivéramos", "tivéssemos", "você", "vocês",
}

var foldedStopwords = func() map[string]bool {
	m := make(map[string]bool, len(accentedStopwords))
	for _, w := range accentedStopwords {
		m[FoldForSearch(w)] = true
	}
	return m
}()

var (
	emptyQuotes   = regexp.MustCompile(`-?"\s*"`)
	quoteInner    = regexp.MustCompile(`(^|\s)"\s+`) // opening quote followed by spaces (a removed word)
	quoteInnerEnd = regexp.MustCompile(`\s+"(\s|$)`) // spaces before a closing quote
)

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// SearchForm is what is indexed and what is searched: FoldForSearch, then the
// folded stop words removed as WHOLE WORDS. Everything that is not a word (quotes,
// "-", spaces, punctuation) is kept as is, so websearch syntax keeps its structure;
// the remnants a removal can leave (empty quotes, a lone "-", a dangling "or") are
// cleaned up.
func SearchForm(s string) string {
	rs := []rune(FoldForSearch(s))
	var b strings.Builder
	for i := 0; i < len(rs); {
		if !isWordRune(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && isWordRune(rs[j]) {
			j++
		}
		if w := string(rs[i:j]); !foldedStopwords[w] {
			b.WriteString(w)
		}
		i = j
	}
	return tidySyntax(b.String())
}

// tidySyntax repairs what removing a word can leave behind: empty quotes (with an
// optional "-" in front), an operator "-" with no word, and "or" at the start, at
// the end or next to another "or".
func tidySyntax(s string) string {
	for {
		t := emptyQuotes.ReplaceAllString(s, " ")
		if t == s {
			break
		}
		s = t
	}
	s = quoteInner.ReplaceAllString(s, `$1"`)
	s = quoteInnerEnd.ReplaceAllString(s, `"$1`)
	words := strings.Fields(s)
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "-" {
			continue
		}
		if w == "or" && (len(out) == 0 || out[len(out)-1] == "or") {
			continue
		}
		out = append(out, w)
	}
	for len(out) > 0 && out[len(out)-1] == "or" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, " ")
}

// AccentedStopwords returns the list, for the test that checks it against PostgreSQL.
func AccentedStopwords() []string { return append([]string(nil), accentedStopwords...) }
