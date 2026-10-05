package businesshours_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/businesshours"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func day(d int, enabled bool, start, end string) map[string]any {
	return map[string]any{"day": float64(d), "enabled": enabled, "start_time": start, "end_time": end}
}

// 2026-10-05 is a Monday (weekday 1).
func at(hhmm string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04", "2026-10-05 "+hhmm)
	return t
}

func TestWithin_KeepsTheRuleTheChatbotAlwaysHad(t *testing.T) {
	hours := models.JSONBArray{day(1, true, "08:00", "18:00")}
	assert.True(t, businesshours.Within(hours, at("12:00")))
	assert.True(t, businesshours.Within(hours, at("08:00")), "start is inclusive")
	assert.True(t, businesshours.Within(hours, at("18:00")), "end is inclusive")
	assert.False(t, businesshours.Within(hours, at("07:59")))
	assert.False(t, businesshours.Within(hours, at("18:01")))

	assert.False(t, businesshours.Within(models.JSONBArray{day(1, false, "08:00", "18:00")}, at("12:00")), "disabled day")
	assert.False(t, businesshours.Within(models.JSONBArray{day(2, true, "08:00", "18:00")}, at("12:00")), "no entry for today")
	assert.False(t, businesshours.Within(nil, at("12:00")))
}

func TestWithin_TheFirstUsableEntryForTodayDecides(t *testing.T) {
	noTimes := map[string]any{"day": float64(1), "enabled": true}
	// an entry without times is skipped in favour of the next one for the same day
	assert.True(t, businesshours.Within(models.JSONBArray{noTimes, day(1, true, "08:00", "18:00")}, at("12:00")))
	// a first complete entry decides even if a later one would say otherwise
	assert.False(t, businesshours.Within(models.JSONBArray{day(1, true, "08:00", "09:00"), day(1, true, "08:00", "18:00")}, at("12:00")))
	// a disabled first entry closes the day
	assert.False(t, businesshours.Within(models.JSONBArray{day(1, false, "", ""), day(1, true, "08:00", "18:00")}, at("12:00")))
	// junk entries are ignored
	assert.True(t, businesshours.Within(models.JSONBArray{"x", map[string]any{"day": "monday"}, day(1, true, "08:00", "18:00")}, at("12:00")))
}

func TestSchedule_UsesTheSameRulePerDay(t *testing.T) {
	hours := models.JSONBArray{
		day(1, true, "08:00", "18:00"),
		day(2, false, "08:00", "18:00"),
		map[string]any{"day": float64(3), "enabled": true}, // no times, then a complete one
		day(3, true, "09:00", "12:00"),
		day(3, true, "00:00", "23:59"), // later duplicate: ignored
		day(9, true, "08:00", "18:00"), // out of range: ignored
	}
	s := businesshours.Schedule(hours)
	assert.Len(t, s, 7)
	assert.Equal(t, time.Sunday, s[0].Day)
	assert.False(t, s[0].Open)
	assert.Equal(t, businesshours.DayHours{Day: time.Monday, Open: true, Start: "08:00", End: "18:00"}, s[1])
	assert.False(t, s[2].Open, "a disabled day is closed")
	assert.Equal(t, businesshours.DayHours{Day: time.Wednesday, Open: true, Start: "09:00", End: "12:00"}, s[3])
	assert.False(t, s[6].Open)

	// Schedule and Within agree on every day and minute boundary
	for d := 0; d < 7; d++ {
		for _, hhmm := range []string{"00:00", "08:00", "10:00", "12:00", "18:00", "23:59"} {
			now := at(hhmm).AddDate(0, 0, d-1) // Monday + (d-1) has weekday d
			assert.Equal(t, d, int(now.Weekday()))
			want := s[d].Open && hhmm >= s[d].Start && hhmm <= s[d].End
			assert.Equal(t, want, businesshours.Within(hours, now), "day %d %s", d, hhmm)
		}
	}
}
