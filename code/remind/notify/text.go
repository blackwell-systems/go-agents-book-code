package notify

import (
	"strconv"
	"strings"
	"time"

	"example.com/remind/store"
)

// previewLen is how much of a reminder fits in a notification's subject.
const previewLen = 60

// Preview shortens text to fit a notification's subject line.
func Preview(text string) string {
	if len(text) <= previewLen {
		return text
	}
	return string([]rune(text)[:previewLen]) + "…"
}

// A Digest lists a user's reminders day by day.
type Digest struct {
	Days       []DigestDay `json:"days"`
	QuietHours []int       `json:"quiet_hours,omitempty"` // hours of the day when nothing is sent
}

// QuietHours lists the hours of the day from the start of the quiet
// period up to its end, such as 22, 23, 0, ... 6 for "22:00" to "07:00".
func QuietHours(from, to string) []int {
	var quiet [24]bool
	for h := hourOf(from); h != hourOf(to); h = (h + 1) % 24 {
		quiet[h] = true
	}
	var hours []int
	for h, q := range quiet {
		if q {
			hours = append(hours, h)
		}
	}
	return hours
}

// hourOf returns the hour of a time of day such as "07:30".
func hourOf(s string) int {
	h, _ := strconv.Atoi(strings.SplitN(s, ":", 2)[0])
	return h
}

// A DigestDay is one day of a digest.
type DigestDay struct {
	Date  string             `json:"date"`
	Items []store.Occurrence `json:"items"`
}

// BuildDigest groups occurrences by day in loc, for the given number of
// days starting at from. Occurrences must be in [from, from+days).
func BuildDigest(from time.Time, days int, loc *time.Location, occ []store.Occurrence) Digest {
	from = from.In(loc)
	d := Digest{Days: make([]DigestDay, days)}
	for i := range d.Days {
		d.Days[i].Date = from.AddDate(0, 0, i).Format(time.DateOnly)
	}
	for _, o := range occ {
		i := daysBetween(from, o.At.In(loc))
		if i >= days {
			continue
		}
		d.Days[i].Items = append(d.Days[i].Items, o)
	}
	return d
}

// daysBetween counts calendar days from a to b, both in the same zone.
func daysBetween(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}
