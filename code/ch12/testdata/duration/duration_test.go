package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"90s", 90 * time.Second},
		{"45m", 45 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"2d", 48 * time.Hour},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Parse(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "1h30", "h", "10y"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}
