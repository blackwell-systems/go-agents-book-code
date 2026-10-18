// Package crash records crash reports: panics in HTTP handlers, and
// requests that stay stuck long enough to suggest a deadlock.
package crash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"time"
)

// A Report is one crash.
type Report struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "panic" or "stuck"
	Time      time.Time `json:"time"`
	Service   string    `json:"service"`
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	GoVersion string    `json:"go_version"`
	Panic     string    `json:"panic"`
	Stack     string    `json:"stack"`
	Request   Request   `json:"request"`
	Logs      []string  `json:"logs"`
}

// Request is the request being served when the crash happened.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body,omitempty"`
}

// maxBody is how much of a request body a report keeps.
const maxBody = 4 << 10

// Reporter writes crash reports as JSON lines.
type Reporter struct {
	Service string
	Version string
	Commit  string
	Now     func() time.Time
	Logs    *LogRing // recent log lines to attach; may be nil

	mu       sync.Mutex
	w        io.Writer
	seq      int
	inflight map[*inflight]struct{}
}

type inflight struct {
	start    time.Time
	req      Request
	body     *bytes.Buffer
	reported bool
}

// NewReporter returns a reporter that appends reports to w.
func NewReporter(w io.Writer, now func() time.Time) *Reporter {
	return &Reporter{
		Service:  "remind",
		Version:  "(devel)",
		Now:      now,
		w:        w,
		inflight: make(map[*inflight]struct{}),
	}
}

// Middleware recovers panics in next, writes a report for each, and
// answers 500. It also tracks requests in flight for Watch.
func (rep *Reporter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := &inflight{start: rep.Now(), req: requestOf(r), body: new(bytes.Buffer)}
		if r.Body != nil {
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.TeeReader(r.Body, &limitedWriter{f.body, maxBody}), r.Body}
		}
		rep.mu.Lock()
		rep.inflight[f] = struct{}{}
		rep.mu.Unlock()
		defer func() {
			rep.mu.Lock()
			delete(rep.inflight, f)
			rep.mu.Unlock()
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				req := f.req
				req.Body = f.body.String()
				rep.write("panic", fmt.Sprint(v), string(debug.Stack()), req)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Check writes a "stuck" report, with every goroutine's stack, for the
// oldest request that has been in flight longer than limit and hasn't
// been reported yet.
func (rep *Reporter) Check(limit time.Duration) bool {
	now := rep.Now()
	rep.mu.Lock()
	var oldest *inflight
	for f := range rep.inflight {
		if !f.reported && now.Sub(f.start) > limit && (oldest == nil || f.start.Before(oldest.start)) {
			oldest = f
		}
	}
	if oldest != nil {
		oldest.reported = true
	}
	rep.mu.Unlock()
	if oldest == nil {
		return false
	}
	var dump bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&dump, 2); err != nil {
		dump.WriteString(err.Error())
	}
	rep.write("stuck", fmt.Sprintf("request in flight for %s", now.Sub(oldest.start).Round(time.Second)), dump.String(), oldest.req)
	return true
}

func (rep *Reporter) write(kind, value, stack string, req Request) {
	rep.mu.Lock()
	defer rep.mu.Unlock()
	rep.seq++
	r := Report{
		ID:        fmt.Sprintf("crash-%06d", rep.seq),
		Kind:      kind,
		Time:      rep.Now(),
		Service:   rep.Service,
		Version:   rep.Version,
		Commit:    rep.Commit,
		GoVersion: runtime.Version(),
		Panic:     value,
		Stack:     stack,
		Request:   req,
	}
	if rep.Logs != nil {
		r.Logs = rep.Logs.Lines()
	}
	// A crash report that can't be written has nowhere else to go.
	_ = json.NewEncoder(rep.w).Encode(r)
}

func requestOf(r *http.Request) Request {
	req := Request{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: make(map[string]string, len(r.Header)),
	}
	for k, v := range r.Header {
		req.Headers[k] = v[0]
	}
	return req
}

// limitedWriter keeps the first n bytes written to it and drops the rest.
type limitedWriter struct {
	buf *bytes.Buffer
	n   int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.n - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
