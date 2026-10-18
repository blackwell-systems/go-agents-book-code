package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// statusErr stands in for chapter 5's *APIError.
type statusErr struct {
	status int
	after  time.Duration
}

func (e *statusErr) Error() string { return fmt.Sprintf("status %d", e.status) }
func (e *statusErr) Retryable() bool {
	return e.status == 408 || e.status == 429 || e.status >= 500
}
func (e *statusErr) RetryDelay() time.Duration { return e.after }

// callTimeout stands in for a client's own per-call deadline, such as
// chapter 2's ErrCallTimeout or chapter 6's idle stream, made a type
// that reports itself as a timeout.
type callTimeout struct{}

func (callTimeout) Error() string { return "model call timed out" }
func (callTimeout) Timeout() bool { return true }

// TestClassify checks the decision for each kind of failure the book's
// runs have met.
func TestClassify(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tlsDrop := fmt.Errorf("send request: %w", badRecordMAC)
	inner := fmt.Errorf("%w: %w", ErrAttemptTimeout, context.DeadlineExceeded)
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		retry bool
		after time.Duration
	}{
		{"success", t.Context(), nil, false, 0},
		{"429 with a delay", t.Context(), &statusErr{429, 3 * time.Second}, true, 3 * time.Second},
		{"408", t.Context(), &statusErr{408, 0}, true, 0},
		{"503", t.Context(), fmt.Errorf("send: %w", &statusErr{503, 0}), true, 0},
		{"400", t.Context(), &statusErr{400, 0}, false, 0},
		{"401", t.Context(), &statusErr{401, 0}, false, 0},
		{"402, out of credit", t.Context(), &statusErr{402, 0}, false, 0},
		{"403", t.Context(), &statusErr{403, 0}, false, 0},
		{"dropped TLS connection", t.Context(), tlsDrop, true, 0},
		{"reset mid-stream", t.Context(), fmt.Errorf("read stream: %w", syscall.ECONNRESET), true, 0},
		{"body cut short", t.Context(), io.ErrUnexpectedEOF, true, 0},
		{"kept-alive connection closed", t.Context(), fmt.Errorf(`Post "x": %w`, io.EOF), true, 0},
		{"idle connection closed", t.Context(), errors.New(`Post "x": http: server closed idle connection`), true, 0},
		{"HTTP/2 stream reset", t.Context(), errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer"), true, 0},
		{"temporary DNS failure", t.Context(), &net.DNSError{Err: "x", IsTemporary: true}, true, 0},
		{"no such host", t.Context(), &net.DNSError{Err: "no such host", IsNotFound: true}, false, 0},
		{"bad certificate", t.Context(), &tls.CertificateVerificationError{Err: errors.New("x509")}, false, 0},
		{"attempt timed out", t.Context(), inner, true, 0},
		{"a call's own timeout", t.Context(), fmt.Errorf("generate: %w", callTimeout{}), true, 0},
		{"a timeout inside a url.Error", t.Context(), &url.Error{Op: "Post", URL: "x",
			Err: fmt.Errorf("%w: %w", context.Canceled, callTimeout{})}, true, 0},
		{"400 as the attempt deadline fired", t.Context(),
			fmt.Errorf("%w: %w", ErrAttemptTimeout, &statusErr{400, 0}), false, 0},
		{"caller cancelled", cancelled, fmt.Errorf("send: %w", &statusErr{503, 0}), false, 0},
		{"retries exhausted", t.Context(), &RetryError{3, &statusErr{503, 0}}, false, 0},
		{"a tool that can't run, its server gone", t.Context(),
			fmt.Errorf("%w: mcp: the server stopped: %w", ErrToolFatal, io.EOF), false, 0},
		{"retries exhausted on timeouts", t.Context(), &RetryError{2, inner}, false, 0},
	} {
		d := Classify(tc.ctx, tc.err)
		if d.Retry != tc.retry || d.After != tc.after {
			t.Errorf("%s: got %+v, want retry %v after %v", tc.name, d, tc.retry, tc.after)
		}
	}
}

// TestBackoff checks the full-jitter ceilings: double each retry, never
// above Max, with no overflow for large bases or retry numbers.
func TestBackoff(t *testing.T) {
	half := func() float64 { return 0.5 }
	b := Backoff{Base: time.Second, Max: 10 * time.Second, Rand: half}
	for n, want := range map[int]time.Duration{0: 500 * time.Millisecond,
		1: 500 * time.Millisecond, 2: time.Second, 3: 2 * time.Second,
		4: 4 * time.Second, 5: 5 * time.Second, 40: 5 * time.Second} {
		if got := b.Delay(n); got != want {
			t.Errorf("Delay(%d) = %v, want %v", n, got, want)
		}
	}
	big := Backoff{Base: 20 * time.Second, Max: time.Hour, Rand: half}
	if got := big.Delay(64); got != 30*time.Minute {
		t.Errorf("large base: Delay(64) = %v, want 30m", got)
	}
}

// failsThen returns an op that fails with errs in turn, then succeeds,
// and counts its calls.
func failsThen(calls *int, errs ...error) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		if *calls <= len(errs) {
			return errs[*calls-1]
		}
		return nil
	}
}

var half = Backoff{Rand: func() float64 { return 0.5 }}

// TestDo checks Do's waits under synctest's fake clock, so they are
// exact: two 503s then success waits half of 1s, then half of 2s.
func TestDo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		err := Do(t.Context(), Policy{Backoff: half},
			failsThen(&calls, &statusErr{503, 0}, &statusErr{503, 0}))
		if err != nil || calls != 3 {
			t.Fatalf("err %v after %d calls", err, calls)
		}
		if got := time.Since(start); got != 1500*time.Millisecond {
			t.Errorf("waited %v, want 1.5s", got)
		}
	})
}

// TestDoGivesUp checks each way Do stops early, and that what it
// returns stops an outer retry too, timeouts included.
func TestDoGivesUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		err := Do(t.Context(), Policy{Attempts: 3, Backoff: half},
			failsThen(&calls, &statusErr{503, 0}, &statusErr{503, 0}, &statusErr{503, 0}))
		if _, ok := errors.AsType[*RetryError](err); !ok || calls != 3 {
			t.Errorf("attempts: %v after %d calls", err, calls)
		}

		calls = 0
		err = Do(t.Context(), Policy{}, failsThen(&calls, &statusErr{400, 0}))
		if _, ok := errors.AsType[*RetryError](err); ok || calls != 1 {
			t.Errorf("400: %v after %d calls", err, calls)
		}

		calls = 0
		err = Do(t.Context(), Policy{}, failsThen(&calls, &statusErr{429, 10 * time.Minute}))
		if err == nil || !strings.Contains(err.Error(), "asked to wait 10m0s") || calls != 1 {
			t.Errorf("long Retry-After: %v after %d calls", err, calls)
		}

		for _, inner := range []error{&statusErr{503, 0}, callTimeout{}} {
			outer, calls := 0, 0
			_ = Do(t.Context(), Policy{Backoff: half}, func(ctx context.Context) error {
				outer++
				return Do(ctx, Policy{Attempts: 2, Backoff: half},
					func(context.Context) error { calls++; return inner })
			})
			if outer != 1 || calls != 2 {
				t.Errorf("inner %v: outer ran %d times, inner op %d", inner, outer, calls)
			}
		}
	})
}

// TestDoHonorsRetryAfter checks that a server's delay replaces a
// shorter backoff, with jitter on top.
func TestDoHonorsRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		err := Do(t.Context(), Policy{Backoff: half},
			failsThen(&calls, &statusErr{429, 3 * time.Second}))
		if want := 3500 * time.Millisecond; err != nil || time.Since(start) != want {
			t.Errorf("err %v, waited %v, want %v", err, time.Since(start), want)
		}
	})
}

// TestDoAttemptDeadline checks that an attempt's own deadline is
// retried, the caller's isn't, and the pacing wait doesn't count
// against the attempt.
func TestDoAttemptDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		hang := func(ctx context.Context) error {
			calls++
			if calls == 1 {
				<-ctx.Done()
				return fmt.Errorf("send request: %w", ctx.Err())
			}
			return nil
		}
		zero := Backoff{Rand: func() float64 { return 0 }}
		if err := Do(t.Context(), Policy{Each: 2 * time.Second, Backoff: zero}, hang); err != nil || calls != 2 {
			t.Errorf("attempt deadline: %v after %d calls", err, calls)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		calls = 0
		hangAlways := func(ctx context.Context) error {
			calls++
			<-ctx.Done()
			return fmt.Errorf("send request: %w", ctx.Err())
		}
		err := Do(ctx, Policy{Each: 2 * time.Second, Backoff: zero}, hangAlways)
		if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
			t.Errorf("caller's deadline: %v after %d calls", err, calls)
		}

		calls = 0
		queue := func(context.Context) error { time.Sleep(5 * time.Second); return nil }
		err = Do(t.Context(), Policy{Each: 2 * time.Second, Wait: queue},
			failsThen(&calls))
		if err != nil || calls != 1 {
			t.Errorf("pacing counted against the attempt: %v after %d calls", err, calls)
		}
	})
}

// TestDoCancelled checks that cancelling during a wait keeps both
// context.Canceled and the last attempt's error.
func TestDoCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		stop := errors.New("shutting down")
		go func() { time.Sleep(time.Second / 4); cancel(stop) }()
		calls := 0
		err := Do(ctx, Policy{Backoff: half},
			failsThen(&calls, &statusErr{503, 0}, &statusErr{503, 0}))
		if !errors.Is(err, context.Canceled) || !errors.Is(err, stop) {
			t.Errorf("lost the cancellation: %v", err)
		}
		if _, ok := errors.AsType[*statusErr](err); !ok {
			t.Errorf("lost the last attempt's error: %v", err)
		}
	})
}

// TestBreaker walks a breaker open, through a failed probe, and closed.
func TestBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Breaker{Threshold: 3, Cooldown: 10 * time.Second}
		for range 3 {
			if _, err := b.Allow(); err != nil {
				t.Fatal(err)
			}
			b.Record(false)
		}
		if _, err := b.Allow(); !errors.Is(err, ErrOpen) {
			t.Fatalf("after 3 failures: %v", err)
		}
		time.Sleep(10 * time.Second)
		if probe, err := b.Allow(); err != nil || !probe {
			t.Fatalf("probe refused: %v %v", probe, err)
		}
		if _, err := b.Allow(); !errors.Is(err, ErrOpen) {
			t.Fatalf("a second call during the probe: %v", err)
		}
		b.Record(false) // the probe failed
		if _, err := b.Allow(); !errors.Is(err, ErrOpen) {
			t.Fatalf("after a failed probe: %v", err)
		}
		time.Sleep(10 * time.Second)
		if _, err := b.Allow(); err != nil {
			t.Fatal(err)
		}
		b.Record(true)
		if _, err := b.Allow(); err != nil {
			t.Fatalf("after a good probe: %v", err)
		}
	})
}

// TestBreakerProbes checks that a probe ended by a panic or by the
// caller's cancellation frees the next call to probe, and that a probe
// that succeeded as the caller cancelled closes the breaker.
func TestBreakerProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		open := func() *Breaker {
			b := &Breaker{Threshold: 1, Cooldown: time.Second}
			b.Record(false)
			time.Sleep(time.Second)
			return b
		}
		b := open()
		func() {
			defer func() { _ = recover() }()
			_ = Do(t.Context(), Policy{Breaker: b}, func(context.Context) error { panic("boom") })
		}()
		if probe, err := b.Allow(); err != nil || !probe {
			t.Errorf("after a panicking probe: %v %v", probe, err)
		}

		b = open()
		ctx, cancel := context.WithCancel(t.Context())
		err := Do(ctx, Policy{Breaker: b}, func(context.Context) error {
			cancel()
			return context.Canceled
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if probe, err := b.Allow(); err != nil || !probe {
			t.Errorf("after a cancelled probe: %v %v", probe, err)
		}

		b = open()
		ctx, cancel = context.WithCancel(t.Context())
		err = Do(ctx, Policy{Breaker: b}, func(context.Context) error {
			cancel()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if probe, err := b.Allow(); err != nil || probe {
			t.Errorf("a successful probe didn't close it: %v %v", probe, err)
		}
	})
}

// TestDoWaitsOutBreaker checks that Do waits for an open breaker that
// will close in time, and gives up with the last error when it won't.
func TestDoWaitsOutBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Breaker{Threshold: 1, Cooldown: 10 * time.Second}
		b.Record(false)
		calls := 0
		start := time.Now()
		if err := Do(t.Context(), Policy{Breaker: b}, failsThen(&calls)); err != nil || calls != 1 {
			t.Fatalf("err %v after %d calls", err, calls)
		}
		if got := time.Since(start); got != 10*time.Second {
			t.Errorf("waited %v, want the 10s cooldown", got)
		}

		b = &Breaker{Threshold: 2, Cooldown: time.Hour}
		calls = 0
		err := Do(t.Context(), Policy{Breaker: b, Backoff: half},
			failsThen(&calls, &statusErr{503, 0}, &statusErr{503, 0}))
		re, ok := errors.AsType[*RetryError](err)
		if !ok || !errors.Is(err, ErrOpen) || re.Attempts != 2 {
			t.Fatalf("breaker too long: %v", err)
		}
		if _, ok := errors.AsType[*statusErr](err); !ok {
			t.Errorf("lost the last attempt's error: %v", err)
		}
	})
}

// TestBudget checks that failures drain a shared budget to half, and
// successes refill it.
func TestBudget(t *testing.T) {
	b := &RetryBudget{Max: 10, Ratio: 0.5}
	for range 5 {
		if !b.Allow() {
			t.Fatal("refused too early")
		}
		b.Record(false)
	}
	if b.Allow() {
		t.Fatal("allowed a retry with 5 of 10 tokens")
	}
	b.Record(true)
	b.Record(true)
	if !b.Allow() {
		t.Fatal("still refused after two successes")
	}
}

// TestFaulty checks each fault through a real http.Client and server.
func TestFaulty(t *testing.T) {
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = io.WriteString(w, "data: one\n\ndata: two\n\n")
	}))
	defer srv.Close()
	f := &Faulty{Next: srv.Client().Transport, Faults: []Fault{
		{Status: http.StatusTooManyRequests, RetryAfter: "2"}, {Drop: true},
		{Lost: true}, {Reset: 11}, {Truncate: 11}, {}}}
	client := &http.Client{Transport: f}
	post := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			srv.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		return client.Do(req)
	}
	read := func() (string, error) {
		resp, err := post()
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	resp, err := post()
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "2" {
		t.Errorf("status fault: %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if _, err := read(); !transient(err) || !strings.Contains(err.Error(), "remote error: tls: bad record MAC") {
		t.Errorf("drop: %v", err)
	}
	before := served
	if _, err := read(); !transient(err) || served != before+1 {
		t.Errorf("lost: %v, served %d", err, served-before)
	}
	if body, err := read(); body != "data: one\n\n" || !transient(err) || !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("reset: %q, %v", body, err)
	}
	if body, err := read(); body != "data: one\n\n" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncate: %q, %v", body, err)
	}
	if body, err := read(); err != nil || len(body) != 22 {
		t.Errorf("pass-through: %q, %v", body, err)
	}
}

// TestRecorder checks the tree: nesting by context, durations from the
// fake clock, attributes, errors, and a span from another recorder.
func TestRecorder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRecorder()
		ctx, run := r.Start(t.Context(), "invoke_agent triage")
		cctx, call := r.Start(ctx, "chat haiku", slog.Int("attempt", 1))
		_, tool := r.Start(cctx, "execute_tool code_read")
		time.Sleep(200 * time.Millisecond)
		tool.Finish(nil)
		call.Set(slog.Int("gen_ai.usage.input_tokens", 4655))
		call.Finish(errors.New("status 503"))
		run.Finish(nil)
		other := NewRecorder()
		_, s := other.Start(cctx, "elsewhere")
		s.Finish(nil)
		want := "invoke_agent triage 200ms\n" +
			"  chat haiku 200ms attempt=1 gen_ai.usage.input_tokens=4655 error=\"status 503\"\n" +
			"    execute_tool code_read 200ms\n"
		if got := r.String(); got != want {
			t.Errorf("tree:\n%s\nwant:\n%s", got, want)
		}
		if got := other.String(); got != "elsewhere 0s\n" {
			t.Errorf("a span under another recorder's span: %q", got)
		}
		if len(r.Trace) != 32 {
			t.Errorf("trace ID %q", r.Trace)
		}
		var none *Recorder
		if _, h := none.Start(t.Context(), "x"); h != nil {
			t.Error("a nil Recorder recorded a span")
		}
	})
}

// TestRecorderConcurrent starts and reads spans from many goroutines,
// for the race detector.
func TestRecorderConcurrent(t *testing.T) {
	r := NewRecorder()
	ctx, root := r.Start(t.Context(), "run")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_, h := r.Start(ctx, "call")
				h.Set(slog.Int("n", 1))
				_ = r.Spans()
				h.Finish(nil)
			}
		})
	}
	wg.Wait()
	root.Finish(nil)
	if n := len(r.Spans()); n != 401 {
		t.Errorf("%d spans, want 401", n)
	}
}
