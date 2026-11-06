package handlers

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// A small, dependency-free PDF writer for text reports: A4, the built-in Helvetica font (WinAnsi,
// so Portuguese accents work), word wrapping and page breaks. It exists so a case report does not
// need a PDF library; it is not a layout engine.

type pdfDoc struct {
	pages   [][]string // content-stream lines per page
	cur     []string
	y       float64
	title   string
	footer  string
	org     string // when set, pages carry the organization identity and a coloured top rule
	ref     string // reference code shown in the page header
	pageNum int
}

const (
	pdfW, pdfH  = 595.0, 842.0
	pdfMargin   = 48.0
	pdfLine     = 13.0
	pdfCharsMax = 98 // body font 9.5pt over the printable width, Helvetica averages ~0.5em
)

func newPDF(title, footer string) *pdfDoc {
	d := &pdfDoc{title: title, footer: footer}
	d.newPage()
	return d
}

func (d *pdfDoc) newPage() {
	if d.cur != nil {
		d.pages = append(d.pages, d.cur)
	}
	d.pageNum++
	d.cur = nil
	d.y = pdfH - pdfMargin
}

// winAnsi converts UTF-8 to the single-byte encoding the font uses; what it cannot draw becomes "?".
func winAnsi(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			out = append(out, ' ')
		case r >= 32 && r < 127:
			out = append(out, byte(r))
		case r >= 160 && r <= 255:
			out = append(out, byte(r))
		case r == '€':
			out = append(out, 0x80)
		case r == '“' || r == '”':
			out = append(out, '"')
		case r == '‘' || r == '’':
			out = append(out, '\'')
		case r == '–' || r == '—':
			out = append(out, '-')
		case r == '•':
			out = append(out, 0x95)
		case r == '…':
			out = append(out, 0x85)
		default:
			out = append(out, '?')
		}
	}
	return out
}

func pdfEscape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch c {
		case '(', ')', '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func (d *pdfDoc) put(text string, size float64, bold bool, indent float64) {
	if d.y < pdfMargin+20 {
		d.newPage()
	}
	font := "F1"
	if bold {
		font = "F2"
	}
	d.cur = append(d.cur, fmt.Sprintf("BT /%s %.1f Tf %.1f %.1f Td (%s) Tj ET", font, size, pdfMargin+indent, d.y, pdfEscape(winAnsi(text))))
	d.y -= size + 3.5
}

// wrap splits text into lines of at most n characters, breaking at spaces (and inside very long words).
func wrap(text string, n int) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			for utf8.RuneCountInString(w) > n {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				r := []rune(w)
				out = append(out, string(r[:n]))
				w = string(r[n:])
			}
			switch {
			case line == "":
				line = w
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= n:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		out = append(out, line)
	}
	return out
}

func (d *pdfDoc) heading(text string) {
	d.y -= 6
	d.put(text, 12, true, 0)
	d.y -= 2
}

func (d *pdfDoc) paragraph(text string, indent float64) {
	for _, l := range wrap(text, pdfCharsMax-int(indent/5)) {
		d.put(l, 9.5, false, indent)
	}
}

func (d *pdfDoc) kv(k, v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	d.put(k+": "+v, 9.5, false, 0)
}

func (d *pdfDoc) space(h float64) { d.y -= h }

// bytes renders the file. Each page carries the document title and "page n of m".
func (d *pdfDoc) bytes() []byte {
	pages := append(append([][]string{}, d.pages...), d.cur)
	var buf bytes.Buffer
	var offsets []int
	obj := func(body string) int {
		offsets = append(offsets, buf.Len())
		id := len(offsets)
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", id, body)
		return id
	}
	buf.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	// ids: 1 catalog, 2 pages, 3-4 fonts, 5 info; then per page i: content 6+2i, page 7+2i.
	pageIDs := make([]int, len(pages))
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 7+i*2)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	obj("<< /Producer (WhatC) >>")
	for i, lines := range pages {
		var header string
		if d.org != "" {
			pg := fmt.Sprintf("Página %d de %d", i+1, len(pages))
			header = fmt.Sprintf("%.3f %.3f %.3f rg 0 %.0f %.0f 6 re f\n", colAccent[0], colAccent[1], colAccent[2], pdfH-6, pdfW) +
				fmt.Sprintf("BT /F2 8 Tf 0.10 0.12 0.15 rg %.1f %.1f Td (%s) Tj ET\n", pdfMargin, pdfH-24, pdfEscape(winAnsi(d.org+"  |  Relatório de ocorrência"))) +
				fmt.Sprintf("BT /F1 8 Tf 0.42 0.45 0.50 rg %.1f %.1f Td (%s) Tj ET\n", pdfW-pdfMargin-textWidth(d.ref, 8, false), pdfH-24, pdfEscape(winAnsi(d.ref))) +
				fmt.Sprintf("BT /F1 7.5 Tf 0.42 0.45 0.50 rg %.1f %.1f Td (%s) Tj ET\n", pdfMargin, 24.0, pdfEscape(winAnsi(d.footer))) +
				fmt.Sprintf("BT /F1 7.5 Tf 0.42 0.45 0.50 rg %.1f %.1f Td (%s) Tj ET\n", pdfW-pdfMargin-textWidth(pg, 7.5, false), 24.0, pdfEscape(winAnsi(pg)))
		} else {
			header = fmt.Sprintf("BT /F2 8 Tf %.1f %.1f Td (%s) Tj ET\nBT /F1 8 Tf %.1f %.1f Td (%s) Tj ET\n",
				pdfMargin, pdfH-28, pdfEscape(winAnsi(d.title)),
				pdfMargin, 24.0, pdfEscape(winAnsi(fmt.Sprintf("%s   -   %d/%d", d.footer, i+1, len(pages)))))
		}
		stream := header + strings.Join(lines, "\n")
		contentID := obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
		pageIDs[i] = obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Contents %d 0 R /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> >>", pdfW, pdfH, contentID))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}
