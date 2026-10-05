// Package businesshours holds the one rule that says whether a moment falls inside an
// organization's configured business hours, shared by the chatbot and by the AI's
// get_business_hours tool so the two can never disagree.
//
// The hours are ChatbotSettings.BusinessHours.Hours: a list of {day 0-6 (Sunday = 0), enabled,
// start_time, end_time} with "HH:MM" text compared as text. The clock is the server's: there is
// no per-organization time zone.
package businesshours

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
)

// entry reads one element of the hours list; ok is false when it is not a usable object.
func entry(raw any) (day int, enabled bool, start, end string, hasTimes, ok bool) {
	m, isMap := raw.(map[string]any)
	if !isMap {
		return 0, false, "", "", false, false
	}
	d, isNum := m["day"].(float64)
	if !isNum {
		return 0, false, "", "", false, false
	}
	enabled, _ = m["enabled"].(bool)
	start, okS := m["start_time"].(string)
	end, okE := m["end_time"].(string)
	return int(d), enabled, start, end, okS && okE, true
}

// Within reports whether now is inside the configured hours. The first entry for today decides:
// disabled means closed; one without both times is skipped in favour of the next entry for that
// day; otherwise it is open from start to end, both inclusive. No entry for today means closed.
func Within(hours models.JSONBArray, now time.Time) bool {
	today := int(now.Weekday())
	current := now.Format("15:04")
	for _, raw := range hours {
		day, enabled, start, end, hasTimes, ok := entry(raw)
		if !ok || day != today {
			continue
		}
		if !enabled {
			return false // the day exists but is disabled
		}
		if !hasTimes {
			continue
		}
		return current >= start && current <= end
	}
	return false
}

// DayHours is the opening of one weekday as Schedule reports it.
type DayHours struct {
	Day   time.Weekday
	Open  bool
	Start string
	End   string
}

// Schedule lists the seven weekdays, Sunday first, resolved by the same rule as Within: the
// first usable entry of each day decides. A day with no usable entry, or a disabled one, is closed.
func Schedule(hours models.JSONBArray) []DayHours {
	out := make([]DayHours, 7)
	decided := [7]bool{}
	for i := range out {
		out[i].Day = time.Weekday(i)
	}
	for _, raw := range hours {
		day, enabled, start, end, hasTimes, ok := entry(raw)
		if !ok || day < 0 || day > 6 || decided[day] {
			continue
		}
		if !enabled {
			decided[day] = true
			continue
		}
		if !hasTimes {
			continue
		}
		decided[day] = true
		out[day].Open, out[day].Start, out[day].End = true, start, end
	}
	return out
}
