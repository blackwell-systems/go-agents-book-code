package quota

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock {
	return &clock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func TestPlanByName(t *testing.T) {
	for name, want := range map[string]Plan{"free": Free, "pro": Pro} {
		got, err := PlanByName(name)
		if err != nil || got != want {
			t.Errorf("PlanByName(%q) = %+v, %v; want %+v", name, got, err, want)
		}
	}
	for _, name := range []string{"", "Free", "enterprise"} {
		if _, err := PlanByName(name); !errors.Is(err, ErrUnknownPlan) {
			t.Errorf("PlanByName(%q) error = %v, want ErrUnknownPlan", name, err)
		}
	}
}

func TestCheckReminders(t *testing.T) {
	tests := []struct {
		plan  Plan
		count int
		ok    bool
	}{
		{Free, 0, true}, {Free, 49, true}, {Free, 50, false}, {Free, 51, false},
		{Pro, 50, true}, {Pro, 5000, false},
	}
	for _, tt := range tests {
		err := CheckReminders(tt.plan, tt.count)
		limit := strconv.Itoa(tt.plan.MaxReminders)
		if tt.ok && err != nil ||
			!tt.ok && (!errors.Is(err, ErrQuotaExceeded) || !strings.Contains(err.Error(), limit)) {
			t.Errorf("CheckReminders(%s, %d) = %v", tt.plan.Name, tt.count, err)
		}
	}
}

func TestBurst(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	for i := range Free.Burst {
		if ok, _ := l.Allow("u1", Free); !ok {
			t.Fatalf("request %d denied within burst", i+1)
		}
	}
	ok, retry := l.Allow("u1", Free)
	if ok {
		t.Fatal("request past burst allowed")
	}
	if want := 200 * time.Millisecond; retry != want {
		t.Errorf("retry after %v, want %v", retry, want)
	}
	if ok, _ := l.Allow("u2", Free); !ok {
		t.Error("another key shares u1's bucket")
	}
}

func TestRefill(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	drain(t, l, "u1", Free)

	c.advance(time.Second)
	if got := count(l, "u1", Free); got != 5 {
		t.Errorf("after 1s: %d allowed, want 5", got)
	}

	c.advance(time.Hour)
	if got := count(l, "u1", Free); got != Free.Burst {
		t.Errorf("after an hour: %d allowed, want the burst of %d", got, Free.Burst)
	}
}

func TestRetryAfter(t *testing.T) {
	p := Plan{Name: "slow", Rate: 0.25, Burst: 1}
	tests := []struct {
		wait time.Duration
		want time.Duration
	}{
		{0, 4 * time.Second},
		{time.Second, 3 * time.Second},
		{2500 * time.Millisecond, 1500 * time.Millisecond},
	}
	for _, tt := range tests {
		c := newClock()
		l := NewLimiter(c.now)
		drain(t, l, "u1", p)
		c.advance(tt.wait)
		ok, retry := l.Allow("u1", p)
		if ok || retry != tt.want {
			t.Errorf("after %v: Allow = %v, %v; want false, %v", tt.wait, ok, retry, tt.want)
		}
	}
}

func TestSweep(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	l.Allow("old", Free)
	c.advance(30 * time.Minute)
	l.Allow("recent", Free)
	c.advance(30 * time.Minute)

	if n := l.Sweep(time.Hour); n != 1 {
		t.Errorf("Sweep removed %d buckets, want 1", n)
	}
	if n := l.Sweep(time.Hour); n != 0 {
		t.Errorf("second Sweep removed %d buckets, want 0", n)
	}
	if got := len(l.buckets); got != 1 {
		t.Errorf("%d buckets left, want 1", got)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	tests := map[time.Duration]int64{
		0:                       1,
		time.Nanosecond:         1,
		time.Second:             1,
		1500 * time.Millisecond: 2,
		4 * time.Second:         4,
	}
	for d, want := range tests {
		if got := retryAfterSeconds(d); got != want {
			t.Errorf("retryAfterSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}

func TestMiddleware(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	p := Plan{Name: "tiny", Rate: 0.4, Burst: 1}
	key := func(r *http.Request) (string, Plan) { return r.Header.Get("X-User"), p }
	h := Middleware(l, key, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	serve := func(user string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/reminders", nil)
		req.Header.Set("X-User", user)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve("u1"); rec.Code != http.StatusNoContent {
		t.Fatalf("first request: status %d, want %d", rec.Code, http.StatusNoContent)
	}
	rec := serve("u1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want %q", got, "3")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
	if body.Error == "" || body.RetryAfter != 3 {
		t.Errorf("body = %+v, want an error and retry_after 3", body)
	}

	if rec := serve("u2"); rec.Code != http.StatusNoContent {
		t.Errorf("other user: status %d, want %d", rec.Code, http.StatusNoContent)
	}
	c.advance(3 * time.Second)
	if rec := serve("u1"); rec.Code != http.StatusNoContent {
		t.Errorf("after Retry-After: status %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func drain(t *testing.T, l *Limiter, key string, p Plan) {
	t.Helper()
	if n := count(l, key, p); n != p.Burst {
		t.Fatalf("drained %d tokens, want %d", n, p.Burst)
	}
}

// count takes tokens until Allow refuses and returns how many it took.
func count(l *Limiter, key string, p Plan) int {
	n := 0
	for {
		if ok, _ := l.Allow(key, p); !ok {
			return n
		}
		n++
	}
}
