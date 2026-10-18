package openrouter

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
)

// TestRetryAfter checks both forms of the header, and the waits that
// are read as none.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"3":                             3 * time.Second,
		"Fri, 16 Oct 2026 12:00:30 GMT": 30 * time.Second,
		"Fri, 16 Oct 2026 11:00:00 GMT": 0, // in the past
		"-5":                            0,
		"9999999999999":                 24 * time.Hour, // capped
		"soon":                          0,
		"":                              0,
	} {
		h := http.Header{}
		h.Set("Retry-After", v)
		if got := agent.RetryAfter(h, now); got != want {
			t.Errorf("Retry-After %q = %v, want %v", v, got, want)
		}
	}
}

// TestRetryableStatuses checks the classifier's view of the client's
// errors: the two kinds of 402, a 400 for a thinking block sent to the
// wrong provider, and errors inside a reply that keep their code.
func TestRetryableStatuses(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		body  string
		retry bool
	}{
		{"402 out of credit", `{"error": {"code": 402, "message": "Insufficient credits"}}`, false},
		{"402 in-flight budget", `{"error": {"code": 402, "message": "would exceed your available credits",` +
			` "metadata": {"reason": "in_flight_budget_exhausted",` +
			` "limit_source": "openrouter_in_flight_budget"}}}`, true},
		{"400 thinking block", `{"error": {"code": 400, "message":` +
			` "this thinking block can only be processed by the provider that created it"}}`, false},
	} {
		status := http.StatusPaymentRequired
		if tc.name == "400 thinking block" {
			status = http.StatusBadRequest
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, err := (&Client{BaseURL: srv.URL}).Generate(ctx, agent.Request{})
		srv.Close()
		d := agent.Classify(ctx, err)
		if d.Retry != tc.retry {
			t.Errorf("%s: retry %v, want %v (%v)", tc.name, d.Retry, tc.retry, err)
		}
		if tc.retry && d.After != 2*time.Second {
			t.Errorf("%s: after %v, want 2s", tc.name, d.After)
		}
	}

	srv := serve(`data: {"id": "gen-7", "choices": [{"delta": {"content": "Hi"}}]}`+"\n\n"+
		`data: {"error": {"code": 429, "message": "Rate limit exceeded"},`+
		` "choices": [{"delta": {}, "finish_reason": "error"}]}`+"\n\n", 4096)
	defer srv.Close()
	var id string
	var err error
	for d, e := range (&Client{BaseURL: srv.URL}).Stream(ctx, agent.Request{}) {
		id = cmp.Or(d.ID, id)
		err = e
	}
	if d := agent.Classify(ctx, err); !d.Retry {
		t.Errorf("a 429 inside a stream wasn't retryable: %v", err)
	}
	if id != "gen-7" {
		t.Errorf("generation ID %q", id)
	}
}

// TestTimeoutsRetryable checks that the client's own deadlines report
// themselves as timeouts, while the caller's don't count.
func TestTimeoutsRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := &Client{BaseURL: srv.URL, Timeout: 50 * time.Millisecond}
	_, err := c.Generate(t.Context(), agent.Request{})
	if !errors.Is(err, ErrCallTimeout) || !agent.Classify(t.Context(), err).Retry {
		t.Errorf("call timeout: %v", err)
	}
	c = &Client{BaseURL: srv.URL, IdleTimeout: 50 * time.Millisecond}
	for _, e := range c.Stream(t.Context(), agent.Request{}) {
		err = e
	}
	if !errors.Is(err, ErrStreamIdle) || !agent.Classify(t.Context(), err).Retry {
		t.Errorf("idle stream: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = (&Client{BaseURL: srv.URL}).Generate(ctx, agent.Request{})
	if agent.Classify(ctx, err).Retry {
		t.Errorf("the caller's deadline was retryable: %v", err)
	}
}

// TestRetryingStreamCut runs the client behind a Faulty transport that
// resets the connection after the first event of a stream, under
// agent.Retrying. The watcher sees the first piece, a Reset, and then
// the whole reply from the second attempt; the reply's usage counts
// both attempts, since the provider billed both (here the cut attempt
// reported none, so the total is the second attempt's).
func TestRetryingStreamCut(t *testing.T) {
	first := `data: {"id":"gen-1","choices":[{"delta":{"content":"Hel"}}]}` + "\n\n"
	body := first +
		`data: {"id":"gen-1","choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"gen-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	srv := serve(body, len(body))
	defer srv.Close()
	faulty := &agent.Faulty{Faults: []agent.Fault{{Reset: len(first)}}}
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Transport: faulty}}
	p := agent.Policy{Backoff: agent.Backoff{Rand: func() float64 { return 0 }}}
	m := agent.Retrying(p)(c)
	var got strings.Builder
	var id string
	for d, err := range m.Stream(t.Context(), agent.Request{}) {
		if err != nil {
			t.Fatalf("after %q: %v", got.String(), err)
		}
		if d.Reset {
			got.WriteString("|reset|")
		}
		got.WriteString(d.Text)
		if d.Usage != nil {
			fmt.Fprintf(&got, "(usage %d)", d.Usage.PromptTokens)
		}
		id = cmp.Or(d.ID, id)
	}
	if want := "Hel|reset|Hello(usage 10)"; got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
	if id != "gen-1" {
		t.Errorf("generation ID %q, want gen-1", id)
	}
}

// TestGeneration checks the lookup of a generation's usage: a 404
// while it isn't recorded yet, then its tokens and cost.
func TestGeneration(t *testing.T) {
	ready := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generation" || r.URL.Query().Get("id") != "gen-1" ||
			r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request %s %v", r.URL, r.Header)
		}
		if !ready {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Generation not found"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"gen-1","native_tokens_prompt":900,`+
			`"native_tokens_completion":12,"native_tokens_cached":800,"total_cost":0.0021,"cancelled":true}}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	_, err := c.Generation(t.Context(), "gen-1")
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != http.StatusNotFound {
		t.Errorf("before it is recorded: %v", err)
	}
	ready = true
	u, err := c.Generation(t.Context(), "gen-1")
	want := agent.Usage{PromptTokens: 900, CompletionTokens: 12, CachedTokens: 800, Cost: 0.0021}
	if err != nil || u != want {
		t.Errorf("got %+v, %v; want %+v", u, err, want)
	}
}
