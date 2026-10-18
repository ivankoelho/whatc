package handlers

import (
	"encoding/csv"
	"regexp"
	"strings"
	"unicode"
)

// Spreadsheet formula injection ("CSV injection"). A cell that starts with = + - @ (or TAB, CR)
// is evaluated as a formula when the file is opened in Excel or LibreOffice, and some of the text we
// export is written by people outside the company (a WhatsApp profile name arrives from the webhook).
// The defence is applied in ONE place, the serialization of every exported cell, so a column added
// later is covered without anyone having to remember it; and it is undone on import so the protective
// apostrophe does not become part of the data after a round trip (export, edit, import).
//
// A cell is "guarded" by a leading apostrophe, the spreadsheet convention for "this is text".

// numericCell is a plain number (a phone number kept with its '+', a negative value): harmless as
// a spreadsheet value, so it keeps its representation. Anything with an operator, a function call or
// whitespace is not plain.
var numericCell = regexp.MustCompile(`^[+-]?[0-9]+([.,][0-9]+)?$`)

// isInvisibleRune: characters that render as nothing (or reorder text) and that a spreadsheet may
// skip before deciding what a cell is, so they cannot be allowed to hide a trigger.
func isInvisibleRune(r rune) bool {
	return r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// startsWithFormulaTrigger reports whether the first meaningful character is a trigger. TAB and CR are
// triggers by themselves; other spaces, control and invisible characters in front are looked through.
func startsWithFormulaTrigger(s string) bool {
	for _, r := range s {
		switch {
		case r == '=' || r == '+' || r == '-' || r == '@' || r == '\t' || r == '\r':
			return true
		case unicode.IsSpace(r) || unicode.IsControl(r) || isInvisibleRune(r):
			continue
		default:
			return false
		}
	}
	return false
}

// csvNeedsGuard: the cell would be read as a formula. Leading apostrophes are looked through, so that a
// value that itself begins with an apostrophe in front of a trigger ("'=1") is guarded too, which
// is what keeps export and import exact inverses of each other.
func csvNeedsGuard(s string) bool {
	core := strings.TrimLeft(s, "'")
	return startsWithFormulaTrigger(core) && !numericCell.MatchString(core)
}

// csvSafeCell returns the cell as it is written to the file.
func csvSafeCell(s string) string {
	if csvNeedsGuard(s) {
		return "'" + s
	}
	return s
}

// csvUnsafeCell undoes csvSafeCell on a cell read from a file. Only an apostrophe that csvSafeCell could
// have added is removed: "'hello" and "'5" are data and stay as they are.
func csvUnsafeCell(s string) string {
	if strings.HasPrefix(s, "'") && csvNeedsGuard(s[1:]) {
		return s[1:]
	}
	return s
}

// writeSafeCSVRow writes one row (header or data) with every cell neutralized.
func writeSafeCSVRow(w *csv.Writer, cells []string) error {
	safe := make([]string, len(cells))
	for i, c := range cells {
		safe[i] = csvSafeCell(c)
	}
	return w.Write(safe)
}
