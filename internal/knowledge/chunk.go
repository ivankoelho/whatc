package knowledge

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Chunk size is a code constant on purpose (no config until a measurement asks for it).
const (
	chunkMax     = 1000 // characters per chunk, overlap included
	chunkOverlap = 100  // tail of the previous chunk repeated at the start of the next
	chunkMin     = 40   // sections shorter than this carry no useful knowledge
)

// Section is a titled block of text, the input of Chunk.
type Section struct {
	Heading string
	Text    string
}

// Piece is one chunk ready to be stored.
type Piece struct {
	Index   int
	Heading string
	Content string
}

// Chunk splits sections into pieces of at most chunkMax characters: by section
// first, then by line/sentence, never in the middle of a word, with a small
// overlap between consecutive pieces of the same section. Output is
// deterministic, so re-running on the same text gives the same chunks.
func Chunk(sections []Section) []Piece {
	var out []Piece
	for _, s := range sections {
		text := NormalizeText(s.Text)
		if utf8.RuneCountInString(text) < 1 {
			continue
		}
		for _, c := range splitText(text) {
			out = append(out, Piece{Index: len(out), Heading: s.Heading, Content: c})
		}
	}
	return out
}

func splitText(text string) []string {
	if utf8.RuneCountInString(text) <= chunkMax {
		return []string{text}
	}
	// Break into units (lines; over-long lines at sentence/space boundaries).
	var units []string
	for _, line := range strings.Split(text, "\n") {
		units = append(units, breakLong(line, chunkMax-chunkOverlap-2)...)
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	for _, u := range units {
		if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+1+utf8.RuneCountInString(u) > chunkMax {
			prev := cur.String()
			flush()
			cur.WriteString(overlapTail(prev))
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(u)
	}
	flush()
	return chunks
}

// breakLong cuts a line longer than max at the last sentence end (or space)
// before max; a single word longer than max is kept whole.
func breakLong(line string, max int) []string {
	var parts []string
	r := []rune(line)
	for len(r) > max {
		cut := lastBoundary(r[:max])
		if cut == max { // no break point before max: keep the word whole, cut after it
			cut = len(r)
			for i := max; i < len(r); i++ {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
		}
		parts = append(parts, strings.TrimSpace(string(r[:cut])))
		r = []rune(strings.TrimSpace(string(r[cut:])))
	}
	if len(r) > 0 {
		parts = append(parts, string(r))
	}
	return parts
}

func lastBoundary(r []rune) int {
	for i := len(r) - 1; i > len(r)/2; i-- { // prefer a sentence end in the back half
		if (r[i] == '.' || r[i] == '!' || r[i] == '?') && i+1 < len(r) && r[i+1] == ' ' {
			return i + 1
		}
	}
	for i := len(r) - 1; i > 0; i-- {
		if r[i] == ' ' {
			return i
		}
	}
	return len(r) // one huge word: keep it whole
}

// overlapTail returns up to chunkOverlap characters from the end of prev,
// starting at a word boundary.
func overlapTail(prev string) string {
	r := []rune(prev)
	if len(r) <= chunkOverlap {
		return ""
	}
	tail := string(r[len(r)-chunkOverlap:])
	if i := strings.IndexAny(tail, " \n"); i >= 0 {
		tail = tail[i+1:]
	}
	return strings.TrimSpace(tail)
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdEmph    = regexp.MustCompile(`(\*\*|__|\*|` + "`" + `)`)
)

// ParseMarkdown turns Markdown into sections, one per heading, with the trail
// of parent headings as the section heading ("Processo > Etapa 2"). Text before
// the first heading uses fallbackHeading (the document title). Markup is
// stripped; list markers and code stay as plain text.
func ParseMarkdown(src, fallbackHeading string) []Section {
	var (
		sections []Section
		trail    []string
		buf      []string
	)
	cur := fallbackHeading
	flush := func() {
		if t := strings.TrimSpace(strings.Join(buf, "\n")); t != "" {
			sections = append(sections, Section{Heading: cur, Text: t})
		}
		buf = nil
	}
	inFence := false
	for _, line := range strings.Split(NormalizeText(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if m := mdHeading.FindStringSubmatch(line); m != nil && !inFence {
			flush()
			level := len(m[1])
			if level-1 < len(trail) {
				trail = trail[:level-1]
			}
			for len(trail) < level-1 {
				trail = append(trail, "")
			}
			trail = append(trail, cleanInline(m[2]))
			cur = joinTrail(trail, fallbackHeading)
			continue
		}
		buf = append(buf, cleanInline(line))
	}
	flush()
	return sections
}

func cleanInline(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	return strings.TrimSpace(mdEmph.ReplaceAllString(s, ""))
}

func joinTrail(trail []string, fallback string) string {
	var parts []string
	for _, t := range trail {
		if t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, " > ")
}
