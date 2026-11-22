package caldav

import (
	"sort"
	"time"
)

// Rules say when the office may be booked.
type Rules struct {
	Zone *time.Location
	// Hours is the opening window of each weekday in minutes from midnight ({540, 1080} is 09:00-18:00).
	// A weekday that is absent is closed.
	Hours     map[time.Weekday][2]int
	Slot      time.Duration // length of one meeting; slots start on this grid from the opening time
	Buffer    time.Duration // free time kept around any other event
	MinNotice time.Duration // nothing earlier than now + this
	Lookahead int           // how many days ahead may be booked (including today)
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// Slots returns up to want free slot starts. They are spread over different days first (the earliest
// free slot of each of the first days that have one), then filled with later slots of those days, so a
// customer is offered real choices instead of three times on the same afternoon.
func Slots(r Rules, busy []Interval, now time.Time, want int) []time.Time {
	if r.Zone == nil {
		r.Zone = time.UTC
	}
	if r.Slot <= 0 || want <= 0 || r.Lookahead <= 0 {
		return nil
	}
	earliest := now.Add(r.MinNotice)
	today := now.In(r.Zone)
	var perDay [][]time.Time
	for d := 0; d < r.Lookahead; d++ {
		day := time.Date(today.Year(), today.Month(), today.Day()+d, 0, 0, 0, 0, r.Zone)
		win, open := r.Hours[day.Weekday()]
		if !open || win[1] <= win[0] {
			continue
		}
		openAt := day.Add(time.Duration(win[0]) * time.Minute)
		closeAt := day.Add(time.Duration(win[1]) * time.Minute)
		var free []time.Time
		for s := openAt; !s.Add(r.Slot).After(closeAt); s = s.Add(r.Slot) {
			if s.Before(earliest) {
				continue
			}
			clash := false
			for _, b := range busy {
				if overlaps(s, s.Add(r.Slot), b.Start.Add(-r.Buffer), b.End.Add(r.Buffer)) {
					clash = true
					break
				}
			}
			if !clash {
				free = append(free, s)
			}
		}
		if len(free) > 0 {
			perDay = append(perDay, free)
		}
	}
	var out []time.Time
	for round := 0; len(out) < want; round++ {
		added := false
		for _, free := range perDay {
			if round < len(free) && len(out) < want {
				out = append(out, free[round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}
