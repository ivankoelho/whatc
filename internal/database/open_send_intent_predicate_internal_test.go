package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The predicate of the open-send index is judged by what it MEANS, not by one rendering: the catalog prints it
// differently between Postgres versions. Everything below is the text pg_get_expr could produce.
func TestOpenSendIntentPredicateOK(t *testing.T) {
	good := map[string]string{
		"Postgres 17 rendering":     `((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]))`,
		"older rendering":           `((state)::text = ANY (ARRAY['processing'::text, 'uncertain'::text]))`,
		"no casts":                  `(state = ANY (ARRAY['processing', 'uncertain']))`,
		"the other element order":   `((state)::text = ANY ((ARRAY['uncertain'::character varying, 'processing'::character varying])::text[]))`,
		"extra whitespace and tabs": "((state)::text =  ANY (\n\t(ARRAY['processing'::character varying,\n 'uncertain'::character varying])::text[]))",
		// What pg_dump/pg_restore leaves for the SAME index (seen on PostgreSQL 16.15 and 17.11): each element parenthesized with its own cast.
		"restored by pg_restore (16.15 and 17.11)": `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, the other element order":        `((state)::text = ANY (ARRAY[('uncertain'::character varying)::text, ('processing'::character varying)::text]))`,
		"restored, extra whitespace":               "((state)::text = ANY (ARRAY[ ( 'processing'::character varying )::text ,\n\t('uncertain'::character varying)::text ]))",
	}
	for name, p := range good {
		assert.True(t, openSendIntentPredicateOK(p), name)
	}

	bad := map[string]string{
		"empty (not partial)":          ``,
		"one state only":               `((state)::text = 'processing'::text)`,
		"the other state only":         `((state)::text = 'uncertain'::text)`,
		"NOT IN of both":               `((state)::text <> ALL ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]))`,
		"NOT (...)":                    `(NOT ((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[])))`,
		"OR on another column":         `(((state)::text = 'processing'::text) OR ((kind)::text = 'uncertain'::text))`,
		"IN plus a time condition":     `(((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[])) AND (claimed_at < '2000-01-01 00:00:00+00'::timestamp with time zone))`,
		"a third state":                `((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying, 'sent'::character varying])::text[]))`,
		"the same state twice":         `((state)::text = ANY ((ARRAY['processing'::character varying, 'processing'::character varying])::text[]))`,
		"another column":               `((kind)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]))`,
		"both states as OR":            `(((state)::text = 'processing'::text) OR ((state)::text = 'uncertain'::text))`,
		"a function around the column": `(lower((state)::text) = ANY (ARRAY['processing'::text, 'uncertain'::text]))`,
		"a comparison of two states":   `((state)::text = 'processing'::text AND (state)::text = 'uncertain'::text)`,
		// The shapes found by the last independent review: each of them used to pass, none protects anything (or only half).
		"two constants compared":            `('processing'::text = 'uncertain'::text)`,
		"two booleans compared":             `(((state)::text = 'processing'::text) = ((state)::text = 'uncertain'::text))`,
		"two arrays compared":               `(ARRAY['processing'::text, 'uncertain'::text] = ARRAY[state, state])`,
		"the column inside the array":       `('processing'::text = ANY (ARRAY[state, 'uncertain'::text]))`,
		"booleans with the constants first": `(('processing'::text = state) = ('uncertain'::text = state))`,
		"the other column inside the array": `('uncertain'::text = ANY (ARRAY['processing'::text, state]))`,
		// More ways to keep the literals and the tokens while changing the meaning.
		"ALL instead of ANY":              `((state)::text = ALL (ARRAY['processing'::text, 'uncertain'::text]))`,
		"a one-element array":             `((state)::text = ANY (ARRAY['processing'::text]))`,
		"a three-element array":           `((state)::text = ANY (ARRAY['processing'::text, 'uncertain'::text, 'sent'::text]))`,
		"unbalanced parentheses":          `((state)::text = ANY (ARRAY['processing'::text, 'uncertain'::text])))`,
		"a trailing condition":            `((state)::text = ANY (ARRAY['processing'::text, 'uncertain'::text])) IS TRUE`,
		"a leading negation":              `NOT ((state)::text = ANY (ARRAY['processing'::text, 'uncertain'::text]))`,
		"the column compared with itself": `((state)::text = ANY (ARRAY[state, state]))`,
		"literals with other spellings":   `((state)::text = ANY (ARRAY['Processing'::text, 'Uncertain'::text]))`,
		// The restored (parenthesized) shape must be as strict as the bare one: every mutation of it is refused too.
		"restored, ALL instead of ANY":                `((state)::text = ALL (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, <> ALL (NOT IN)":                   `((state)::text <> ALL (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, a third element":                   `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text, ('sent'::character varying)::text]))`,
		"restored, a replaced element":                `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('sent'::character varying)::text]))`,
		"restored, the same element twice":            `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('processing'::character varying)::text]))`,
		"restored, a one-element array":               `((state)::text = ANY (ARRAY[('processing'::character varying)::text]))`,
		"restored, the column inside the array":       `((state)::text = ANY (ARRAY[('processing'::character varying)::text, (state)::text]))`,
		"restored, the other column":                  `((kind)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, a constant compared to a constant": `(('processing'::character varying)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, NOT around it":                     `(NOT ((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])))`,
		"restored, OR with another condition":         `(((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])) OR (kind = 'x'::text))`,
		"restored, AND with another condition":        `(((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])) AND (claimed_at < now()))`,
		"restored, a trailing condition":              `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])) IS TRUE`,
		"restored, unbalanced parentheses":            `((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])))`,
		"restored, a literal with another spelling":   `((state)::text = ANY (ARRAY[('Processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, the elements mixed (one bare)":     `((state)::text = ANY (ARRAY[('processing'::character varying)::text, 'uncertain'::character varying]))`,
		"restored, an array of one ROW":               `((state)::text = ANY (ARRAY[('processing', 'uncertain')]))`,
		"restored, a function around the element":     `((state)::text = ANY (ARRAY[lower('processing'::character varying)::text, ('uncertain'::character varying)::text]))`,
		"restored, a cast to a non-character type":    `((state)::text = ANY (ARRAY[('processing'::character varying)::boolean, ('uncertain'::character varying)::text]))`,
		"bare shape with the elements of both":        `((state)::text = ANY (ARRAY['processing'::text, ('uncertain'::character varying)::text]))`,
	}
	for name, p := range bad {
		assert.False(t, openSendIntentPredicateOK(p), name)
	}
}

// The advice printed for the operator always double-quotes a catalog name (embedded quotes doubled), so a name that is a
// reserved SQL word (user, order, ...) or needs quoting (capitals, hyphen, space, accents) still works when pasted.
func TestQuoteIdentForAdvice(t *testing.T) {
	cases := map[string]string{
		"uq_send_intent_open": `"uq_send_intent_open"`,
		"user":                `"user"`,
		"order":               `"order"`,
		"Zz-Other Quoted":     `"Zz-Other Quoted"`,
		`a"b`:                 `"a""b"`,
		"café":                `"café"`,
		"1abc":                `"1abc"`,
	}
	for in, want := range cases {
		assert.Equal(t, want, quoteIdentForAdvice(in), in)
	}
}
