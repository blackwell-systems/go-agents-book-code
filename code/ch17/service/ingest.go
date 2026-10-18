package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// maxReport is the largest report body accepted. remind keeps 4 KiB of
// a request body and a dozen log lines; a goroutine dump can be long,
// but not this long.
const maxReport = 256 << 10

// tag::wire[]

// A wireReport is a crash report as remind's crash package posts it.
// Fields it doesn't know are ignored, so remind can add one before the
// service reads it.
type wireReport struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Time      time.Time `json:"time"`
	Service   string    `json:"service"`
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	GoVersion string    `json:"go_version"`
	Panic     string    `json:"panic"`
	Stack     string    `json:"stack"`
	Request   struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Query   string            `json:"query"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Logs []string `json:"logs"`
}

// end::wire[]

var (
	reportID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	commit   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// tag::validate[]

// validate checks what the service relies on: an ID it can name a file
// after, a kind it knows, a stack to fingerprint, and the commit that
// crashed, which the run's workspace checks out.
func (w *wireReport) validate() error {
	switch {
	case !reportID.MatchString(w.ID):
		return errors.New("id: 1 to 64 letters, digits, _ or -")
	case w.Kind != "panic" && w.Kind != "stuck":
		return errors.New(`kind: "panic" or "stuck"`)
	case !commit.MatchString(w.Commit):
		return errors.New("commit: a hexadecimal commit hash")
	case strings.TrimSpace(w.Stack) == "":
		return errors.New("stack: empty")
	case w.Time.IsZero():
		return errors.New("time: missing")
	}
	return nil
}

// end::validate[]

// kept are the request headers whose values reach the disk, redacted;
// any other header keeps its name and loses its value, since a token
// in X-Auth-Token or Private-Token matches no pattern Redact knows.
// longToken matches a run of letters and digits long enough to be a
// token, which has no place in a header's name.
var longToken = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)

var kept = []string{"Accept", "Accept-Encoding", "Accept-Language",
	"Content-Length", "Content-Type", "User-Agent", "X-Request-Id"}

// tag::key[]

// key returns the ID the service files a report under. remind numbers
// its reports from 1 in each process, so its IDs repeat after every
// restart; the key adds a hash of the ID, the time, the build, and the
// service, which a retry of the same report repeats and a new report
// with a reused ID doesn't.
func (w *wireReport) key() string {
	h := sha256.New()
	at := w.Time.UTC().Format(time.RFC3339Nano)
	for _, s := range []string{w.ID, at, w.Commit, w.Service} {
		fmt.Fprintf(h, "%d:%s", len(s), s)
	}
	return w.ID + "-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// end::key[]

// tag::redact[]

// report converts a validated report to the agent's form, redacting
// credentials before it is written anywhere (chapter 7's Redact).
func (w *wireReport) report() triage.Report {
	r := triage.Report{ID: w.key(), Version: triage.Redact(w.Version),
		Commit: w.Commit, Kind: w.Kind, Panic: triage.Redact(w.Panic),
		Stack: triage.Redact(w.Stack), FirstSeen: w.Time,
		LastSeen: w.Time, Count: 1}
	r.Request = triage.Request{Method: w.Request.Method,
		Path:    triage.Redact(w.Request.Path),
		Query:   triage.Redact(w.Request.Query),
		Body:    triage.Redact(w.Request.Body),
		Headers: map[string]string{}}
	for k, v := range w.Request.Headers {
		if len(k) > 64 || triage.Redact(k) != k ||
			longToken.MatchString(k) {
			continue // a name that may itself be a credential
		}
		r.Request.Headers[k] = "[REDACTED]"
		if slices.ContainsFunc(kept, func(h string) bool {
			return strings.EqualFold(h, k)
		}) { // with its name, so patterns like "cookie: ..." match
			red := triage.Redact(k + ": " + v)
			r.Request.Headers[k] = strings.TrimPrefix(red, k+": ")
		}
	}
	for _, l := range w.Logs {
		r.Logs = append(r.Logs, triage.Redact(l))
	}
	return r
}

// end::redact[]

// tag::secret[]

// sameSecret compares a presented secret with the real one in constant
// time. Hashing both first makes the time independent of the lengths
// too.
func sameSecret(got, want string) bool {
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// bearer returns the token in a request's Authorization header.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return ""
	}
	return tok
}

// end::secret[]

// tag::limiter[]

// A limiter allows each source a number of reports per window. The map
// starts over each window, so it holds at most one window's sources.
type limiter struct {
	per    int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	start time.Time
	seen  map[string]int
}

// allow reports whether the source may post now, and if not, how long
// until it may.
func (l *limiter) allow(source string) (bool, time.Duration) {
	if l.per <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.start) >= l.window || l.seen == nil {
		l.start, l.seen = now, map[string]int{}
	}
	if l.seen[source] >= l.per {
		return false, l.start.Add(l.window).Sub(now)
	}
	l.seen[source]++
	return true, 0
}

// end::limiter[]

// source names who sent a request: its address without the port. A
// proxy in front of the service makes every report one source; trust
// its forwarding header only if the proxy sets it.
func source(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// retryAfter formats a wait for the Retry-After header, in whole
// seconds, at least one.
func retryAfter(d time.Duration) string {
	return fmt.Sprint(max(1, int((d+time.Second-1)/time.Second)))
}
