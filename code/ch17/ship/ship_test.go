package ship

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// collector is a stand-in service: it answers with the next status in
// script (202 when the script runs out) and keeps every body it took.
type collector struct {
	mu     sync.Mutex
	script []int
	got    []string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	status := http.StatusAccepted
	if len(c.script) > 0 {
		status, c.script = c.script[0], c.script[1:]
	}
	if status < 300 {
		c.got = append(c.got, string(b))
	}
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(status)
}

func (c *collector) bodies() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

func newShipper(t *testing.T, url string) *Shipper {
	t.Helper()
	dir := t.TempDir()
	return &Shipper{File: filepath.Join(dir, "crashes.jsonl"),
		Offset: filepath.Join(dir, "offset.json"), URL: url,
		Secret: "s", base: time.Millisecond,
		Log: slog.New(slog.DiscardHandler)}
}

func appendTo(t *testing.T, file, text string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func line(id string) string { return `{"id":"` + id + `"}` + "\n" }

func TestShipInOrderOnce(t *testing.T) {
	c := &collector{script: []int{http.StatusServiceUnavailable,
		http.StatusTooManyRequests}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	s := newShipper(t, srv.URL)
	ctx := t.Context()

	if err := s.drain(ctx); err != nil { // no file yet
		t.Fatal(err)
	}
	appendTo(t, s.File, line("a")+line("b")+`{"id":"c`) // c is half-written
	if err := s.drain(ctx); err != nil {
		t.Fatal(err)
	}
	appendTo(t, s.File, `"}`+"\n")
	if err := s.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.drain(ctx); err != nil { // nothing new
		t.Fatal(err)
	}
	want := []string{`{"id":"a"}`, `{"id":"b"}`, `{"id":"c"}`}
	if got := c.bodies(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("posted %q, want %q", got, want)
	}
}

// TestShipRestart: a new shipper on the same offset file goes on where
// the last one stopped.
func TestShipRestart(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	s := newShipper(t, srv.URL)
	appendTo(t, s.File, line("a"))
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	again := *s
	appendTo(t, s.File, line("b"))
	if err := again.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := c.bodies(); len(got) != 2 || got[1] != `{"id":"b"}` {
		t.Errorf("posted %q", got)
	}
}

// TestShipRotated replaces the file, as log rotation does: the shipper
// reads the new one from its start, even when it is longer than the
// old offset.
func TestShipRotated(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	s := newShipper(t, srv.URL)
	appendTo(t, s.File, line("crash-000001"))
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.File); err != nil {
		t.Fatal(err)
	}
	appendTo(t, s.File, line("crash-000001-again")+line("crash-000002"))
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := c.bodies(); len(got) != 3 {
		t.Errorf("posted %q, want 3 reports", got)
	}
}

func TestShipSkipsAndStops(t *testing.T) {
	c := &collector{script: []int{http.StatusBadRequest}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	s := newShipper(t, srv.URL)
	appendTo(t, s.File, "not json\n"+line("rejected")+line("ok"))
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := c.bodies(); len(got) != 1 || got[0] != `{"id":"ok"}` {
		t.Errorf("posted %q, want only ok", got)
	}

	c.mu.Lock()
	c.script = []int{http.StatusUnauthorized}
	c.mu.Unlock()
	appendTo(t, s.File, line("next"))
	if err := s.drain(t.Context()); !errors.Is(err, ErrRefused) {
		t.Errorf("drain with a wrong secret: %v, want ErrRefused", err)
	}
	if err := s.drain(t.Context()); err != nil { // the line wasn't lost
		t.Fatal(err)
	}
	if got := c.bodies(); got[len(got)-1] != `{"id":"next"}` {
		t.Errorf("posted %q", got)
	}
}

// TestShipWaitsForService: while the service is down, Run keeps the
// report and stops when its context ends, with its cause.
func TestShipWaitsForService(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens
	s := newShipper(t, srv.URL)
	appendTo(t, s.File, line("a"))
	stop := errors.New("test over")
	ctx, cancel := context.WithCancelCause(t.Context())
	time.AfterFunc(50*time.Millisecond, func() { cancel(stop) })
	if err := s.Run(ctx); !errors.Is(err, stop) {
		t.Errorf("Run = %v, want the context's cause", err)
	}
	var p position
	if err := readPos(s.Offset, &p); err != nil || p.Offset != 0 {
		t.Errorf("position %+v, %v: the report was counted as sent", p, err)
	}
}

type done struct{}

func (done) Triage(context.Context, service.Run, []triage.Report,
	agent.Approver, float64) (service.Outcome, error) {
	return service.Outcome{Status: "fixed"}, nil
}

// TestShipToService ships remind-style reports to the real service:
// the same IDs again after remind restarts are new reports, and
// shipping the file again after a lost position sends duplicates the
// service ignores.
func TestShipToService(t *testing.T) {
	const secret = "ingest-secret-0123456789abcdef0123"
	dir := t.TempDir()
	svc, err := service.New(context.Background(), service.Config{
		Dir: dir, Secret: secret, Triager: done{},
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	svc.Start()
	defer svc.Shutdown(context.Background()) //nolint:errcheck // test end
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	s := newShipper(t, srv.URL+"/crashes")
	s.Secret = secret
	report := func(id, at string) string {
		return fmt.Sprintf(`{"id":%q,"kind":"panic","commit":"9c41e7a",`+
			`"time":%q,"panic":"boom","stack":"goroutine 1 [running]:\n`+
			`example.com/remind/a.A()\n"}`+"\n", id, at)
	}
	appendTo(t, s.File, report("crash-000001", "2026-10-16T09:00:00Z")+
		report("crash-000001", "2026-10-16T11:00:00Z")) // after a restart
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Offset); err != nil { // the position is lost
		t.Fatal(err)
	}
	if err := s.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("the service holds %d reports, want 2", len(entries))
	}
}
