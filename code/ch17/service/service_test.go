package service

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

const (
	secret   = "ingest-secret-0123456789abcdef0123"
	revToken = "review-token-ana-0123456789abcdef0"
)

// clock is a test's time, moved by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// triager is a scripted Triager: each call runs the test's function,
// and every call is sent on calls, so a test can wait for one.
type triager struct {
	fn func(ctx context.Context, run Run, ap agent.Approver,
		maxCost float64) (Outcome, error)
	calls chan Run
}

func (f *triager) Triage(ctx context.Context, run Run,
	_ []triage.Report, ap agent.Approver, maxCost float64) (Outcome,
	error) {
	f.calls <- run
	return f.fn(ctx, run, ap, maxCost)
}

func newTriager(fn func(ctx context.Context, run Run, ap agent.Approver,
	maxCost float64) (Outcome, error)) *triager {
	return &triager{fn: fn, calls: make(chan Run, 100)}
}

// fixed answers every run with an issue at once.
func fixed() *triager {
	return newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		return Outcome{Status: "fixed", Issue: 7, Cost: 0.01}, nil
	})
}

// issues is a fake tracker for the service's own lookups and comments.
type issues struct {
	gated    bool
	mu       sync.Mutex
	open     map[string]int // fingerprint to open issue
	comments map[string]string
}

func (i *issues) IsOpen(_ context.Context, id int) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, open := range i.open {
		if open == id {
			return true, nil
		}
	}
	return false, nil
}

func (i *issues) Open(_ context.Context, fp string) (int, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	id, ok := i.open[fp]
	return id, ok, nil
}

// Comment asks ap first when gated is set, as the real one's gate does.
func (i *issues) Comment(ctx context.Context, ap agent.Approver, id int,
	key, body string) error {
	if i.gated {
		req := agent.ApprovalRequest{Call: agent.ToolCall{ID: key,
			Name: "issue_comment"}, Why: "comments", Evidence: body,
			Hash: key + body}
		d, err := ap.Decide(ctx, req)
		if errors.Is(err, agent.ErrNoDecision) {
			return &agent.ApprovalPending{
				Requests: []agent.ApprovalRequest{req}}
		}
		if err != nil {
			return fmt.Errorf("decide: %w", err)
		}
		if !d.Approved {
			return fmt.Errorf("%w: %s", ErrDeclined, d.Reason)
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.comments == nil {
		i.comments = map[string]string{}
	}
	i.comments[key] = fmt.Sprintf("#%d %s", id, body)
	return nil
}

func (i *issues) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.comments)
}

type env struct {
	t   *testing.T
	cfg Config
	svc *Service
	clk *clock
}

func newEnv(t *testing.T, tr Triager, edit func(*Config)) *env {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	cfg := Config{Dir: t.TempDir(), Secret: secret,
		Reviewers: map[string]string{revToken: "ana"}, Triager: tr,
		Issues: &issues{}, RunCost: 0.25, Sweep: time.Hour, Now: clk.Now,
		Log: slog.New(slog.DiscardHandler)}
	if edit != nil {
		edit(&cfg)
	}
	e := &env{t: t, cfg: cfg, clk: clk}
	e.open()
	return e
}

// open starts a service on the env's state directory, as a process
// starting does.
func (e *env) open() {
	e.t.Helper()
	svc, err := New(context.Background(), e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	svc.Start()
	e.svc = svc
	e.t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
}

// restart stops the service and opens it again on the same directory.
func (e *env) restart() {
	e.t.Helper()
	if err := e.svc.Shutdown(e.t.Context()); err != nil {
		e.t.Fatal(err)
	}
	e.open()
}

// report returns a crash report whose top frame in remind is fn, so
// reports with the same fn share a fingerprint.
func report(id, fn string) map[string]any {
	return map[string]any{
		"id": id, "kind": "panic", "service": "remind",
		"version": "v1.4.0", "commit": "9c41e7a",
		"time":  "2026-10-16T08:59:00Z",
		"panic": "runtime error: index out of range [3] with length 2",
		"stack": "goroutine 7 [running]:\nexample.com/remind/" + fn +
			"(0x1)\n\t/app/remind/x.go:10 +0x1\n",
		"request": map[string]any{"method": "GET", "path": "/v1/reminders",
			"headers": map[string]string{"User-Agent": "test"}},
	}
}

func (e *env) do(method, path, token string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, ok := body.([]byte)
		if !ok {
			var err error
			if b, err = json.Marshal(body); err != nil {
				e.t.Fatal(err)
			}
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequestWithContext(e.t.Context(), method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.svc.Handler().ServeHTTP(w, req)
	return w
}

func (e *env) post(rep map[string]any) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/crashes", secret, rep)
}

// wait waits for the run to reach state, or fails the test.
func (e *env) wait(id string, state RunState) Run {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.svc.mu.Lock()
		r, ok := e.svc.runs[id]
		var got Run
		if ok {
			got = *r
		}
		e.svc.mu.Unlock()
		if ok && got.State == state && !e.isRunning(id) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("run %s never reached %s", id, state)
	return Run{}
}

func (e *env) isRunning(id string) bool {
	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()
	return e.svc.running[id]
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return v
}

// fakeGitLab is a made-up GitLab token for the redaction test, split
// in two so secret scanners don't flag the source.
const fakeGitLab = "glpat-" + "x8Yz1aB2cD3eF4gH5iJ6"

func TestIngest(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	good := report("cr_000001", "store.(*Store).List")
	tests := []struct {
		name   string
		token  string
		body   any
		status int
	}{
		{"no secret", "", good, http.StatusUnauthorized},
		{"wrong secret", "wrong", good, http.StatusUnauthorized},
		{"reviewer token", revToken, good, http.StatusUnauthorized},
		{"not JSON", secret, []byte("{"), http.StatusBadRequest},
		{"too large", secret, bytes.Repeat([]byte(" "), maxReport+1),
			http.StatusRequestEntityTooLarge},
		{"bad id", secret, with(good, "id", "../../etc/x"),
			http.StatusBadRequest},
		{"bad kind", secret, with(good, "kind", "oops"),
			http.StatusBadRequest},
		{"bad commit", secret, with(good, "commit", "HEAD"),
			http.StatusBadRequest},
		{"no stack", secret, with(good, "stack", " "),
			http.StatusBadRequest},
		{"good", secret, good, http.StatusAccepted},
		{"again", secret, good, http.StatusOK},
	}
	for _, tt := range tests {
		if w := e.do(http.MethodPost, "/crashes", tt.token,
			tt.body); w.Code != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.name, w.Code, w.Body,
				tt.status)
		}
	}
	if n := len(e.svc.snapshot()); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
}

// keyOf returns the ID the service files a report under.
func keyOf(t *testing.T, rep map[string]any) string {
	t.Helper()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var wr wireReport
	if err := json.Unmarshal(b, &wr); err != nil {
		t.Fatal(err)
	}
	return wr.key()
}

// TestRepeatedIDs posts two reports with remind's ID from before and
// after a restart, which start numbering again at 1: both are kept.
// The same report posted again is still a duplicate.
func TestRepeatedIDs(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	first := report("crash-000001", "a.A")
	again := with(first, "time", "2026-10-16T10:30:00Z")
	for _, tt := range []struct {
		rep    map[string]any
		status int
	}{
		{first, http.StatusAccepted},
		{first, http.StatusOK},
		{again, http.StatusAccepted},
	} {
		if w := e.post(tt.rep); w.Code != tt.status {
			t.Errorf("%v: %d, want %d", tt.rep["time"], w.Code, tt.status)
		}
	}
	for _, rep := range []map[string]any{first, again} {
		if !exists(filepath.Join(e.cfg.Dir, reportsDir,
			keyOf(t, rep)+".json")) {
			t.Errorf("report at %v not saved", rep["time"])
		}
	}
}

func with(m map[string]any, k string, v any) map[string]any {
	out := maps.Clone(m)
	out[k] = v
	return out
}

func TestRedactedOnDisk(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	rep := report("cr_000002", "store.(*Store).List")
	rep["panic"] = "bad token rk_live_abc123def456"
	rep["logs"] = []string{"auth: Bearer abcdefgh12345678 rejected"}
	rep["request"] = map[string]any{"method": "GET", "path": "/v1/me",
		"query": "access_token=q1w2e3r4t5y6u7i8o9p0",
		"headers": map[string]string{"Authorization": "Bearer s3cr3tt0ken99",
			"Cookie":                                 "session=abc123",
			"Private-Token":                          fakeGitLab,
			"Api-Key":                                "live_51HxQ2eZvKYlo2C9",
			"X-Auth-Token":                           "a1b2c3d4e5f60718293a4b5c6d7e8f90",
			"User-Agent":                             "probe/1 token=z9y8x7w6v5u4t3s2r1",
			"X-Key-c0ffee0123456789abcdef0123456789": "x"}}
	if w := e.post(rep); w.Code != http.StatusAccepted {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(filepath.Join(e.cfg.Dir, reportsDir,
		keyOf(t, rep)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"rk_live_abc123", "abcdefgh12345678",
		"s3cr3tt0ken99", "session=abc123", fakeGitLab,
		"live_51HxQ2eZvKYlo2C9", "a1b2c3d4e5f60718293a4b5c6d7e8f90",
		"q1w2e3r4t5y6u7i8o9p0", "z9y8x7w6v5u4t3s2r1",
		"c0ffee0123456789abcdef"} {
		if bytes.Contains(b, []byte(s)) {
			t.Errorf("%q reached the disk: %s", s, b)
		}
	}
}

// TestGroupAtOnce posts reports of one new group from many clients at
// once: one run, and every report in it.
func TestGroupAtOnce(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		<-release
		return Outcome{Status: "fixed", Issue: 3}, nil
	})
	e := newEnv(t, tr, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			rep := report(fmt.Sprintf("cr_10000%d", i), "api.(*API).list")
			if w := e.post(rep); w.Code != http.StatusAccepted {
				t.Errorf("%d %s", w.Code, w.Body)
			}
		})
	}
	wg.Wait()
	close(release)
	runs := e.svc.snapshot()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := e.wait(runs[0].ID, Done)
	if n := len(r.Reports) + len(r.Late); n != 8 {
		t.Errorf("%d reports in the run, want 8", n)
	}
}

func TestQueueFull(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		<-release
		return Outcome{}, nil
	})
	e := newEnv(t, tr, func(c *Config) { c.Slots, c.MaxQueued = 1, 1 })
	defer close(release)
	e.post(report("cr_200001", "a.A"))
	<-tr.calls
	if w := e.post(report("cr_200002", "b.B")); w.Code != http.StatusAccepted {
		t.Fatalf("second group: %d", w.Code)
	}
	refused := report("cr_200003", "c.C")
	w := e.post(refused)
	if w.Code != http.StatusServiceUnavailable ||
		w.Header().Get("Retry-After") == "" {
		t.Errorf("third group: %d, Retry-After %q", w.Code,
			w.Header().Get("Retry-After"))
	}
	if w := e.post(report("cr_200004", "b.B")); w.Code != http.StatusAccepted {
		t.Errorf("a report for a queued group: %d", w.Code)
	}
	if exists(filepath.Join(e.cfg.Dir, reportsDir,
		keyOf(t, refused)+".json")) {
		t.Error("a refused report was saved")
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, fixed(), func(c *Config) { c.PerMinute = 2 })
	for i := range 2 {
		e.post(report(fmt.Sprintf("cr_30000%d", i), "a.A"))
	}
	w := e.post(report("cr_300009", "a.A"))
	if w.Code != http.StatusTooManyRequests ||
		w.Header().Get("Retry-After") != "60" {
		t.Errorf("third report: %d, Retry-After %q", w.Code,
			w.Header().Get("Retry-After"))
	}
	e.clk.Add(time.Minute)
	if w := e.post(report("cr_300009", "a.A")); w.Code == http.StatusTooManyRequests {
		t.Error("still limited after the window")
	}
}

// TestRequestContext cancels the request that started a run: the run
// goes on, because its context comes from the service's root.
func TestRequestContext(t *testing.T) {
	check := make(chan error, 1)
	release := make(chan struct{})
	tr := newTriager(func(ctx context.Context, _ Run, _ agent.Approver,
		_ float64) (Outcome, error) {
		<-release
		check <- ctx.Err()
		return Outcome{}, nil
	})
	e := newEnv(t, tr, nil)
	ctx, cancel := context.WithCancel(t.Context())
	b, _ := json.Marshal(report("cr_400001", "a.A"))
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/crashes",
		bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+secret)
	e.svc.Handler().ServeHTTP(httptest.NewRecorder(), req)
	<-tr.calls
	cancel() // the client hangs up
	close(release)
	if err := <-check; err != nil {
		t.Errorf("the run's context ended with the request: %v", err)
	}
}

// TestShutdownResume shuts the service down mid-run: the run sees
// ErrShutdown as its cause, stays queued on disk, and the next process
// resumes it.
func TestShutdownResume(t *testing.T) {
	var mu sync.Mutex
	causes := []error{}
	tr := newTriager(func(ctx context.Context, _ Run, _ agent.Approver,
		_ float64) (Outcome, error) {
		mu.Lock()
		first := len(causes) == 0
		mu.Unlock()
		if !first {
			return Outcome{Status: "fixed", Issue: 4}, nil
		}
		<-ctx.Done()
		mu.Lock()
		causes = append(causes, context.Cause(ctx))
		mu.Unlock()
		return Outcome{Cost: 0.02}, ctx.Err()
	})
	e := newEnv(t, tr, nil)
	e.post(report("cr_500001", "a.A"))
	first := <-tr.calls
	if err := e.svc.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Queued again, but its agent has begun: a report now is late.
	if got := decode[routed](t, e.post(report("cr_500002",
		"a.A"))); !got.Late {
		t.Errorf("report for an interrupted run routed %+v", got)
	}
	e.open()
	mu.Lock()
	if len(causes) != 1 || !errors.Is(causes[0], agent.ErrShutdown) {
		t.Errorf("causes %v, want ErrShutdown", causes)
	}
	mu.Unlock()
	again := <-tr.calls
	if again.ID != first.ID {
		t.Errorf("resumed %s, want %s", again.ID, first.ID)
	}

	if r := e.wait(first.ID, Done); r.Cost != 0.02 || r.Issue != 4 {
		t.Errorf("run %+v", r)
	}
}

// pauser pauses on its first call with one request of kind k, and on
// later calls returns what the approver decides.
func pauser(k agent.ApprovalKind) (*triager, chan agent.ApprovalDecision) {
	got := make(chan agent.ApprovalDecision, 10)
	req := agent.ApprovalRequest{Kind: k, Run: "triage",
		Call: agent.ToolCall{ID: "call_1", Name: "issue_file",
			Arguments: []byte(`{"title":"x"}`)},
		Why: "files an issue", Evidence: "the issue text", Hash: "h1"}
	tr := newTriager(func(ctx context.Context, _ Run, ap agent.Approver,
		_ float64) (Outcome, error) {
		d, err := ap.Decide(ctx, req)
		if errors.Is(err, agent.ErrNoDecision) {
			return Outcome{}, &agent.ApprovalPending{
				Requests: []agent.ApprovalRequest{req}}
		}
		if err != nil {
			return Outcome{}, fmt.Errorf("decide: %w", err)
		}
		got <- d
		return Outcome{Status: "fixed"}, nil
	})
	return tr, got
}

// TestPausedRestart kills the service with an approval pending; after
// the restart, a reviewer approves, and the run resumes with the
// decision.
func TestPausedRestart(t *testing.T) {
	tr, got := pauser(agent.KindApproval)
	e := newEnv(t, tr, nil)
	e.post(report("cr_600001", "a.A"))
	run := (<-tr.calls).ID
	e.wait(run, Paused)
	e.restart()
	if w := e.do(http.MethodGet, "/review", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("review list without a token: %d", w.Code)
	}
	items := decode[[]Item](t, e.do(http.MethodGet, "/review", revToken, nil))
	if len(items) != 1 || items[0].Run != run || items[0].Tool != "issue_file" {
		t.Fatalf("review items %+v", items)
	}
	e.svc.sweep()
	if r := e.wait(run, Paused); r.State != Paused {
		t.Fatal("resumed with no decision")
	}
	w := e.do(http.MethodPost, "/review/"+items[0].ID+"/approve", revToken,
		map[string]string{})
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	<-tr.calls
	d := <-got
	if !d.Approved || d.By != "ana" || d.Hash != "h1" {
		t.Errorf("decision %+v", d)
	}
	e.wait(run, Done)
	w = e.do(http.MethodPost, "/review/"+items[0].ID+"/deny", revToken,
		map[string]string{"reason": "no"})
	if w.Code != http.StatusConflict {
		t.Errorf("second decision: %d", w.Code)
	}
}

// TestMissedDeadline lets an approval's deadline pass: the run resumes
// with a denial, and a late decision is refused.
func TestMissedDeadline(t *testing.T) {
	tr, got := pauser(agent.KindApproval)
	e := newEnv(t, tr, func(c *Config) { c.ReviewWait = time.Hour })
	e.post(report("cr_700001", "a.A"))
	run := (<-tr.calls).ID
	e.wait(run, Paused)
	items := e.svc.review.open()
	e.clk.Add(time.Hour)
	e.svc.sweep()
	<-tr.calls
	if d := <-got; d.Approved || d.By != "deadline" || d.Hash != "h1" {
		t.Errorf("decision %+v, want a denial by the deadline", d)
	}
	e.wait(run, Done)
	w := e.do(http.MethodPost, "/review/"+items[0].ID+"/approve", revToken,
		map[string]string{})
	if w.Code != http.StatusConflict {
		t.Errorf("approve after the deadline: %d", w.Code)
	}
}

// TestUnknownOutcome: an unknown outcome has no deadline, and denying
// it ("it took effect") needs a reason.
func TestUnknownOutcome(t *testing.T) {
	tr, got := pauser(agent.KindUnknownOutcome)
	e := newEnv(t, tr, func(c *Config) { c.ReviewWait = time.Hour })
	e.post(report("cr_800001", "a.A"))
	run := (<-tr.calls).ID
	e.wait(run, Paused)
	if it := e.svc.review.open(); len(it) != 1 || it[0].Stale {
		t.Fatalf("items %+v, want one, not stale", it)
	}
	e.clk.Add(48 * time.Hour)
	e.svc.sweep()
	items := e.svc.review.open()
	if len(items) != 1 || items[0].Kind != KindOutcome || !items[0].Stale {
		t.Fatalf("items %+v, want one stale unknown outcome", items)
	}
	st := decode[Status](t, e.do(http.MethodGet, "/runs/"+run, revToken,
		nil))
	if st.State != Paused || st.Waiting != 1 || st.Stale != 1 {
		t.Errorf("status %+v, want paused with 1 stale item", st)
	}
	if w := e.do(http.MethodPost, "/review/"+items[0].ID+"/approve",
		revToken, map[string]string{}); w.Code != http.StatusConflict {
		t.Errorf("approve on an unknown outcome: %d, want 409", w.Code)
	}
	path := "/review/" + items[0].ID + "/done"
	if w := e.do(http.MethodPost, path, revToken,
		map[string]string{}); w.Code != http.StatusBadRequest {
		t.Errorf("deny without a reason: %d", w.Code)
	}
	if w := e.do(http.MethodPost, path, revToken,
		map[string]string{"reason": "filed as #12"}); w.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", w.Code, w.Body)
	}
	<-tr.calls
	if d := <-got; d.Approved || d.Reason != "filed as #12" {
		t.Errorf("decision %+v", d)
	}
}

func TestShutdownWhilePaused(t *testing.T) {
	tr, _ := pauser(agent.KindApproval)
	e := newEnv(t, tr, nil)
	e.post(report("cr_900001", "a.A"))
	run := (<-tr.calls).ID
	e.wait(run, Paused)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := e.svc.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with a paused run: %v", err)
	}
	e.open()
	if r := e.wait(run, Paused); len(e.svc.review.open()) != 1 {
		t.Errorf("after restart: %s with %d items", r.State,
			len(e.svc.review.open()))
	}
}

func TestUnresolved(t *testing.T) {
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		return Outcome{Cost: 0.1}, fmt.Errorf("run: %w", agent.ErrMaxTurns)
	})
	e := newEnv(t, tr, nil)
	e.post(report("cr_a00001", "a.A"))
	r := e.wait((<-tr.calls).ID, Failed)
	if r.Outcome != "unresolved" {
		t.Errorf("outcome %q, want unresolved", r.Outcome)
	}
}

func TestDailyBudget(t *testing.T) {
	var mu sync.Mutex
	var got []float64
	release := make(chan struct{})
	tr := newTriager(func(_ context.Context, _ Run, _ agent.Approver,
		maxCost float64) (Outcome, error) {
		mu.Lock()
		got = append(got, maxCost)
		mu.Unlock()
		<-release
		return Outcome{Cost: maxCost}, nil
	})
	e := newEnv(t, tr, func(c *Config) {
		c.DailyCost, c.RunCost, c.Slots = 1, 0.6, 3
	})
	for i, fn := range []string{"a.A", "b.B", "c.C"} {
		e.post(report(fmt.Sprintf("cr_b0000%d", i), fn))
	}
	<-tr.calls
	<-tr.calls
	close(release)
	select {
	case <-tr.calls:
		t.Fatal("a third run started past the day's budget")
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	if len(got) != 2 || got[0] != 0.6 || got[1] < 0.39 || got[1] > 0.41 {
		t.Errorf("budgets %v, want 0.6 and 0.4", got)
	}
	mu.Unlock()
	e.restart()
	select {
	case <-tr.calls:
		t.Fatal("a restart reset the day's budget")
	case <-time.After(50 * time.Millisecond):
	}
	e.clk.Add(24 * time.Hour)
	e.svc.sweep()
	<-tr.calls // the next day
}

func TestLateReports(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		<-release
		return Outcome{Status: "fixed", Issue: 9}, nil
	})
	iss := &issues{open: map[string]int{}}
	e := newEnv(t, tr, func(c *Config) { c.Issues = iss })
	e.post(report("cr_c00001", "a.A"))
	run := (<-tr.calls).ID
	w := e.post(report("cr_c00002", "a.A"))
	if got := decode[routed](t, w); !got.Late || got.Run != run {
		t.Errorf("late report routed %+v", got)
	}
	close(release)
	e.wait(run, Done)
	waitFor(t, func() bool { return iss.count() == 1 })
	e.post(report("cr_c00002", "a.A")) // remind's retry: ignored
	e.svc.sweep()
	time.Sleep(20 * time.Millisecond)
	if n := iss.count(); n != 1 {
		t.Errorf("%d comments, want 1", n)
	}

	// After the run: an open issue gets a comment; a closed one, a run.
	fp := e.svc.runs[run].Fingerprint
	iss.mu.Lock()
	iss.open[fp] = 9
	iss.mu.Unlock()
	if got := decode[routed](t, e.post(report("cr_c00003", "a.A"))); !got.Late {
		t.Errorf("report for an open issue routed %+v", got)
	}
	waitFor(t, func() bool { return iss.count() == 2 })
	iss.mu.Lock()
	delete(iss.open, fp)
	iss.mu.Unlock()
	if got := decode[routed](t, e.post(report("cr_c00004", "a.A"))); !got.New {
		t.Errorf("report after the issue closed routed %+v", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 1000 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// TestRecoverReport saves a report without a run, as a crash between
// the two writes leaves it: the next start routes it.
func TestRecoverReport(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	if err := e.svc.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wr wireReport
	b, _ := json.Marshal(report("cr_d00001", "a.A"))
	if err := json.Unmarshal(b, &wr); err != nil {
		t.Fatal(err)
	}
	rep := wr.report()
	if err := writeJSON(filepath.Join(e.cfg.Dir, reportsDir,
		rep.ID+".json"), storedReport{Report: rep,
		Fingerprint: "fp_x"}); err != nil {
		t.Fatal(err)
	}
	e.open()
	r := e.wait((<-e.cfg.Triager.(*triager).calls).ID, Done)
	if len(r.Reports) != 1 || r.Reports[0] != rep.ID {
		t.Errorf("recovered run %+v", r)
	}
}

func TestSecrets(t *testing.T) {
	for _, cfg := range []Config{
		{Secret: "short"},
		{Secret: secret, Reviewers: map[string]string{"short": "ana"}},
		{Secret: secret, Reviewers: map[string]string{revToken: " "}},
		{Secret: secret, Reviewers: map[string]string{secret: "ana"}},
	} {
		cfg.Dir = t.TempDir()
		if _, err := New(t.Context(), cfg); err == nil {
			t.Errorf("New accepted %+v", cfg)
		}
	}
}

func TestResults(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	e.post(report("cr_e00001", "a.A"))
	e.wait((<-e.cfg.Triager.(*triager).calls).ID, Done)
	res := decode[Results](t, e.do(http.MethodGet, "/results", revToken, nil))
	if res.Counts["fixed"] != 1 || res.SpentToday < 0.0099 {
		t.Errorf("results %+v", res)
	}
	if !strings.Contains(e.do(http.MethodGet, "/runs/nope", revToken,
		nil).Body.String(), "no such run") {
		t.Error("unknown run not reported")
	}
}

// TestLateGated: a late-report comment waits for a person, as the
// agent's issue_comment would, and is posted once approved.
func TestLateGated(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		<-release
		return Outcome{Status: "fixed", Issue: 9}, nil
	})
	iss := &issues{gated: true, open: map[string]int{}}
	e := newEnv(t, tr, func(c *Config) { c.Issues = iss })
	e.post(report("cr_f00001", "a.A"))
	run := (<-tr.calls).ID
	e.post(report("cr_f00002", "a.A"))
	e.post(report("cr_f00003", "a.A"))
	close(release)
	e.wait(run, Done)
	var items []Item
	waitFor(t, func() bool {
		items = e.svc.review.open()
		return len(items) == 1
	})
	if items[0].Run != "late-"+run || !strings.Contains(items[0].Evidence,
		"Seen 2 more times") || iss.count() != 0 {
		t.Fatalf("items %+v, %d comments", items, iss.count())
	}
	w := e.do(http.MethodPost, "/review/"+items[0].ID+"/approve", revToken,
		map[string]string{})
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool { return iss.count() == 1 })
	e.svc.sweep()
	time.Sleep(20 * time.Millisecond)
	if n := iss.count(); n != 1 {
		t.Errorf("%d comments after approval, want 1", n)
	}
}

// TestRestart: a run whose workspace changed is restarted once under a
// new ID with every report; the second time, it fails for a person.
func TestRestart(t *testing.T) {
	var mu sync.Mutex
	var seen []Run
	tr := newTriager(func(_ context.Context, run Run, _ agent.Approver,
		_ float64) (Outcome, error) {
		mu.Lock()
		seen = append(seen, run)
		mu.Unlock()
		return Outcome{}, &Restart{Note: "c1 took effect",
			Err: fmt.Errorf("resume: %w", agent.ErrWorkspaceChanged)}
	})
	e := newEnv(t, tr, nil)
	e.post(report("cr_g00001", "a.A"))
	first := (<-tr.calls).ID
	second := (<-tr.calls)
	if second.RestartOf != first || second.ID == first ||
		len(second.Reports) != 1 || second.Note != "c1 took effect" {
		t.Fatalf("restart %+v of %s", second, first)
	}
	r := e.wait(second.ID, Failed)
	if !strings.Contains(r.Err, "needs a person") {
		t.Errorf("second failure: %q", r.Err)
	}
	select {
	case extra := <-tr.calls:
		t.Errorf("a third run: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestAdd files reports from the command's file, keeping their IDs,
// and Wait returns once every run is done.
func TestAdd(t *testing.T) {
	cfg := Config{Dir: t.TempDir(), Secret: secret, Triager: fixed(),
		Log: slog.New(slog.DiscardHandler)}
	svc, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown(context.Background()) //nolint:errcheck // test end
	e := &env{t: t, cfg: cfg, svc: svc}
	reps := []triage.Report{
		{ID: "cr_1e48bd", Kind: "panic", Commit: "9c41e7a",
			Stack: "example.com/remind/a.A()\n"},
		{ID: "cr_8a89f7", Kind: "panic", Commit: "9c41e7a",
			Stack: "example.com/remind/a.A()\n"},
		{ID: "cr_000b0b", Kind: "panic", Commit: "9c41e7a",
			Stack: "example.com/remind/b.B()\n"},
	}
	runs, err := e.svc.Add(t.Context(), reps...)
	if err != nil || len(runs) != 2 {
		t.Fatalf("Add: %v, %v", runs, err)
	}
	e.svc.Start()
	if err := e.svc.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.svc.Runs() {
		if r.State != Done {
			t.Errorf("run %s: %s", r.ID, r.State)
		}
		if r.Fingerprint == triage.Fingerprint(reps[0]) &&
			strings.Join(r.Reports, ",") != "cr_1e48bd,cr_8a89f7" {
			t.Errorf("run %s reports %v", r.ID, r.Reports)
		}
	}
}

// TestLedgerCrash copies the state directory while a run is going, as
// a kill leaves it, and starts a service on the copy: the run's whole
// reservation counts as spent, so the day's budget isn't spent twice.
func TestLedgerCrash(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(ctx context.Context, _ Run, _ agent.Approver,
		maxCost float64) (Outcome, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Outcome{Cost: 0.9 * maxCost}, ctx.Err()
	})
	e := newEnv(t, tr, func(c *Config) { c.DailyCost, c.RunCost = 1, 1 })
	defer close(release)
	e.post(report("cr_h00001", "a.A"))
	<-tr.calls
	crashed := t.TempDir()
	if err := os.CopyFS(crashed, os.DirFS(e.cfg.Dir)); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Dir = crashed
	second := newTriager(tr.fn)
	cfg.Triager = second
	svc, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown(context.Background()) //nolint:errcheck // test end
	svc.Start()
	select {
	case <-second.calls:
		t.Error("the second life started the run again on the same day's money")
	case <-time.After(100 * time.Millisecond):
	}
	if svc.ledger.Spent < 1 {
		t.Errorf("ledger spent $%.2f after the crash, want the $1 held",
			svc.ledger.Spent)
	}
}

// TestLateHandedOn: late reports of a run that ends with no issue to
// comment on go to a run of their own, for every such ending.
func TestLateHandedOn(t *testing.T) {
	for _, tt := range []struct {
		name string
		out  Outcome
		err  error
	}{
		{"not a bug", Outcome{Status: "not_a_bug"}, nil},
		{"out of turns", Outcome{}, fmt.Errorf("run: %w", agent.ErrMaxTurns)},
		{"tool fatal", Outcome{}, fmt.Errorf("run: %w", agent.ErrToolFatal)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			tr := newTriager(func(context.Context, Run, agent.Approver,
				float64) (Outcome, error) {
				first := false
				once.Do(func() { first = true })
				if first {
					<-release
					return tt.out, tt.err
				}
				return Outcome{Status: "fixed", Issue: 4}, nil
			})
			e := newEnv(t, tr, nil)
			e.post(report("cr_j00001", "a.A"))
			run := (<-tr.calls).ID
			e.post(report("cr_j00002", "a.A"))
			e.post(report("cr_j00003", "a.A"))
			close(release)
			next := <-tr.calls
			if next.FollowUpOf != run || len(next.Reports) != 2 {
				t.Errorf("follow-up %+v of %s", next, run)
			}
			e.wait(next.ID, Done)
			e.svc.mu.Lock()
			if h := e.svc.runs[run].HandedOn; h != next.ID {
				t.Errorf("handed on to %q, want %q", h, next.ID)
			}
			e.svc.mu.Unlock()
		})
	}
}

// TestLateDeclined: a denied late comment is that batch's answer; the
// next late reports get a comment of their own; and a batch whose
// deadline passes is declined the same way.
func TestLateDeclined(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		<-release
		return Outcome{Status: "fixed", Issue: 9}, nil
	})
	iss := &issues{gated: true, open: map[string]int{}}
	e := newEnv(t, tr, func(c *Config) {
		c.Issues, c.ReviewWait = iss, time.Hour
	})
	e.post(report("cr_k00001", "a.A"))
	run := (<-tr.calls).ID
	fp := e.svc.runs[run].Fingerprint
	e.post(report("cr_k00002", "a.A"))
	close(release)
	e.wait(run, Done)
	iss.mu.Lock()
	iss.open[fp] = 9
	iss.mu.Unlock()
	item := waitItem(t, e)
	w := e.do(http.MethodPost, "/review/"+item.ID+"/deny", revToken,
		map[string]string{"reason": "too noisy"})
	if w.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", w.Code, w.Body)
	}
	waitFor(t, func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		r := e.svc.runs[run]
		return len(r.Declined) == 1 && r.Posted == 1
	})

	e.post(report("cr_k00003", "a.A")) // a new batch, a new question
	item = waitItem(t, e)
	if !strings.Contains(item.Evidence, "cr_k00003") ||
		strings.Contains(item.Evidence, "cr_k00002") {
		t.Errorf("the second batch's text: %q", item.Evidence)
	}
	e.do(http.MethodPost, "/review/"+item.ID+"/approve", revToken,
		map[string]string{})
	waitFor(t, func() bool { return iss.count() == 1 })

	e.post(report("cr_k00004", "a.A")) // a third, left to its deadline
	waitItem(t, e)
	e.clk.Add(time.Hour)
	e.svc.sweep()
	waitFor(t, func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		return len(e.svc.runs[run].Declined) == 2
	})
}

// waitItem waits for the one open review item.
func waitItem(t *testing.T, e *env) Item {
	t.Helper()
	var items []Item
	waitFor(t, func() bool {
		e.svc.sweep()
		items = e.svc.review.open()
		return len(items) == 1
	})
	return items[0]
}

// TestWaitingItem: a run paused for something that isn't a person's
// decision has no item in the review list and resumes once it settles.
func TestWaitingItem(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	req := agent.ApprovalRequest{Kind: agent.KindWait,
		Call: agent.ToolCall{ID: "c1", Name: "issue_file"},
		Why:  "waiting", Targets: []string{"cause:a/a.go:F"}, Hash: "w"}
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		if busy.Load() {
			return Outcome{}, &agent.ApprovalPending{
				Requests: []agent.ApprovalRequest{req}}
		}
		return Outcome{Status: "fixed", Issue: 2}, nil
	})
	e := newEnv(t, tr, func(c *Config) {
		c.Busy = func(string) bool { return busy.Load() }
	})
	e.post(report("cr_m00001", "a.A"))
	run := (<-tr.calls).ID
	e.wait(run, Paused)
	if items := e.svc.review.open(); len(items) != 0 {
		t.Errorf("a waiting item in the review list: %+v", items)
	}
	e.svc.sweep()
	if r := e.wait(run, Paused); r.State != Paused {
		t.Fatal("woke while still busy")
	}
	busy.Store(false)
	e.svc.sweep()
	<-tr.calls
	e.wait(run, Done)
}

// TestDuplicateGroupLate: a group answered as a duplicate of another
// group's open issue sends its later reports there, not to new runs.
func TestDuplicateGroupLate(t *testing.T) {
	tr := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		return Outcome{Status: "duplicate", Issue: 7}, nil
	})
	iss := &issues{open: map[string]int{"fp_other": 7}}
	e := newEnv(t, tr, func(c *Config) { c.Issues = iss })
	e.post(report("cr_n00001", "a.A"))
	e.wait((<-tr.calls).ID, Done)
	for _, id := range []string{"cr_n00002", "cr_n00003"} {
		if got := decode[routed](t, e.post(report(id, "a.A"))); !got.Late {
			t.Errorf("%s routed %+v, want late", id, got)
		}
	}
	select {
	case r := <-tr.calls:
		t.Errorf("a new run %s for a duplicate group", r.ID)
	case <-time.After(50 * time.Millisecond):
	}
	waitFor(t, func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		for _, r := range e.svc.runs {
			if r.Posted == 2 {
				return iss.count() >= 1
			}
		}
		return false
	})
}

// TestPrune: a finished run's files go after Keep; its record and a
// paused run's files stay.
func TestPrune(t *testing.T) {
	var mu sync.Mutex
	var pruned []string
	e := newEnv(t, fixed(), func(c *Config) {
		c.Prune = func(r Run) error {
			mu.Lock()
			defer mu.Unlock()
			pruned = append(pruned, r.ID)
			return nil
		}
	})
	e.post(report("cr_p00001", "a.A"))
	run := (<-e.cfg.Triager.(*triager).calls).ID
	e.wait(run, Done)
	e.svc.sweep()
	e.clk.Add(8 * 24 * time.Hour)
	e.svc.sweep()
	e.svc.sweep()
	mu.Lock()
	defer mu.Unlock()
	if len(pruned) != 1 || pruned[0] != run || !e.svc.Runs()[0].Pruned {
		t.Errorf("pruned %v", pruned)
	}
}

func TestCloseItems(t *testing.T) {
	q, err := openReview(t.TempDir(), time.Hour, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	req := agent.ApprovalRequest{Call: agent.ToolCall{ID: "c1"}, Hash: "h"}
	if err := q.add("run-1", &agent.ApprovalPending{
		Requests: []agent.ApprovalRequest{req}}); err != nil {
		t.Fatal(err)
	}
	q.close("run-1", "the run ended")
	if len(q.open()) != 0 || !q.settled("run-1") {
		t.Errorf("a closed item is still open: %+v", q.open())
	}
	if _, err := q.decide(itemID("run-1", req), KindApproval,
		Decision{Approved: true}); !errors.Is(err, ErrClosed) {
		t.Errorf("deciding a closed item: %v", err)
	}
}

// TestWaitingLastGroup: the group waiting on another's filing resumes
// when that run ends, though it was the last run going, and Wait
// doesn't return before it finishes.
func TestWaitingLastGroup(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	req := agent.ApprovalRequest{Kind: agent.KindWait,
		Call:    agent.ToolCall{ID: "c1", Name: "issue_file"},
		Targets: []string{"cause:a/a.go:F"}, Hash: "w"}
	tr := newTriager(func(_ context.Context, run Run, _ agent.Approver,
		_ float64) (Outcome, error) {
		if run.Fingerprint == triage.Fingerprint(triage.Report{Kind: "panic",
			Stack: "example.com/remind/a.A()\n"}) {
			time.Sleep(20 * time.Millisecond)
			busy.Store(false) // A's filing settles as A ends
			return Outcome{Status: "fixed", Issue: 1}, nil
		}
		if busy.Load() {
			return Outcome{}, &agent.ApprovalPending{
				Requests: []agent.ApprovalRequest{req}}
		}
		return Outcome{Status: "duplicate", Issue: 1}, nil
	})
	cfg := Config{Dir: t.TempDir(), Secret: secret, Triager: tr,
		Busy: func(string) bool { return busy.Load() }, Slots: 2,
		Log: slog.New(slog.DiscardHandler)}
	svc, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown(context.Background()) //nolint:errcheck // test end
	if _, err := svc.Add(t.Context(),
		triage.Report{ID: "a1", Kind: "panic", Commit: "9c41e7a",
			Stack: "example.com/remind/a.A()\n"},
		triage.Report{ID: "b1", Kind: "panic", Commit: "9c41e7a",
			Stack: "example.com/remind/b.B()\n"}); err != nil {
		t.Fatal(err)
	}
	svc.Start()
	if err := svc.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, r := range svc.Runs() {
		if r.State != Done {
			t.Errorf("run %s left %s", r.Fingerprint, r.State)
		}
	}
}

// TestLedgerResume: a killed run's hold counts as what its checkpoint
// says it spent, so the run resumes within the budget; and a total
// budget doesn't start over at midnight.
func TestLedgerResume(t *testing.T) {
	release := make(chan struct{})
	tr := newTriager(func(ctx context.Context, _ Run, _ agent.Approver,
		_ float64) (Outcome, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Outcome{Cost: 0.1}, ctx.Err()
	})
	e := newEnv(t, tr, func(c *Config) { c.TotalCost, c.RunCost = 1, 1 })
	e.post(report("cr_q00001", "a.A"))
	<-tr.calls
	crashed := t.TempDir()
	if err := os.CopyFS(crashed, os.DirFS(e.cfg.Dir)); err != nil {
		t.Fatal(err)
	}
	close(release)
	cfg := e.cfg
	cfg.Dir = crashed
	second := newTriager(func(context.Context, Run, agent.Approver,
		float64) (Outcome, error) {
		return Outcome{Status: "fixed", Cost: 0.2}, nil
	})
	cfg.Triager = second
	cfg.SpentSoFar = func(string) (float64, bool) { return 0.1, true }
	svc, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown(context.Background()) //nolint:errcheck // test end
	svc.Start()
	select {
	case <-second.calls: // resumed with the $0.90 left
	case <-time.After(2 * time.Second):
		t.Fatal("the killed run didn't resume")
	}
	if err := svc.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	total := svc.ledger.Total
	svc.mu.Unlock()
	if math.Abs(total-0.3) > 1e-9 {
		t.Errorf("total spent $%.2f, want $0.30", total)
	}
	e.clk.Add(24 * time.Hour)
	svc.mu.Lock()
	svc.ledger.Total = 1
	ok := svc.ledger.canStart()
	svc.mu.Unlock()
	if ok {
		t.Error("the total budget started over at midnight")
	}
}

// TestHandOnAfterCrash: a follow-up run saved just before a crash, with
// the finished run's save lost, is adopted, not made twice.
func TestHandOnAfterCrash(t *testing.T) {
	e := newEnv(t, fixed(), nil)
	now := e.clk.Now()
	old := &Run{ID: "fp_x-1", Fingerprint: "fp_x", State: Done,
		Late: []string{"cr_r1"}, Created: now}
	follow := &Run{ID: "fp_x-2", Fingerprint: "fp_x", State: Done,
		FollowUpOf: old.ID, Reports: []string{"cr_r1"}, Created: now}
	e.svc.mu.Lock()
	e.svc.runs[old.ID], e.svc.runs[follow.ID] = old, follow
	e.svc.handOn(old)
	n := len(e.svc.runs)
	e.svc.mu.Unlock()
	if old.HandedOn != follow.ID || n != 2 {
		t.Errorf("handed on to %q with %d runs; want %q and 2", old.HandedOn,
			n, follow.ID)
	}
}
