package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sp = func() *time.Location { l, _ := time.LoadLocation("America/Sao_Paulo"); return l }()

const multistatusFixture = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
<d:response><d:href>/a.ics</d:href><d:propstat><d:prop><c:calendar-data>BEGIN:VCALENDAR
BEGIN:VEVENT
UID:1
DTSTART:20261124T170000Z
DTEND:20261124T180000Z
SUMMARY:Reuniao
END:VEVENT
END:VCALENDAR</c:calendar-data></d:prop></d:propstat></d:response>
<d:response><d:href>/b.ics</d:href><d:propstat><d:prop><c:calendar-data>BEGIN:VCALENDAR
BEGIN:VEVENT
UID:2
DTSTART;TZID=America/Sao_Paulo:20261125T140000
DURATION:PT1H30M
END:VEVENT
BEGIN:VEVENT
UID:3
DTSTART;VALUE=DATE:20261126
END:VEVENT
BEGIN:VEVENT
UID:4
DTSTART:20261127T120000Z
DTEND:20261127T130000Z
TRANSP:TRANSPARENT
END:VEVENT
BEGIN:VEVENT
UID:5
DTSTART:20261127T150000Z
DTEND:20261127T160000Z
STATUS:CANCELLED
END:VEVENT
BEGIN:VEVENT
UID:6
DTSTART:20261128T130000
DTEND:20261128T140000
END:VEVENT
END:VCALENDAR</c:calendar-data></d:prop></d:propstat></d:response>
</d:multistatus>`

func TestParseBusy_ReadsUTCZonedDurationAllDayAndFloatingEvents(t *testing.T) {
	got, err := ParseBusy([]byte(multistatusFixture), sp)
	require.NoError(t, err)
	require.Len(t, got, 4, "the transparent and the cancelled events are not busy")

	assert.Equal(t, time.Date(2026, 11, 24, 17, 0, 0, 0, time.UTC), got[0].Start.UTC())
	assert.Equal(t, time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC), got[0].End.UTC())
	// 14:00 in São Paulo (UTC-3) for 1h30
	assert.Equal(t, time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC), got[1].Start.UTC())
	assert.Equal(t, 90*time.Minute, got[1].End.Sub(got[1].Start))
	// an all-day event blocks the whole day
	assert.Equal(t, 24*time.Hour, got[2].End.Sub(got[2].Start))
	// a floating time is read in the calendar's zone: 13:00 São Paulo = 16:00 UTC
	assert.Equal(t, time.Date(2026, 11, 28, 16, 0, 0, 0, time.UTC), got[3].Start.UTC())
}

func TestBuildICS_IsAValidSingleEventInUTCWithEscapedText(t *testing.T) {
	ics := BuildICS(Event{UID: "abc-1", Summary: "Conversa; com, advogado", Description: "Área: Trabalhista\nCidade: Salvador",
		Location: "Escritório", Start: time.Date(2026, 11, 24, 14, 0, 0, 0, sp), End: time.Date(2026, 11, 24, 14, 30, 0, 0, sp)})
	assert.Contains(t, ics, "BEGIN:VEVENT\r\n")
	assert.Contains(t, ics, "UID:abc-1\r\n")
	assert.Contains(t, ics, "DTSTART:20261124T170000Z\r\n")
	assert.Contains(t, ics, "DTEND:20261124T173000Z\r\n")
	assert.Contains(t, ics, `SUMMARY:Conversa\; com\, advogado`)
	assert.Contains(t, ics, `Trabalhista\nCidade`, "a newline becomes \\n")
	for _, line := range strings.Split(strings.TrimSuffix(ics, "\r\n"), "\r\n") {
		assert.LessOrEqual(t, len(line), 75, "folded at 75 octets: %q", line)
	}
}

func TestBuildICS_RoundTripsThroughTheParser(t *testing.T) {
	start := time.Date(2026, 11, 24, 14, 0, 0, 0, sp)
	ics := BuildICS(Event{UID: "u", Summary: "x", Start: start, End: start.Add(30 * time.Minute)})
	got := parseEvents(ics, sp)
	require.Len(t, got, 1)
	assert.True(t, got[0].Start.Equal(start))
}

func TestClient_BusyAndCreateEventSpeakCalDAV(t *testing.T) {
	var reportBody, putBody, putUser, ifNone string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, _ := r.BasicAuth()
		switch r.Method {
		case "REPORT":
			b, _ := io.ReadAll(r.Body)
			reportBody = string(b)
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(multistatusFixture))
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			putBody, putUser, ifNone = string(b), u, r.Header.Get("If-None-Match")
			assert.Equal(t, "/cal/triagem/uid-9.ics", r.URL.Path)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), CalendarURL: srv.URL + "/cal/triagem/", User: "triagem", Pass: "app-pass", Zone: sp}
	busy, err := c.Busy(context.Background(), time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Len(t, busy, 4)
	assert.Contains(t, reportBody, `<c:expand start="20261124T000000Z" end="20261201T000000Z"/>`, "recurrences are expanded by the server")
	assert.Contains(t, reportBody, "calendar-query")

	err = c.CreateEvent(context.Background(), Event{UID: "uid-9", Summary: "s", Start: time.Now(), End: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "triagem", putUser)
	assert.Equal(t, "*", ifNone, "never overwrites an existing event")
	assert.Contains(t, putBody, "UID:uid-9")
}

func TestClient_MapsServerAnswersToErrors(t *testing.T) {
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), CalendarURL: srv.URL + "/cal/x/", User: "u", Pass: "p"}
	ctx := context.Background()
	from, to := time.Now(), time.Now().Add(time.Hour)

	cases := []struct {
		code int
		busy error
		put  error
	}{{401, ErrAuth, ErrAuth}, {404, ErrNotFound, ErrNotFound}, {500, ErrUnavailable, ErrUnavailable}, {412, ErrUnavailable, ErrConflict}}
	for _, tc := range cases {
		status = tc.code
		_, err := c.Busy(ctx, from, to)
		assert.ErrorIs(t, err, tc.busy, "REPORT %d", tc.code)
		assert.ErrorIs(t, c.CreateEvent(ctx, Event{UID: "u", Start: from, End: to}), tc.put, "PUT %d", tc.code)
	}
}

func TestClient_DoesNotFollowARedirectToAnotherHost(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the redirect was followed to %s with %v", r.Host, r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := &Client{HTTP: hc, CalendarURL: srv.URL + "/cal/x/", User: "u", Pass: "p"}
	_, err := c.Busy(context.Background(), time.Now(), time.Now().Add(time.Hour))
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestClient_DeleteEventAndAReadOnlyCalendar(t *testing.T) {
	var gotMethod, gotPath string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), CalendarURL: srv.URL + "/cal/x/", User: "u", Pass: "p"}
	ctx := context.Background()

	require.NoError(t, c.DeleteEvent(ctx, "e1@whatc-test"))
	assert.Equal(t, "DELETE", gotMethod)
	assert.Equal(t, "/cal/x/e1@whatc-test.ics", gotPath)

	status = http.StatusNotFound
	assert.NoError(t, c.DeleteEvent(ctx, "gone"), "already removed is fine")

	status = http.StatusForbidden // a calendar shared read-only: known user, no write
	assert.ErrorIs(t, c.DeleteEvent(ctx, "x"), ErrReadOnly)
	assert.ErrorIs(t, c.CreateEvent(ctx, Event{UID: "x", Start: time.Now(), End: time.Now().Add(time.Hour)}), ErrReadOnly)
	status = http.StatusUnauthorized
	assert.ErrorIs(t, c.CreateEvent(ctx, Event{UID: "x", Start: time.Now(), End: time.Now().Add(time.Hour)}), ErrAuth)
}
