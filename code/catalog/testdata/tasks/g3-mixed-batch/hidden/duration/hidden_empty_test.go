package duration_test

import (
	"testing"

	"example.com/remind/duration"
)

func TestHiddenParseEmpty(t *testing.T) {
	for _, in := range []string{"", " "} {
		if _, err := duration.Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}
