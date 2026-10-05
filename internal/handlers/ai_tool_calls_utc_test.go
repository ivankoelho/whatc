package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

// A plain date in from/to is a UTC date; an RFC 3339 value carries its own offset. Neither is ever
// read in the server's or the caller's local time zone.
func TestAIToolCallsAPI_TimeFiltersAreUTC(t *testing.T) {
	e := newCallsEnv(t)
	before := time.Date(2026, 10, 2, 23, 30, 0, 0, time.UTC) // Oct 2, 20:30 in Brasília
	after := time.Date(2026, 10, 3, 0, 30, 0, 0, time.UTC)   // Oct 2, 21:30 in Brasília
	a := e.seed(t, e.orgA.ID, func(c *models.AIToolCall) { c.RequestedAt = before })
	b := e.seed(t, e.orgA.ID, func(c *models.AIToolCall) { c.RequestedAt = after })

	ids := func(q map[string]string) []string {
		_, body := e.list(t, e.orgA.ID, e.adminA.ID, q)
		var out []string
		for _, c := range decodeCalls(t, body).Data.Calls {
			out = append(out, c["id"].(string))
		}
		return out
	}
	assert.Equal(t, []string{b.ID.String()}, ids(map[string]string{"from": "2026-10-03"}), "from=date starts at 00:00 UTC")
	assert.Equal(t, []string{a.ID.String()}, ids(map[string]string{"to": "2026-10-02"}), "to=date ends at 23:59:59 UTC of that day")
	assert.Equal(t, []string{b.ID.String()}, ids(map[string]string{"from": "2026-10-02T21:00:00-03:00"}), "an RFC 3339 offset is honoured (21:00-03:00 is 00:00Z)")
	assert.Equal(t, []string{a.ID.String()}, ids(map[string]string{"to": "2026-10-02T20:59:59-03:00"}))
}
