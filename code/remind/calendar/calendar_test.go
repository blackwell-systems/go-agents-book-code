package calendar

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var stamp = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, events []Event) string {
	t.Helper()
	var b strings.Builder
	cw := Writer{Now: func() time.Time { return stamp }}
	if err := cw.Write(&b, "-//remind//EN", events); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// unfold joins folded lines back into content lines.
func unfold(s string) []string {
	s = strings.ReplaceAll(s, "\r\n ", "")
	return strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n")
}

func TestWriteGolden(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	events := []Event{
		{
			UID:      "rem_0001@remind",
			Summary:  "Call the dentist",
			Start:    time.Date(2026, 10, 12, 11, 30, 0, 0, cest),
			Duration: 15 * time.Minute,
		},
		{
			UID:         "rem_0002@remind",
			Summary:     "Stand up",
			Description: "Daily; bring notes",
			Start:       time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC),
			Duration:    90 * time.Minute,
			RRule:       WeeklyRRule([]time.Weekday{time.Monday, time.Wednesday}),
		},
	}
	want := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//remind//EN",
		"CALSCALE:GREGORIAN",
		"BEGIN:VEVENT",
		"UID:rem_0001@remind",
		"DTSTAMP:20261015T120000Z",
		"DTSTART:20261012T093000Z",
		"DURATION:PT15M",
		"SUMMARY:Call the dentist",
		"END:VEVENT",
		"BEGIN:VEVENT",
		"UID:rem_0002@remind",
		"DTSTAMP:20261015T120000Z",
		"DTSTART:20261013T070000Z",
		"DURATION:PT1H30M",
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE",
		"SUMMARY:Stand up",
		`DESCRIPTION:Daily\; bring notes`,
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")
	if got := write(t, events); got != want {
		t.Errorf("calendar:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteMissingFields(t *testing.T) {
	start := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for _, e := range []Event{{Start: start}, {UID: "x"}} {
		var b strings.Builder
		if err := Write(&b, "-//remind//EN", []Event{e}); err == nil {
			t.Errorf("Write(%+v) succeeded, want an error", e)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteError(t *testing.T) {
	err := Write(failWriter{}, "-//remind//EN", nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("err = %v, want the writer's error", err)
	}
}

func TestEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{`C:\notes`, `C:\\notes`},
		{"milk; eggs, bread", `milk\; eggs\, bread`},
		{"line one\nline two", `line one\nline two`},
		{"windows\r\nline", `windows\nline`},
		{"pão, café; chá", `pão\, café\; chá`},
	}
	for _, tt := range tests {
		if got := escape(tt.in); got != tt.want {
			t.Errorf("escape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFold(t *testing.T) {
	// "SUMMARY:" is 8 octets, so the 2-octet "é" would straddle octet 75.
	boundary := strings.Repeat("a", 66) + "é" + strings.Repeat("b", 10)
	tests := []string{
		"short",
		strings.Repeat("x", 67),
		strings.Repeat("x", 68),
		boundary,
		strings.Repeat("日本語のメモ", 30),
		strings.Repeat("🦷 dentist ", 20),
	}
	for _, summary := range tests {
		out := write(t, []Event{{
			UID:     "u",
			Summary: summary,
			Start:   stamp,
		}})
		for l := range strings.SplitSeq(strings.TrimSuffix(out, "\r\n"), "\r\n") {
			if len(l) > maxLine {
				t.Errorf("line of %d octets: %q", len(l), l)
			}
			if !utf8.ValidString(l) {
				t.Errorf("line splits a UTF-8 sequence: %q", l)
			}
		}
		lines := unfold(out)
		if got := lines[len(lines)-3]; got != "SUMMARY:"+summary {
			t.Errorf("unfolded summary = %q, want %q", got, summary)
		}
	}

	out := write(t, []Event{{UID: "u", Summary: boundary, Start: stamp}})
	if want := "SUMMARY:" + strings.Repeat("a", 66) + "\r\n é"; !strings.Contains(out, want) {
		t.Errorf("fold at boundary:\n%s\nwant it to contain %q", out, want)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "PT0S"},
		{15 * time.Minute, "PT15M"},
		{90 * time.Minute, "PT1H30M"},
		{time.Hour + 5*time.Second, "PT1H0M5S"},
		{24 * time.Hour, "P1D"},
		{26 * time.Hour, "P1DT2H"},
		{-10 * time.Minute, "-PT10M"},
		{1500 * time.Millisecond, "PT1S"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestWeeklyRRule(t *testing.T) {
	tests := []struct {
		days []time.Weekday
		want string
	}{
		{nil, "FREQ=WEEKLY"},
		{[]time.Weekday{time.Monday, time.Wednesday}, "FREQ=WEEKLY;BYDAY=MO,WE"},
		{[]time.Weekday{time.Sunday}, "FREQ=WEEKLY;BYDAY=SU"},
		{[]time.Weekday{time.Friday, time.Tuesday, time.Friday}, "FREQ=WEEKLY;BYDAY=FR,TU"},
	}
	for _, tt := range tests {
		if got := WeeklyRRule(tt.days); got != tt.want {
			t.Errorf("WeeklyRRule(%v) = %q, want %q", tt.days, got, tt.want)
		}
	}
}

func TestIntervalRRule(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Hour, "FREQ=HOURLY;INTERVAL=2"},
		{time.Hour, "FREQ=HOURLY"},
		{90 * time.Minute, "FREQ=MINUTELY;INTERVAL=90"},
		{48 * time.Hour, "FREQ=DAILY;INTERVAL=2"},
		{36 * time.Hour, "FREQ=HOURLY;INTERVAL=36"},
		{14 * 24 * time.Hour, "FREQ=WEEKLY;INTERVAL=2"},
		{45 * time.Second, "FREQ=SECONDLY;INTERVAL=45"},
		{0, ""},
		{-time.Hour, ""},
		{1500 * time.Millisecond, ""},
	}
	for _, tt := range tests {
		if got := IntervalRRule(tt.d); got != tt.want {
			t.Errorf("IntervalRRule(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
