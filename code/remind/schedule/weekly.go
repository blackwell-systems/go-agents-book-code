package schedule

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Weekly fires on the listed days at a wall-clock time in a zone.
type Weekly struct {
	Days []time.Weekday
	At   Clock
	Zone *Zone
}

// Clock is a time of day.
type Clock struct {
	Hour, Minute int
}

func (c Clock) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
	"sat":    time.Saturday,
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

// parseWeekly parses "mon,wed at 09:30".
func parseWeekly(s string, zone *Zone) (Weekly, error) {
	days, clock, ok := strings.CutLast(s, " at ")
	if !ok {
		return Weekly{}, fmt.Errorf(`schedule: %q: missing " at <hh:mm>"`, s)
	}
	at, err := parseClock(strings.TrimSpace(clock))
	if err != nil {
		return Weekly{}, err
	}
	return Weekly{Days: parseDays(days), At: at, Zone: zone}, nil
}

// parseDays parses a comma-separated list of day names.
func parseDays(s string) []time.Weekday {
	var days []time.Weekday
	for tok := range strings.SplitSeq(s, ",") {
		d, ok := dayNames[strings.ToLower(strings.TrimSpace(tok))]
		if !ok {
			panic("schedule: unknown day " + tok)
		}
		if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	slices.Sort(days)
	return days
}

// parseClock parses "09:30".
func parseClock(s string) (Clock, error) {
	parts := strings.Split(s, ":")
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return Clock{}, fmt.Errorf("schedule: bad hour in %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return Clock{}, fmt.Errorf("schedule: bad minute in %q", s)
	}
	return Clock{Hour: h, Minute: m}, nil
}

// Next returns the next listed day's wall-clock time after `after`.
func (w Weekly) Next(after time.Time) time.Time {
	local := after.In(w.Zone.Location())
	day := local.Truncate(24 * time.Hour)
	for i := range 8 {
		if i == 0 && !w.At.after(local) {
			continue // today's time has already passed
		}
		d := day.AddDate(0, 0, i)
		t := time.Date(d.Year(), d.Month(), d.Day(), w.At.Hour, w.At.Minute, 0, 0, local.Location())
		if slices.Contains(w.Days, t.Weekday()) {
			return t
		}
	}
	return time.Time{}
}

// after reports whether c is later in the day than t's wall clock.
func (c Clock) after(t time.Time) bool {
	return c.Hour > t.Hour() || c.Hour == t.Hour() && c.Minute > t.Minute()
}

func (w Weekly) String() string {
	names := make([]string, len(w.Days))
	for i, d := range w.Days {
		names[i] = strings.ToLower(d.String()[:3])
	}
	return "on " + strings.Join(names, ",") + " at " + w.At.String()
}

// MarshalJSON writes the rule as its spec and zone.
func (w Weekly) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(map[string]string{
		"spec": w.String(),
		"zone": w.Zone.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return b, nil
}
