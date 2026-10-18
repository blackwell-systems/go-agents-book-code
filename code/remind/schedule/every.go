package schedule

import (
	"encoding/json"
	"fmt"
	"time"

	"example.com/remind/duration"
)

// Every fires at a fixed interval after Start.
type Every struct {
	Start    time.Time
	Interval time.Duration
}

func parseEvery(s string, start time.Time) (Every, error) {
	d, err := duration.Parse(s)
	if err != nil {
		return Every{}, fmt.Errorf("schedule: %w", err)
	}
	return Every{Start: start, Interval: d}, nil
}

// Next returns the first multiple of the interval after `after`.
func (e Every) Next(after time.Time) time.Time {
	if after.Before(e.Start) {
		return e.Start
	}
	n := after.Sub(e.Start) / e.Interval
	return e.Start.Add((n + 1) * e.Interval)
}

func (e Every) String() string {
	return "every " + duration.Format(e.Interval)
}

// MarshalJSON writes the rule as its spec.
func (e Every) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(map[string]string{"spec": e.String()})
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return b, nil
}
