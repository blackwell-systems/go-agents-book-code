// Package schedule parses and evaluates recurring reminder schedules.
//
// A spec is one of:
//
//	every 2h               a fixed interval, counted from the reminder's creation
//	on mon,wed at 09:30    weekly, on the listed days, in a time zone
package schedule

import (
	"fmt"
	"strings"
	"time"
)

// A Rule decides when a recurring reminder fires.
type Rule interface {
	// Next returns the first time after `after` that the rule fires,
	// or the zero time if it never fires again.
	Next(after time.Time) time.Time
	// String returns the rule as a spec that Parse accepts.
	String() string
}

// Parse parses a spec. Weekly rules use zone; interval rules count from
// start.
func Parse(spec string, zone *Zone, start time.Time) (Rule, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(spec, "every "):
		return parseEvery(strings.TrimPrefix(spec, "every "), start)
	case strings.HasPrefix(spec, "on "):
		return parseWeekly(strings.TrimPrefix(spec, "on "), zone)
	}
	return nil, fmt.Errorf(`schedule %q: want "every <duration>" or "on <days> at <hh:mm>"`, spec)
}
