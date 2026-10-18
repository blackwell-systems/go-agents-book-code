package schedule

import (
	"testing"
	"time"
)

var start = time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC) // a Monday

func TestEvery(t *testing.T) {
	r, err := Parse("every 2h", nil, start)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		after, want time.Time
	}{
		{start.Add(-time.Hour), start},
		{start, start.Add(2 * time.Hour)},
		{start.Add(3 * time.Hour), start.Add(4 * time.Hour)},
	}
	for _, tt := range tests {
		if got := r.Next(tt.after); !got.Equal(tt.want) {
			t.Errorf("Next(%v) = %v, want %v", tt.after, got, tt.want)
		}
	}
	if got, want := r.String(), "every 2h"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestWeekly(t *testing.T) {
	r, err := Parse("on mon,wed at 09:30", UTC, start)
	if err != nil {
		t.Fatal(err)
	}
	sunday := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		after, want time.Time
	}{
		{sunday, time.Date(2026, 9, 7, 9, 30, 0, 0, time.UTC)},
		{start, time.Date(2026, 9, 7, 9, 30, 0, 0, time.UTC)},
		{time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC), time.Date(2026, 9, 9, 9, 30, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := r.Next(tt.after); !got.Equal(tt.want) {
			t.Errorf("Next(%v) = %v, want %v", tt.after, got, tt.want)
		}
	}
	if got, want := r.String(), "on mon,wed at 09:30"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{"", "daily", "every soon", "on mon", "on mon at 25:00", "on mon at 9:75"} {
		if _, err := Parse(spec, UTC, start); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", spec)
		}
	}
}

func TestLookupZone(t *testing.T) {
	if z := LookupZone("Europe/Paris"); z == nil || z.Name != "Europe/Paris" {
		t.Errorf("LookupZone(Europe/Paris) = %v", z)
	}
	if z := LookupZone(""); z != UTC {
		t.Errorf("LookupZone(\"\") = %v, want UTC", z)
	}
	if z := LookupZone("Mars/Olympus"); z != nil {
		t.Errorf("LookupZone(Mars/Olympus) = %v, want nil", z)
	}
}
