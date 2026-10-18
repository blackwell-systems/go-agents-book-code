package agent

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// tag::retry-after[]

// RetryAfter reads a Retry-After header in either of its forms, a
// number of seconds or an HTTP date, as a wait from now. A wait that is
// negative, in the past, or unreadable is 0; one over a day is capped
// at a day, so the multiplication can't overflow (chapter 12).
func RetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(min(max(secs, 0), 86400)) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return min(max(t.Sub(now), 0), 24*time.Hour)
}

// end::retry-after[]
