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
	}
	for name, p := range good {
		assert.True(t, openSendIntentPredicateOK(p), name)
	}

	bad := map[string]string{
		"empty (not partial)":           ``,
		"one state only":                `((state)::text = 'processing'::text)`,
		"the other state only":          `((state)::text = 'uncertain'::text)`,
		"NOT IN of both":                `((state)::text <> ALL ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]))`,
		"NOT (...)":                     `(NOT ((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[])))`,
		"OR on another column":          `(((state)::text = 'processing'::text) OR ((kind)::text = 'uncertain'::text))`,
		"IN plus a time condition":      `(((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[])) AND (claimed_at < '2000-01-01 00:00:00+00'::timestamp with time zone))`,
		"a third state":                 `((state)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying, 'sent'::character varying])::text[]))`,
		"the same state twice":          `((state)::text = ANY ((ARRAY['processing'::character varying, 'processing'::character varying])::text[]))`,
		"another column":                `((kind)::text = ANY ((ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]))`,
		"both states as OR":             `(((state)::text = 'processing'::text) OR ((state)::text = 'uncertain'::text))`,
		"a function around the column":  `(lower((state)::text) = ANY (ARRAY['processing'::text, 'uncertain'::text]))`,
		"a comparison of two states":    `((state)::text = 'processing'::text AND (state)::text = 'uncertain'::text)`,
		"literals with other spellings": `((state)::text = ANY (ARRAY['Processing'::text, 'Uncertain'::text]))`,
	}
	for name, p := range bad {
		assert.False(t, openSendIntentPredicateOK(p), name)
	}
}
