package duration_test

import (
	"testing"

	"example.com/remind/duration"
)

func TestHiddenMissingUnit(t *testing.T) {
	for _, in := range []string{"1h30", "45", "2d4"} {
		if _, err := duration.Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}
