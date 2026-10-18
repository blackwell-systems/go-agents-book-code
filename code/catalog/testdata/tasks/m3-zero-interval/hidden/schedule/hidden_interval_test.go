package schedule_test

import (
	"testing"
	"time"

	"example.com/remind/schedule"
)

func TestHiddenZeroInterval(t *testing.T) {
	for _, spec := range []string{"every 0m", "every 0s", "every 0h0m"} {
		if _, err := schedule.Parse(spec, nil, time.Now()); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", spec)
		}
	}
	if _, err := schedule.Parse("every 1m", nil, time.Now()); err != nil {
		t.Errorf("Parse(every 1m): %v", err)
	}
}
