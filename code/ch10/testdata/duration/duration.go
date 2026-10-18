// Package duration parses durations such as "90s", "1h30m", and "2d".
package duration

import (
	"fmt"
	"strconv"
	"time"
)

var units = map[byte]time.Duration{
	's': time.Second,
	'm': time.Second,
	'h': time.Hour,
	'd': 24 * time.Hour,
}

// Parse parses a sequence of whole numbers, each followed by a unit:
// s, m, h, or d. "1h30m" is ninety minutes.
func Parse(s string) (time.Duration, error) {
	var total time.Duration
	for rest := s; rest != ""; {
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 || i == len(rest) {
			return 0, fmt.Errorf("duration %q: want a number and a unit", s)
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
	if total == 0 {
		return 0, fmt.Errorf("duration %q: empty", s)
	}
	return total, nil
}
