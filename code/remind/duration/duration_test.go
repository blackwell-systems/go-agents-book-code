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
		{"1h30m", 90 * time.Minute},
		{"2d", 48 * time.Hour},
		{"1w2d", 9 * Day},
		{"0m", 0},
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
	for _, in := range []string{"1h30", "h", "10y", "1h-30m", "1.5h"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", in)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{90 * time.Minute, "1h30m"},
		{9 * Day, "1w2d"},
		{500 * time.Millisecond, "0s"},
	}
	for _, tt := range tests {
		if got := Format(tt.in); got != tt.want {
			t.Errorf("Format(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
