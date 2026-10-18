// Package duration parses the human-friendly durations remind accepts,
// such as "90s", "1h30m", "2d", and "1w2d".
package duration

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Day and Week are the units Parse adds to the standard library's.
const (
	Day  = 24 * time.Hour
	Week = 7 * Day
)

var units = map[byte]time.Duration{
	's': time.Second,
	'm': time.Minute,
	'h': time.Hour,
	'd': Day,
	'w': Week,
}

// Parse parses a sequence of whole numbers, each followed by a unit:
// s, m, h, d (24 hours), or w (7 days). "1h30m" is ninety minutes.
func Parse(s string) (time.Duration, error) {
	if last := s[len(s)-1]; last >= '0' && last <= '9' {
		return 0, fmt.Errorf("duration %q: missing unit", s)
	}
	var total time.Duration
	for rest := s; rest != ""; {
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("duration %q: expected a number at %q", s, rest)
		}
		n, err := strconv.Atoi(rest[:i])
		if err != nil {
			return 0, fmt.Errorf("duration %q: %w", s, err)
		}
		unit, ok := units[rest[i]]
		if !ok {
			return 0, fmt.Errorf("duration %q: unknown unit %q", s, rest[i])
		}
		total += time.Duration(n) * unit
		rest = rest[i+1:]
	}
	return total, nil
}

// Format formats d the way Parse reads it, largest units first.
// Parts smaller than a second are dropped.
func Format(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		size time.Duration
		name byte
	}{{Week, 'w'}, {Day, 'd'}, {time.Hour, 'h'}, {time.Minute, 'm'}, {time.Second, 's'}} {
		if n := d / u.size; n > 0 {
			b.WriteString(strconv.FormatInt(int64(n), 10))
			b.WriteByte(u.name)
			d -= n * u.size
		}
	}
	return b.String()
}
