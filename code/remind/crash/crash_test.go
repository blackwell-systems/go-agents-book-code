package crash

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddlewareReportsPanics(t *testing.T) {
	var out bytes.Buffer
	ring := NewLogRing(slog.DiscardHandler, 5, time.Now)
	log := slog.New(ring)
	rep := NewReporter(&out, time.Now)
	rep.Logs = ring
	h := rep.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		log.Info("handling", "name", body.Name)
		panic("boom: " + body.Name)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/things?x=1", strings.NewReader(`{"Name":"ana"}`))
	req.Header.Set("User-Agent", "test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "panic" || r.Panic != "boom: ana" {
		t.Errorf("report kind %q panic %q", r.Kind, r.Panic)
	}
	if r.Request.Body != `{"Name":"ana"}` || r.Request.Query != "x=1" || r.Request.Headers["User-Agent"] != "test" {
		t.Errorf("request = %+v", r.Request)
	}
	if !strings.Contains(r.Stack, "TestMiddlewareReportsPanics") {
		t.Errorf("stack doesn't show the handler:\n%s", r.Stack)
	}
	if len(r.Logs) != 1 || !strings.Contains(r.Logs[0], "name=ana") {
		t.Errorf("logs = %q", r.Logs)
	}
}

func TestCheckReportsStuckRequests(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	rep := NewReporter(&out, func() time.Time { return now })
	release := make(chan struct{})
	started := make(chan struct{})
	h := rep.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}))
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/slow", nil))
		close(done)
	}()
	<-started
	if rep.Check(time.Minute) {
		t.Error("reported a request that isn't stuck yet")
	}
	now = now.Add(2 * time.Minute)
	if !rep.Check(time.Minute) {
		t.Error("didn't report a stuck request")
	}
	if rep.Check(time.Minute) {
		t.Error("reported the same request twice")
	}
	close(release)
	<-done
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "stuck" || r.Request.Path != "/slow" || !strings.Contains(r.Stack, "goroutine") {
		t.Errorf("report = %+v", r)
	}
}

func TestLogRingKeepsLastLines(t *testing.T) {
	ring := NewLogRing(slog.DiscardHandler, 2, time.Now)
	log := slog.New(ring).With("svc", "remind")
	for _, m := range []string{"one", "two", "three"} {
		log.Info(m, "n", 1)
	}
	got := ring.Lines()
	if len(got) != 2 || !strings.Contains(got[0], "INFO two svc=remind n=1") {
		t.Errorf("Lines = %q", got)
	}
}
