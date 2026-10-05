package knowledge

import (
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

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// SearchForm is what is indexed and what is searched (the ONLY transformation
// shared by both): FoldForSearch, then the folded stop words removed.
//
// It reads the text the way websearch_to_tsquery does and keeps that syntax intact:
//
//	"a phrase"      a quoted phrase is one item; stop words inside it are dropped,
//	                and a phrase left empty disappears
//	-term, -"x y"   a "-" at the START of an item is an exclusion operator
//	or              a whole word "or" between items is the OR operator
//	foo-bar         a "-" between letters is part of the word (a compound): the
//	                compound is kept whole, stop-word parts included ("nao-conformidade"
//	                stays "nao-conformidade"); it never turns into "-conformidade"
//
// A stop word is removed only when it stands alone. Whatever a removal leaves
// behind (an empty item, a lone "-", an "or" with nothing on one side) is dropped.
// An unterminated quote is ignored. Items are joined by single spaces.
func SearchForm(s string) string {
	rs := []rune(FoldForSearch(s))
	var items []string
	for i := 0; i < len(rs); {
		switch c := rs[i]; {
		case unicode.IsSpace(c):
			i++
		case c == '"':
			end := indexRune(rs, '"', i+1)
			if end < 0 {
				i++ // unterminated quote: ignore the stray character
				continue
			}
			items = appendPhrase(items, false, rs[i+1:end])
			i = end + 1
		case c == '-' && i+1 < len(rs) && (rs[i+1] == '"' || isWordRune(rs[i+1])):
			i++ // exclusion operator: it opens an item
			if rs[i] == '"' {
				end := indexRune(rs, '"', i+1)
				if end < 0 {
					i++
					continue
				}
				items = appendPhrase(items, true, rs[i+1:end])
				i = end + 1
				continue
			}
			j := termEnd(rs, i)
			items = appendTerm(items, true, rs[i:j])
			i = j
		case c == '-':
			i++ // a lone hyphen is neither an operator nor a word
		default:
			j := termEnd(rs, i)
			items = appendTerm(items, false, rs[i:j])
			i = j
		}
	}
	return joinItems(items)
}

func indexRune(rs []rune, r rune, from int) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// termEnd is the end of the bare term starting at i: up to whitespace or a quote.
func termEnd(rs []rune, i int) int {
	for i < len(rs) && !unicode.IsSpace(rs[i]) && rs[i] != '"' {
		i++
	}
	return i
}

func hasWordRune(s string) bool { return strings.IndexFunc(s, isWordRune) >= 0 }

// dropStopwords removes the stop words that stand alone in term. A word joined to
// another by a hyphen ("nao-conformidade", "so-leitura") is part of a compound and is kept.
func dropStopwords(term []rune) string {
	var b strings.Builder
	for i := 0; i < len(term); {
		if !isWordRune(term[i]) {
			b.WriteRune(term[i])
			i++
			continue
		}
		j := i
		for j < len(term) && isWordRune(term[j]) {
			j++
		}
		compound := (i >= 2 && term[i-1] == '-' && isWordRune(term[i-2])) ||
			(j+1 < len(term) && term[j] == '-' && isWordRune(term[j+1]))
		if compound || !foldedStopwords[string(term[i:j])] {
			b.WriteString(string(term[i:j]))
		}
		i = j
	}
	return b.String()
}

func appendTerm(items []string, neg bool, term []rune) []string {
	t := dropStopwords(term)
	if !hasWordRune(t) {
		return items // nothing left but punctuation
	}
	if neg {
		return append(items, "-"+t)
	}
	return append(items, t) // a plain "or" stays: joinItems treats it as the operator
}

func appendPhrase(items []string, neg bool, content []rune) []string {
	var words []string
	for _, w := range strings.Fields(string(content)) {
		if t := dropStopwords([]rune(w)); hasWordRune(t) {
			words = append(words, t)
		}
	}
	if len(words) == 0 {
		return items
	}
	p := `"` + strings.Join(words, " ") + `"`
	if neg {
		p = "-" + p
	}
	return append(items, p)
}

// joinItems joins the items and drops an "or" that has nothing on one side
// (at the start, at the end, or right after another "or").
func joinItems(items []string) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it == "or" && (len(out) == 0 || out[len(out)-1] == "or") {
			continue
		}
		out = append(out, it)
	}
	for len(out) > 0 && out[len(out)-1] == "or" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, " ")
}

// AccentedStopwords returns the list, for the test that checks it against PostgreSQL.
func AccentedStopwords() []string { return append([]string(nil), accentedStopwords...) }

// unaccentedStopwords are the entries of PostgreSQL's Portuguese stop list that carry no
// diacritic. SearchForm leaves them to PostgreSQL (it drops them itself, and keeping them
// in the indexed text keeps phrase positions identical to a plain to_tsvector), but PlainTerms
// must drop them too: otherwise a customer message full of "para", "da", "no" would count as
// having enough terms and would use up the 12-term cap.
// TestStopwords_AgreeWithPostgres checks every entry against the database.
var unaccentedStopwords = strings.Fields(`a ao aos aquela aquelas aquele aqueles aquilo as com como da das de dela delas dele deles
depois do dos e ela elas ele eles em entre era eram essa essas esse esses esta estamos estas estava estavam este esteja estejam
estejamos estes esteve estive estivemos estiver estivera estiveram estiverem estivermos estivesse estivessem estou eu foi fomos for
fora foram forem formos fosse fossem fui haja hajam hajamos havemos hei houve houvemos houver houvera houveram houverei houverem
houveremos houveria houveriam houvermos houvesse houvessem isso isto lhe lhes mais mas me mesmo meu meus minha minhas muito na nas
nem no nos nossa nossas nosso nossos num numa o os ou para pela pelas pelo pelos por qual quando que quem se seja sejam sejamos sem
serei seremos seria seriam seu seus somos sou sua suas te tem temos tenha tenham tenhamos tenho terei teremos teria teriam teu teus
teve tinha tinham tive tivemos tiver tivera tiveram tiverem tivermos tivesse tivessem tu tua tuas um uma vos`)

// plainStopwords is every Portuguese stop word, folded: what PlainTerms drops.
var plainStopwords = func() map[string]bool {
	m := make(map[string]bool, len(foldedStopwords)+len(unaccentedStopwords))
	for w := range foldedStopwords {
		m[w] = true
	}
	for _, w := range unaccentedStopwords {
		m[FoldForSearch(w)] = true
	}
	return m
}()

// UnaccentedStopwords returns the list, for the test that checks it against PostgreSQL.
func UnaccentedStopwords() []string { return append([]string(nil), unaccentedStopwords...) }
