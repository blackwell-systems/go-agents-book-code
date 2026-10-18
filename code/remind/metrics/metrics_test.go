package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func text(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if err := r.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestWriteText(t *testing.T) {
	r := NewRegistry()
	sent := r.Counter("remind_sent_total", "Reminders delivered.", "channel", "attempt")
	sent.With("webhook", "1").Add(3)
	sent.With("email", "1").Inc()
	sent.With("email", "2").Inc()
	r.Gauge("remind_pending", "Reminders waiting to be sent.").With().Set(7.5)
	r.Counter("remind_unused_total", "Never touched.", "user")
	lat := r.Histogram("remind_send_seconds", "Delivery time.", []float64{1, 0.1, 0.5}, "channel")
	lat.With("email").Observe(0.05)
	lat.With("email").Observe(0.3)
	lat.With("email").Observe(0.5)
	lat.With("email").Observe(4)

	want := `# HELP remind_pending Reminders waiting to be sent.
# TYPE remind_pending gauge
remind_pending 7.5
# HELP remind_send_seconds Delivery time.
# TYPE remind_send_seconds histogram
remind_send_seconds_bucket{channel="email",le="0.1"} 1
remind_send_seconds_bucket{channel="email",le="0.5"} 3
remind_send_seconds_bucket{channel="email",le="1"} 3
remind_send_seconds_bucket{channel="email",le="+Inf"} 4
remind_send_seconds_sum{channel="email"} 4.85
remind_send_seconds_count{channel="email"} 4
# HELP remind_sent_total Reminders delivered.
# TYPE remind_sent_total counter
remind_sent_total{attempt="1",channel="email"} 1
remind_sent_total{attempt="1",channel="webhook"} 3
remind_sent_total{attempt="2",channel="email"} 1
`
	if got := text(t, r); got != want {
		t.Errorf("WriteText:\n%s\nwant:\n%s", got, want)
	}
}

func TestEscaping(t *testing.T) {
	r := NewRegistry()
	r.Counter("errors_total", "Errors.\nSplit by `kind` \\ path.", "kind").
		With("say \"hi\"\nback\\slash").Inc()

	want := `# HELP errors_total Errors.\nSplit by ` + "`kind`" + ` \\ path.
# TYPE errors_total counter
errors_total{kind="say \"hi\"\nback\\slash"} 1
`
	if got := text(t, r); got != want {
		t.Errorf("WriteText:\n%s\nwant:\n%s", got, want)
	}
}

func TestHistogramBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("h", "h", []float64{1, 2, 2, 3}).With()
	for _, x := range []float64{0, 1, 1.5, 2, 2.5, 3, 10} {
		h.Observe(x)
	}
	got := text(t, r)
	for _, line := range []string{
		`h_bucket{le="1"} 2`,
		`h_bucket{le="2"} 4`,
		`h_bucket{le="3"} 6`,
		`h_bucket{le="+Inf"} 7`,
		`h_count 7`,
		`h_sum 20`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("output lacks %q:\n%s", line, got)
		}
	}
	if n := strings.Count(got, "_bucket"); n != 4 {
		t.Errorf("%d bucket lines, want 4 (duplicate bound kept?)", n)
	}
}

func TestWrongLabelCount(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("c", "c", "a", "b")
	defer func() {
		if recover() == nil {
			t.Error("With with one value for two labels did not panic")
		}
	}()
	c.With("x")
}

func TestRegisterTwice(t *testing.T) {
	r := NewRegistry()
	r.Counter("c", "c", "a").With("x").Inc()
	r.Counter("c", "c", "a").With("x").Inc()
	if got := text(t, r); !strings.Contains(got, `c{a="x"} 2`) {
		t.Errorf("second registration did not share the counter:\n%s", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("registering c as a gauge did not panic")
		}
	}()
	r.Gauge("c", "c", "a")
}

func TestConcurrentUpdates(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("c", "c", "worker")
	g := r.Gauge("g", "g")
	h := r.Histogram("h", "h", []float64{1})

	const workers, n = 8, 1000
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range n {
				c.With("w").Inc()
				g.With().Add(0.5)
				h.With().Observe(0.5)
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			var b strings.Builder
			if err := r.WriteText(&b); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()

	got := text(t, r)
	for _, line := range []string{
		`c{worker="w"} 8000`,
		`g 4000`,
		`h_count 8000`,
		`h_bucket{le="1"} 8000`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("output lacks %q:\n%s", line, got)
		}
	}
}

func TestHandler(t *testing.T) {
	r := NewRegistry()
	r.Counter("c", "c").With().Inc()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "c 1\n") {
		t.Errorf("body:\n%s", rec.Body)
	}
}

func TestInstrument(t *testing.T) {
	r := NewRegistry()
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }

	h := Instrument(r, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clock = clock.Add(250 * time.Millisecond)
		switch req.URL.Path {
		case "/missing":
			http.NotFound(w, req)
		case "/twice":
			w.WriteHeader(http.StatusCreated)
			w.WriteHeader(http.StatusInternalServerError)
		case "/write":
			if _, err := w.Write([]byte("ok")); err != nil {
				t.Error(err)
			}
		}
	}))
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/write"},
		{http.MethodGet, "/missing"},
		{http.MethodPost, "/twice"},
	} {
		req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	got := text(t, r)
	for _, line := range []string{
		`http_requests_total{code="200",method="GET"} 2`,
		`http_requests_total{code="404",method="GET"} 1`,
		`http_requests_total{code="201",method="POST"} 1`,
		`http_request_duration_seconds_count{method="GET"} 3`,
		`http_request_duration_seconds_sum{method="GET"} 0.75`,
		`http_request_duration_seconds_bucket{method="GET",le="0.1"} 0`,
		`http_request_duration_seconds_bucket{method="GET",le="0.25"} 3`,
		`http_request_duration_seconds_count{method="POST"} 1`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("output lacks %q:\n%s", line, got)
		}
	}
}
