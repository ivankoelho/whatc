package knowledge

import (
	"regexp"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// ManualDoc is one importable section of an HTML manual.
type ManualDoc struct {
	Title  string
	Anchor string // id of the heading (or of its enclosing <section>); never empty
	Text   string
}

// skipped entirely: the manuals embed ~1.7 MB of base64 images and inline scripts,
// none of which is knowledge.
var skipTags = map[string]bool{
	"script": true, "style": true, "img": true, "svg": true, "head": true,
	"nav": true, "noscript": true, "iframe": true, "button": true, "template": true,
}

var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "ul": true, "ol": true,
	"li": true, "tr": true, "table": true, "pre": true, "blockquote": true, "br": true,
	"dl": true, "dt": true, "dd": true, "header": true, "footer": true, "main": true,
	"h1": true, "h2": true, "h3": true, "h4": true,
}

// ParseManualHTML splits an HTML manual into one document per h2/h3 section.
// The text before a heading belongs to the previous section; sections with
// almost no text are dropped. The anchor comes from the heading's own id, else
// the nearest enclosing element's id (the manuals put ids on <section>), else a
// stable positional fallback.
func ParseManualHTML(r io.Reader) ([]ManualDoc, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	w := &htmlWalker{used: map[string]bool{}}
	w.walk(root, "")
	w.closeSection()

	var docs []ManualDoc
	for _, d := range w.docs {
		if utf8.RuneCountInString(d.Text) >= chunkMin {
			docs = append(docs, d)
		}
	}
	return docs, nil
}

type htmlWalker struct {
	docs   []ManualDoc
	title  string
	anchor string
	buf    strings.Builder
	used   map[string]bool // anchors already given in this file
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugOf turns a heading into an anchor: folded, lowercase, hyphenated.
func slugOf(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(FoldForSearch(title), "-"), "-")
	if r := []rune(s); len(r) > 60 {
		s = strings.Trim(string(r[:60]), "-")
	}
	if s == "" {
		return "secao"
	}
	return s
}

// pickAnchor chooses the origin anchor of a section, stable when other sections
// are inserted or removed: the heading's own id, else the nearest ancestor id
// (unless an earlier section of the file already took it, as when one <section>
// holds several headings), else the slug of the heading text. Only when every
// candidate is taken does a numeric suffix apply, counted among the duplicates of
// that same anchor and not by position in the file.
func (w *htmlWalker) pickAnchor(own, ancestor, title string) string {
	candidates := []string{own, ancestor, slugOf(title)}
	for _, c := range candidates {
		if c != "" && !w.used[c] {
			w.used[c] = true
			return c
		}
	}
	base := firstNonEmpty(own, slugOf(title))
	for n := 2; ; n++ {
		if c := fmt.Sprintf("%s-%d", base, n); !w.used[c] {
			w.used[c] = true
			return c
		}
	}
}

func (w *htmlWalker) closeSection() {
	text := NormalizeText(w.buf.String())
	w.buf.Reset()
	if w.title == "" && text == "" {
		return
	}
	title := w.title
	if title == "" {
		title = "Introdução"
	}
	anchor := w.anchor
	if anchor == "" { // text before the first heading
		anchor = w.pickAnchor("", "", "introducao")
	}
	w.docs = append(w.docs, ManualDoc{Title: title, Anchor: anchor, Text: text})
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func (w *htmlWalker) walk(n *html.Node, ctxID string) {
	switch n.Type {
	case html.TextNode:
		w.buf.WriteString(n.Data)
		return
	case html.ElementNode:
		if skipTags[n.Data] {
			return
		}
		if n.Data == "h2" || n.Data == "h3" {
			w.closeSection()
			w.title = NormalizeText(innerText(n))
			w.anchor = w.pickAnchor(attr(n, "id"), ctxID, w.title)
			return // the heading text is the title, not part of the body
		}
		if id := attr(n, "id"); id != "" {
			ctxID = id
		}
		block := blockTags[n.Data]
		cell := n.Data == "td" || n.Data == "th"
		if block {
			w.buf.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c, ctxID)
		}
		if block {
			w.buf.WriteString("\n")
		}
		if cell {
			w.buf.WriteString(" | ")
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, ctxID)
	}
}

func innerText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		if x.Type == html.ElementNode && skipTags[x.Data] {
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
