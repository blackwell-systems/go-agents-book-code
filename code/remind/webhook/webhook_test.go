package webhook

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("s3cret")
	t0     = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
)

func TestSign(t *testing.T) {
	got := Sign(secret, t0, []byte(`{"a":1}`))
	prefix := "t=1772355600,v1="
	if !strings.HasPrefix(got, prefix) || len(got) != len(prefix)+64 {
		t.Errorf("Sign = %q, want %s followed by 64 hex digits", got, prefix)
	}
	if again := Sign(secret, t0, []byte(`{"a":1}`)); again != got {
		t.Errorf("Sign is not deterministic: %q then %q", got, again)
	}
}

func TestVerify(t *testing.T) {
	body := []byte(`{"reminder":"r_1"}`)
	good := Sign(secret, t0, body)
	tests := []struct {
		name    string
		header  string
		body    []byte
		secret  []byte
		now     time.Time
		wantErr error
	}{
		{"good", good, body, secret, t0.Add(time.Minute), nil},
		{"two signatures", good + ",v1=00ff", body, secret, t0, nil},
		{"tampered body", good, []byte(`{"reminder":"r_2"}`), secret, t0, ErrBadSignature},
		{"wrong secret", good, body, []byte("other"), t0, ErrBadSignature},
		{"old timestamp", good, body, secret, t0.Add(10 * time.Minute), ErrStale},
		{"future timestamp", good, body, secret, t0.Add(-10 * time.Minute), ErrStale},
		{"empty", "", body, secret, t0, ErrMalformedHeader},
		{"no equals", "garbage", body, secret, t0, ErrMalformedHeader},
		{"bad timestamp", "t=soon,v1=00", body, secret, t0, ErrMalformedHeader},
		{"no timestamp", "v1=00", body, secret, t0, ErrMalformedHeader},
		{"no signature", "t=1772355600", body, secret, t0, ErrMalformedHeader},
		{"bad hex", "t=1772355600,v1=zz", body, secret, t0, ErrMalformedHeader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(tt.secret, tt.header, tt.body, tt.now, 5*time.Minute)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Verify = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPost(t *testing.T) {
	tests := []struct {
		status    int
		wantErr   bool
		permanent bool
	}{
		{http.StatusOK, false, false},
		{http.StatusNoContent, false, false},
		{http.StatusBadRequest, true, true},
		{http.StatusTooManyRequests, true, false},
		{http.StatusServiceUnavailable, true, false},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var got *http.Request
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			c := &Client{HTTP: srv.Client(), Secret: secret, Now: func() time.Time { return t0 }}
			d := &Delivery{ID: "whd_000001", URL: srv.URL, Event: "reminder.sent", Payload: []byte(`{"id":"r_1"}`)}
			err := c.Post(context.Background(), d)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Post = %v, want error: %v", err, tt.wantErr)
			}
			if errors.Is(err, ErrPermanent) != tt.permanent {
				t.Errorf("Post = %v, want permanent: %v", err, tt.permanent)
			}
			if d.LastStatus != tt.status {
				t.Errorf("LastStatus = %d, want %d", d.LastStatus, tt.status)
			}
			if wantDone := !tt.wantErr || tt.permanent; d.Done != wantDone {
				t.Errorf("Done = %v, want %v", d.Done, wantDone)
			}
			if (d.LastError != "") != tt.wantErr {
				t.Errorf("LastError = %q", d.LastError)
			}

			if got.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", got.Method)
			}
			for k, want := range map[string]string{
				"Content-Type":      "application/json",
				"X-Remind-Event":    "reminder.sent",
				"X-Remind-Delivery": "whd_000001",
			} {
				if v := got.Header.Get(k); v != want {
					t.Errorf("%s = %q, want %q", k, v, want)
				}
			}
			sig := got.Header.Get("X-Remind-Signature")
			if err := Verify(secret, sig, gotBody, t0, time.Minute); err != nil {
				t.Errorf("signature %q does not verify: %v", sig, err)
			}
		})
	}
}

func TestPostUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	d := &Delivery{ID: "whd_000001", URL: url, Payload: []byte(`{}`)}
	err := (&Client{}).Post(context.Background(), d)
	if err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("Post = %v, want a retryable error", err)
	}
	if d.Done || d.LastStatus != 0 || d.LastError == "" {
		t.Errorf("delivery = %+v, want pending with an error and no status", d)
	}
}

func newQueue(now *time.Time) *Queue {
	return &Queue{
		Now:  func() time.Time { return *now },
		Rand: func() float64 { return 0.5 },
	}
}

func TestQueueAdd(t *testing.T) {
	now := t0
	q := newQueue(&now)
	for i, want := range []string{"whd_000001", "whd_000002"} {
		d, err := q.Add("https://example.com/hook", "reminder.sent", map[string]int{"n": i})
		if err != nil {
			t.Fatal(err)
		}
		if d.ID != want || !d.Created.Equal(t0) || !d.Next.Equal(t0) {
			t.Errorf("Add = %+v, want ID %s due at %v", d, want, t0)
		}
	}
	if _, err := q.Add("https://example.com/hook", "bad", func() {}); err == nil {
		t.Error("Add with an unencodable payload succeeded")
	}
	if got := len(q.Due(t0)); got != 2 {
		t.Errorf("Due = %d deliveries, want 2", got)
	}
}

func TestQueueBackoff(t *testing.T) {
	now := t0
	q := newQueue(&now)
	q.MaxAttempts = 10
	d, err := q.Add("https://example.com/hook", "reminder.sent", nil)
	if err != nil {
		t.Fatal(err)
	}
	retry := errors.New("503")
	// Rand returns 0.5, so each wait is half the capped exponential delay.
	want := []time.Duration{
		15 * time.Second,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		30 * time.Minute,
		30 * time.Minute,
	}
	for i, w := range want {
		if err := q.Record(d, retry); err != nil {
			t.Fatal(err)
		}
		if got := d.Next.Sub(now); got != w {
			t.Errorf("after attempt %d: wait %v, want %v", i+1, got, w)
		}
		if len(q.Due(d.Next.Add(-time.Second))) != 0 {
			t.Errorf("after attempt %d: due before its next attempt", i+1)
		}
		now = d.Next
	}
}

func TestQueueGiveUp(t *testing.T) {
	now := t0
	q := newQueue(&now)
	d, err := q.Add("https://example.com/hook", "reminder.sent", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range DefaultMaxAttempts {
		if d.Done {
			t.Fatalf("done after %d attempts, want %d", d.Attempts, DefaultMaxAttempts)
		}
		if err := q.Record(d, errors.New("connection refused")); err != nil {
			t.Fatal(err)
		}
	}
	if !d.Done || d.Attempts != DefaultMaxAttempts {
		t.Errorf("delivery = %+v, want done after %d attempts", d, DefaultMaxAttempts)
	}
	if pending, failed, done := q.Stats(); pending != 0 || failed != 1 || done != 0 {
		t.Errorf("Stats = %d, %d, %d; want 0, 1, 0", pending, failed, done)
	}
}

func TestQueueRecordOutcomes(t *testing.T) {
	now := t0
	q := newQueue(&now)
	ok, _ := q.Add("https://example.com/a", "e", nil)
	perm, _ := q.Add("https://example.com/b", "e", nil)
	if _, err := q.Add("https://example.com/c", "e", nil); err != nil {
		t.Fatal(err)
	}

	if err := q.Record(ok, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Record(perm, ErrPermanent); err != nil {
		t.Fatal(err)
	}
	if pending, failed, done := q.Stats(); pending != 1 || failed != 1 || done != 1 {
		t.Errorf("Stats = %d, %d, %d; want 1, 1, 1", pending, failed, done)
	}
	if err := q.Record(&Delivery{ID: "whd_999999"}, nil); !errors.Is(err, ErrUnknownDelivery) {
		t.Errorf("Record unknown = %v, want ErrUnknownDelivery", err)
	}
}
