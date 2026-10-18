package calendar

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

var dayCodes = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// WeeklyRRule returns a rule that repeats every week on the given days,
// such as "FREQ=WEEKLY;BYDAY=MO,WE". Days keep their order; repeats are
// dropped.
func WeeklyRRule(days []time.Weekday) string {
	var codes []string
	for _, d := range days {
		c := dayCodes[d]
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	if len(codes) == 0 {
		return "FREQ=WEEKLY"
	}
	return "FREQ=WEEKLY;BYDAY=" + strings.Join(codes, ",")
}

// intervalUnits are the frequencies IntervalRRule chooses from, largest
// first.
var intervalUnits = []struct {
	freq string
	size time.Duration
}{
	{"WEEKLY", 7 * 24 * time.Hour},
	{"DAILY", 24 * time.Hour},
	{"HOURLY", time.Hour},
	{"MINUTELY", time.Minute},
	{"SECONDLY", time.Second},
}

// IntervalRRule returns a rule that repeats every d, using the largest
// unit that divides d: 2h gives "FREQ=HOURLY;INTERVAL=2" and 90m gives
// "FREQ=MINUTELY;INTERVAL=90". It returns "" if d is shorter than a
// second or not a whole number of seconds.
func IntervalRRule(d time.Duration) string {
	for _, u := range intervalUnits {
		if d < u.size || d%u.size != 0 {
			continue
		}
		n := int64(d / u.size)
		if n == 1 {
			return "FREQ=" + u.freq
		}
		return fmt.Sprintf("FREQ=%s;INTERVAL=%d", u.freq, n)
	}
	return ""
}
