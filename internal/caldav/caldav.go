// Package caldav is the small part of CalDAV the scheduling agent needs against a Nextcloud calendar:
// read when the calendar is busy, and create one event. It speaks to one calendar collection with an app
// password and never follows a redirect to another host (the credentials would go with it).
package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxBody  = 4 << 20
	icsStamp = "20060102T150405Z"
)

// Errors a caller tells apart: bad credentials, a calendar that is not there, and everything else.
var (
	ErrAuth        = errors.New("the calendar server did not accept the user or app password")
	ErrNotFound    = errors.New("the calendar was not found")
	ErrUnavailable = errors.New("the calendar server could not be reached or answered something unexpected")
	ErrConflict    = errors.New("an event with this id already exists")
	ErrReadOnly    = errors.New("the calendar does not accept new events (read-only for this user)")
)

// Interval is a stretch of time in which the calendar is busy.
type Interval struct{ Start, End time.Time }

// Event is what gets written to the calendar.
type Event struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Start, End  time.Time
}

// Client talks to one calendar collection. CalendarURL is the full address of the collection, ending in "/".
type Client struct {
	HTTP        *http.Client
	CalendarURL string
	User, Pass  string
	// Zone is where floating times (no zone, no Z) are read; UTC when nil.
	Zone *time.Location
}

func (c *Client) do(ctx context.Context, method, target string, hdr map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.User, c.Pass)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.HTTP.Do(req)
}

// Busy returns the busy intervals of the calendar between from and to. Recurring events come back already
// expanded into their occurrences inside the range (the server does it: calendar-data/expand).
func (c *Client) Busy(ctx context.Context, from, to time.Time) ([]Interval, error) {
	f, t := from.UTC().Format(icsStamp), to.UTC().Format(icsStamp)
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop>` +
		`<c:calendar-data><c:expand start="` + f + `" end="` + t + `"/></c:calendar-data></d:prop>` +
		`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
		`<c:time-range start="` + f + `" end="` + t + `"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`
	resp, err := c.do(ctx, "REPORT", c.CalendarURL, map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"}, []byte(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusMultiStatus:
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, ErrUnavailable
	}
	return ParseBusy(raw, c.Zone)
}

// CreateEvent writes the event as <calendar>/<uid>.ics. It never overwrites: an existing id is ErrConflict.
func (c *Client) CreateEvent(ctx context.Context, ev Event) error {
	target := strings.TrimRight(c.CalendarURL, "/") + "/" + url.PathEscape(ev.UID) + ".ics"
	resp, err := c.do(ctx, "PUT", target, map[string]string{"Content-Type": "text/calendar; charset=utf-8", "If-None-Match": "*"}, []byte(BuildICS(ev)))
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden: // the user is known but may not write here (a calendar shared read-only)
		return ErrReadOnly
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusPreconditionFailed, http.StatusConflict:
		return ErrConflict
	}
	return ErrUnavailable
}

// DeleteEvent removes the event written as <calendar>/<uid>.ics. An event that is already gone is not an error.
func (c *Client) DeleteEvent(ctx context.Context, uid string) error {
	target := strings.TrimRight(c.CalendarURL, "/") + "/" + url.PathEscape(uid) + ".ics"
	resp, err := c.do(ctx, "DELETE", target, nil, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	case http.StatusUnauthorized:
		return ErrAuth
	case http.StatusForbidden:
		return ErrReadOnly
	}
	return ErrUnavailable
}

// --- iCalendar --------------------------------------------------------------------------------------

func escapeText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "")
	return r.Replace(s)
}

// fold wraps a content line at 73 characters, as RFC 5545 asks, never inside a multi-byte character.
func fold(line string) string {
	var b strings.Builder
	n := 0
	for _, r := range line {
		l := len(string(r))
		if n+l > 73 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += l
	}
	return b.String()
}

// BuildICS renders a single-event calendar in UTC.
func BuildICS(ev Event) string {
	lines := []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Whatc//Agenda//PT", "CALSCALE:GREGORIAN", "BEGIN:VEVENT",
		"UID:" + ev.UID,
		"DTSTAMP:" + time.Now().UTC().Format(icsStamp),
		"DTSTART:" + ev.Start.UTC().Format(icsStamp),
		"DTEND:" + ev.End.UTC().Format(icsStamp),
		"SUMMARY:" + escapeText(ev.Summary),
	}
	if ev.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escapeText(ev.Description))
	}
	if ev.Location != "" {
		lines = append(lines, "LOCATION:"+escapeText(ev.Location))
	}
	lines = append(lines, "STATUS:CONFIRMED", "END:VEVENT", "END:VCALENDAR")
	for i, l := range lines {
		lines[i] = fold(l)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

type multistatus struct {
	Responses []struct {
		Data []string `xml:"propstat>prop>calendar-data"`
	} `xml:"response"`
}

// ParseBusy reads a CalDAV multistatus answer into the busy intervals of its events. Cancelled and
// transparent ("free") events are not busy. zone is where a floating time is read (UTC when nil).
func ParseBusy(raw []byte, zone *time.Location) ([]Interval, error) {
	var ms multistatus
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if zone == nil {
		zone = time.UTC
	}
	var out []Interval
	for _, r := range ms.Responses {
		for _, data := range r.Data {
			out = append(out, parseEvents(data, zone)...)
		}
	}
	return out, nil
}

// unfold joins the continuation lines of an iCalendar text.
func unfold(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

func parseEvents(ics string, zone *time.Location) []Interval {
	var out []Interval
	inEvent := false
	var start, end time.Time
	var hasStart, hasEnd, allDay, free, cancelled bool
	var dur time.Duration
	for _, line := range unfold(ics) {
		name, params, value := splitLine(line)
		switch {
		case name == "BEGIN" && value == "VEVENT":
			inEvent, hasStart, hasEnd, allDay, free, cancelled, dur = true, false, false, false, false, false, 0
		case name == "END" && value == "VEVENT":
			if inEvent && hasStart && !free && !cancelled {
				if !hasEnd {
					switch {
					case dur > 0:
						end = start.Add(dur)
					case allDay:
						end = start.AddDate(0, 0, 1)
					default:
						end = start.Add(time.Hour)
					}
				}
				if end.After(start) {
					out = append(out, Interval{Start: start, End: end})
				}
			}
			inEvent = false
		case !inEvent:
		case name == "DTSTART":
			start, allDay, hasStart = parseTime(value, params, zone)
		case name == "DTEND":
			end, _, hasEnd = parseTime(value, params, zone)
		case name == "DURATION":
			dur = parseDuration(value)
		case name == "TRANSP":
			free = strings.EqualFold(value, "TRANSPARENT")
		case name == "STATUS":
			cancelled = strings.EqualFold(value, "CANCELLED")
		}
	}
	return out
}

func splitLine(line string) (name string, params map[string]string, value string) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", nil, ""
	}
	head, value := line[:i], line[i+1:]
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	params = map[string]string{}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
	}
	return name, params, strings.TrimSpace(value)
}

func parseTime(value string, params map[string]string, zone *time.Location) (t time.Time, allDay, ok bool) {
	if strings.EqualFold(params["VALUE"], "DATE") || len(value) == 8 {
		d, err := time.ParseInLocation("20060102", value, zone)
		return d, true, err == nil
	}
	if strings.HasSuffix(value, "Z") {
		d, err := time.Parse(icsStamp, value)
		return d, false, err == nil
	}
	loc := zone
	if tz := params["TZID"]; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	d, err := time.ParseInLocation("20060102T150405", value, loc)
	return d, false, err == nil
}

// parseDuration reads the subset of ISO 8601 durations iCalendar uses (P1D, PT1H30M, PT45M).
func parseDuration(v string) time.Duration {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "+"), "P")
	var d time.Duration
	inTime := false
	num := 0
	for _, r := range v {
		switch {
		case r == 'T':
			inTime = true
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
		default:
			switch {
			case r == 'W':
				d += time.Duration(num) * 7 * 24 * time.Hour
			case r == 'D':
				d += time.Duration(num) * 24 * time.Hour
			case r == 'H' && inTime:
				d += time.Duration(num) * time.Hour
			case r == 'M' && inTime:
				d += time.Duration(num) * time.Minute
			case r == 'S' && inTime:
				d += time.Duration(num) * time.Second
			}
			num = 0
		}
	}
	return d
}
