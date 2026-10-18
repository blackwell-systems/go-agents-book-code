package schedule_test

import (
	"testing"
	"time"

	"example.com/remind/schedule"
)

func TestHiddenWeeklyNextIsAfter(t *testing.T) {
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo", "Pacific/Auckland", "Europe/London", "UTC"} {
		z := schedule.LookupZone(zone)
		for _, spec := range []string{"on sun at 20:00", "on mon at 07:30", "on mon,wed,fri at 23:45", "on sat at 00:15"} {
			r, err := schedule.Parse(spec, z, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
			for h := 0; h < 24*8; h += 5 {
				after := start.Add(time.Duration(h)*time.Hour + 17*time.Minute)
				next := r.Next(after)
				if !next.After(after) || next.Sub(after) > 7*24*time.Hour {
					t.Errorf("%s %q: Next(%v) = %v", zone, spec, after.In(z.Location()), next.In(z.Location()))
				}
			}
		}
	}
}
