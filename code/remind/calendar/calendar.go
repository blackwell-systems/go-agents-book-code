// Package calendar writes reminders as iCalendar (RFC 5545) files that
// calendar apps can import or subscribe to.
package calendar

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLine is the longest content line, in octets, before it is folded.
const maxLine = 75

// utcFormat is the iCalendar form of a UTC date-time.
const utcFormat = "20060102T150405Z"

// An Event is one calendar entry.
type Event struct {
	UID         string
	Summary     string
	Description string
	Start       time.Time
	Duration    time.Duration
	RRule       string // a recurrence rule such as "FREQ=DAILY", or empty
}

// A Writer writes calendars. The zero value stamps events with the
// current time.
type Writer struct {
	Now func() time.Time // the DTSTAMP clock; nil means time.Now
}

// Write writes events as a calendar, using the current time as each
// event's DTSTAMP.
func Write(w io.Writer, prodID string, events []Event) error {
	return Writer{}.Write(w, prodID, events)
}

// Write writes events to w as a VCALENDAR identified by prodID.
// Every event needs a UID and a start time.
func (cw Writer) Write(w io.Writer, prodID string, events []Event) error {
	now := time.Now
	if cw.Now != nil {
		now = cw.Now
	}
	stamp := now().UTC().Format(utcFormat)

	var b strings.Builder
	line(&b, "BEGIN:VCALENDAR")
	line(&b, "VERSION:2.0")
	line(&b, "PRODID:"+escape(prodID))
	line(&b, "CALSCALE:GREGORIAN")
	for i, e := range events {
		if e.UID == "" {
			return fmt.Errorf("calendar: event %d has no UID", i)
		}
		if e.Start.IsZero() {
			return fmt.Errorf("calendar: event %s has no start time", e.UID)
		}
		line(&b, "BEGIN:VEVENT")
		line(&b, "UID:"+escape(e.UID))
		line(&b, "DTSTAMP:"+stamp)
		line(&b, "DTSTART:"+e.Start.UTC().Format(utcFormat))
		line(&b, "DURATION:"+formatDuration(e.Duration))
		if e.RRule != "" {
			line(&b, "RRULE:"+e.RRule)
		}
		line(&b, "SUMMARY:"+escape(e.Summary))
		if e.Description != "" {
			line(&b, "DESCRIPTION:"+escape(e.Description))
		}
		line(&b, "END:VEVENT")
	}
	line(&b, "END:VCALENDAR")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write calendar: %w", err)
	}
	return nil
}

// line writes one content line, folding it into lines of at most
// maxLine octets. Continuation lines start with a space.
func line(b *strings.Builder, s string) {
	limit := maxLine
	for len(s) > limit {
		cut := limit
		for !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = maxLine - 1
	}
	b.WriteString(s)
	b.WriteString("\r\n")
}

var textEscaper = strings.NewReplacer(
	`\`, `\\`,
	";", `\;`,
	",", `\,`,
	"\r\n", `\n`,
	"\n", `\n`,
	"\r", `\n`,
)

// escape escapes a TEXT property value.
func escape(s string) string {
	return textEscaper.Replace(s)
}

// formatDuration formats d as an iCalendar duration such as "PT15M" or
// "P1DT2H". Fractions of a second are dropped.
func formatDuration(d time.Duration) string {
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	b.WriteByte('P')
	secs := int64(d / time.Second)
	days := secs / 86400
	parts := [3]int64{secs % 86400 / 3600, secs % 3600 / 60, secs % 60}
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}

	// RFC 5545 doesn't allow gaps (hours then seconds without minutes),
	// so write every unit from the first nonzero one to the last.
	first, last := -1, -1
	for i, p := range parts {
		if p != 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		if days == 0 {
			b.WriteString("T0S")
		}
		return b.String()
	}
	b.WriteByte('T')
	for i := first; i <= last; i++ {
		fmt.Fprintf(&b, "%d%c", parts[i], "HMS"[i])
	}
	return b.String()
}
