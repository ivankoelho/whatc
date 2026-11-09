package handlers

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Drawing helpers for the case report on top of the small PDF writer: colour, rectangles, measured text
// wrapping, a label/value grid, tables with a repeated header, and timeline blocks. Everything is drawn from
// data the system records; nothing here makes claims about the document itself.

type rgb [3]float64

var (
	colInk    = rgb{0.10, 0.12, 0.15}
	colMuted  = rgb{0.42, 0.45, 0.50}
	colAccent = rgb{0.05, 0.50, 0.36}
	colBand   = rgb{0.94, 0.96, 0.95}
	colLine   = rgb{0.82, 0.85, 0.84}
	colWarn   = rgb{0.72, 0.16, 0.16}
	colWhite  = rgb{1, 1, 1}
)

const (
	repLeft   = pdfMargin
	repWidth  = pdfW - 2*pdfMargin
	repBottom = pdfMargin + 14
)

// Helvetica advance widths (1/1000 em) for ASCII 32..126; accented Latin letters are close to their base.
var helvWidths = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

func textWidth(s string, size float64, bold bool) float64 {
	w := 0
	for _, r := range s {
		switch {
		case r >= 32 && r < 127:
			w += helvWidths[r-32]
		default:
			w += 600
		}
	}
	f := float64(w) * size / 1000
	if bold {
		f *= 1.07
	}
	return f * 1.01 // a hair of safety so a line never touches the neighbouring column
}

// wrapW breaks text into lines that fit maxW points at the given size (long words are split).
func wrapW(text string, maxW, size float64, bold bool) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		flush := func() {
			out = append(out, line)
			line = ""
		}
		for _, w := range words {
			for textWidth(w, size, bold) > maxW {
				r := []rune(w)
				n := len(r)
				for n > 1 && textWidth(string(r[:n]), size, bold) > maxW {
					n--
				}
				if line != "" {
					flush()
				}
				out = append(out, string(r[:n]))
				w = string(r[n:])
			}
			switch {
			case line == "":
				line = w
			case textWidth(line+" "+w, size, bold) <= maxW:
				line += " " + w
			default:
				flush()
				line = w
			}
		}
		out = append(out, line)
	}
	return out
}

func (d *pdfDoc) ensure(h float64) {
	if d.y-h < repBottom {
		d.newPage()
	}
}

func (d *pdfDoc) rect(x, y, w, h float64, c rgb) {
	d.cur = append(d.cur, fmt.Sprintf("%.3f %.3f %.3f rg %.1f %.1f %.1f %.1f re f", c[0], c[1], c[2], x, y, w, h))
}

func (d *pdfDoc) hline(x, y, w float64, c rgb) {
	d.rect(x, y, w, 0.6, c)
}

func (d *pdfDoc) textAt(x, y float64, s string, size float64, bold bool, c rgb) {
	font := "F1"
	if bold {
		font = "F2"
	}
	d.cur = append(d.cur, fmt.Sprintf("BT /%s %.1f Tf %.3f %.3f %.3f rg %.1f %.1f Td (%s) Tj ET", font, size, c[0], c[1], c[2], x, y, pdfEscape(winAnsi(s))))
}

func (d *pdfDoc) textRight(xRight, y float64, s string, size float64, bold bool, c rgb) {
	d.textAt(xRight-textWidth(s, size, bold), y, s, size, bold, c)
}

// block draws wrapped text at x within width w, line by line (so a long text flows over page breaks).
func (d *pdfDoc) block(x, w float64, text string, size float64, bold bool, c rgb) {
	lh := size + 3.2
	for _, l := range wrapW(text, w, size, bold) {
		d.ensure(lh)
		d.textAt(x, d.y-size, l, size, bold, c)
		d.y -= lh
	}
}

// section draws the numbered section bar with its subtitle.
func (d *pdfDoc) section(num, title, subtitle string) {
	d.ensure(54)
	d.y -= 10
	d.rect(repLeft, d.y-20, repWidth, 20, colBand)
	d.rect(repLeft, d.y-20, 3, 20, colAccent)
	d.textAt(repLeft+10, d.y-14, num+"  "+strings.ToUpper(title), 10, true, colInk)
	d.textRight(repLeft+repWidth-8, d.y-14, "SEÇÃO "+num, 7.5, true, colMuted)
	d.y -= 24
	if subtitle != "" {
		d.textAt(repLeft+10, d.y-9, subtitle, 8.5, false, colMuted)
		d.y -= 18
	}
}

type reportCell struct{ label, value string }

// grid lays label/value pairs out in two columns; a value that does not fit wraps inside its cell.
func (d *pdfDoc) grid(cells []reportCell) {
	colW := repWidth/2 - 10
	for i := 0; i < len(cells); i += 2 {
		row := cells[i:min(i+2, len(cells))]
		lines := make([][]string, len(row))
		maxLines := 1
		for j, c := range row {
			v := c.value
			if strings.TrimSpace(v) == "" {
				v = "—"
			}
			lines[j] = wrapW(v, colW, 9.5, false)
			maxLines = max(maxLines, len(lines[j]))
		}
		h := 11 + float64(maxLines)*12.5 + 4
		d.ensure(h)
		for j, c := range row {
			x := repLeft + 10 + float64(j)*(colW+20)
			d.textAt(x, d.y-8, strings.ToUpper(c.label), 7, true, colMuted)
			for k, l := range lines[j] {
				d.textAt(x, d.y-20-float64(k)*12.5, l, 9.5, false, colInk)
			}
		}
		d.y -= h
	}
}

type reportCol struct {
	title string
	w     float64
}

// table draws rows with a header that repeats on every page; cells wrap.
func (d *pdfDoc) table(cols []reportCol, rows [][]string) {
	head := func() {
		d.ensure(34)
		d.rect(repLeft, d.y-16, repWidth, 16, colBand)
		x := repLeft + 8
		for _, c := range cols {
			d.textAt(x, d.y-11, strings.ToUpper(c.title), 7, true, colMuted)
			x += c.w
		}
		d.y -= 18
	}
	head()
	for _, row := range rows {
		lines := make([][]string, len(cols))
		maxLines := 1
		for i, c := range cols {
			v := ""
			if i < len(row) {
				v = row[i]
			}
			if strings.TrimSpace(v) == "" {
				v = "—"
			}
			lines[i] = wrapW(v, c.w-10, 9, false)
			maxLines = max(maxLines, len(lines[i]))
		}
		h := float64(maxLines)*11.5 + 7
		if d.y-h < repBottom {
			d.newPage()
			head()
		}
		x := repLeft + 8
		for i, c := range cols {
			for k, l := range lines[i] {
				d.textAt(x, d.y-10-float64(k)*11.5, l, 9, false, colInk)
			}
			x += c.w
		}
		d.y -= h
		d.hline(repLeft, d.y+1, repWidth, colLine)
	}
	d.y -= 4
}

// timelineEntry draws one record: date and time on the left; author, kind and text on the right.
func (d *pdfDoc) timelineEntry(date, clock, author, kind, text string) {
	textX := repLeft + 78
	textW := repWidth - 78
	d.ensure(44)
	top := d.y
	d.textAt(repLeft, top-10, date, 9, true, colInk)
	d.textAt(repLeft, top-22, clock, 8.5, false, colMuted)
	d.textAt(textX, top-10, author, 9, true, colInk)
	if kind != "" {
		tw := textWidth(kind, 7.5, true) + 12
		d.rect(repLeft+repWidth-tw, top-14, tw, 14, colBand)
		d.textAt(repLeft+repWidth-tw+6, top-10.5, kind, 7.5, true, colAccent)
	}
	d.y -= 17
	if strings.TrimSpace(text) != "" {
		d.block(textX, textW, text, 9.2, false, colInk)
	}
	d.y -= 5
	d.hline(repLeft, d.y, repWidth, colLine)
	d.y -= 7
}

// reportLines is the number of lines a text takes in a column; used by tests.
func reportLines(text string, w float64) int { return len(wrapW(text, w, 9.5, false)) }

var _ = utf8.RuneCountInString
