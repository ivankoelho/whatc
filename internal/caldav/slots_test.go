package caldav

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var weekdays = map[time.Weekday][2]int{
	time.Monday: {540, 1080}, time.Tuesday: {540, 1080}, time.Wednesday: {540, 1080}, time.Thursday: {540, 1080}, time.Friday: {540, 1080},
}

func rules() Rules {
	return Rules{Zone: sp, Hours: weekdays, Slot: 30 * time.Minute, Buffer: 0, MinNotice: 2 * time.Hour, Lookahead: 14}
}

// a Monday, 08:00 in São Paulo
var monday = time.Date(2026, 11, 23, 8, 0, 0, 0, sp)

func TestSlots_AreSpreadOverDaysAndRespectOpeningHours(t *testing.T) {
	got := Slots(rules(), nil, monday, 3)
	assert.Equal(t, []time.Time{
		time.Date(2026, 11, 23, 10, 0, 0, 0, sp), // Monday, after the 2 h notice
		time.Date(2026, 11, 24, 9, 0, 0, 0, sp),
		time.Date(2026, 11, 25, 9, 0, 0, 0, sp),
	}, got)
}

func TestSlots_SkipBusyTimeAndTheBufferAroundIt(t *testing.T) {
	r := rules()
	r.Buffer = 30 * time.Minute
	busy := []Interval{{Start: time.Date(2026, 11, 24, 9, 0, 0, 0, sp), End: time.Date(2026, 11, 24, 10, 0, 0, 0, sp)}}
	got := Slots(r, busy, monday, 3)
	// Tuesday 09:00-10:00 is busy and 30 min around it: the first free Tuesday slot is 10:30
	assert.Equal(t, time.Date(2026, 11, 24, 10, 30, 0, 0, sp), got[1])
}

func TestSlots_NeverOfferWeekendsPastClosingOrTheNoticeWindow(t *testing.T) {
	friday := time.Date(2026, 11, 27, 17, 0, 0, 0, sp) // 17:00: with 2 h of notice nothing is left today
	got := Slots(rules(), nil, friday, 3)
	for _, s := range got {
		wd := s.In(sp).Weekday()
		assert.NotEqual(t, time.Saturday, wd)
		assert.NotEqual(t, time.Sunday, wd)
		assert.False(t, s.Before(friday.Add(2*time.Hour)))
	}
	assert.Equal(t, time.Date(2026, 11, 30, 9, 0, 0, 0, sp), got[0], "the next open day")
}

func TestSlots_FewDaysFreeFillsWithLaterSlotsOfTheSameDays(t *testing.T) {
	r := rules()
	r.Lookahead = 1 // only Monday
	got := Slots(r, nil, monday, 3)
	assert.Len(t, got, 3)
	assert.Equal(t, time.Date(2026, 11, 23, 10, 0, 0, 0, sp), got[0])
	assert.Equal(t, time.Date(2026, 11, 23, 10, 30, 0, 0, sp), got[1])
}

func TestSlots_NothingFreeOrNoRulesReturnsNothing(t *testing.T) {
	assert.Empty(t, Slots(Rules{Zone: sp, Slot: 0, Lookahead: 7}, nil, monday, 3))
	assert.Empty(t, Slots(Rules{Zone: sp, Hours: map[time.Weekday][2]int{}, Slot: time.Hour, Lookahead: 7}, nil, monday, 3), "closed every day")
	all := []Interval{{Start: monday, End: monday.AddDate(0, 0, 30)}}
	assert.Empty(t, Slots(rules(), all, monday, 3), "busy for a month")
}
