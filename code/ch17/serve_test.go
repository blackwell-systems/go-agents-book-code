package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
)

const (
	testSecret = "ingest-secret-0123456789abcdef0123"
	testToken  = "review-token-ana-0123456789abcdef0"
)

// TestServe runs the service on a port: a report posted with the
// secret starts a run in its own workspace, the status route shows it
// done, and a shutdown stops the server and the runs and returns nil.
func TestServe(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp",
		"127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	env := map[string]string{"TRIAGE_INGEST_SECRET": testSecret,
		"TRIAGE_REVIEWERS": "# reviewers\nana " + testToken + "\n"}
	getenv := func(k string) string { return env[k] }
	c := config{repo: "..", context: "none", maxTurns: 6, maxCost: 0.25,
		state: t.TempDir(), approve: "later", defenses: "all",
		minRung: 2, daily: 1}
	ctx, stop := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- serve(ctx, getenv, &syncWriter{w: &out}, c,
			models{main: streamedFake{&groupModel{}}}, addr)
	}()
	base := "http://" + addr
	waitUp(t, base)

	body := `{"id":"crash-000001","kind":"panic","commit":"9c41e7a",` +
		`"time":"2026-10-16T09:00:00Z","panic":"runtime error: index ` +
		`out of range [3] with length 2","stack":"goroutine 1 ` +
		`[running]:\nexample.com/remind/store.(*Store).List()\n"}`
	if code := post(t, base+"/crashes", testSecret, body); code != http.StatusAccepted {
		t.Fatalf("POST /crashes: %d", code)
	}
	// The group's issue_file waits for a person (chapter 14's gate).
	var items []service.Item
	for range 600 {
		get(t, base+"/review", &items)
		if len(items) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(items) != 1 || items[0].Tool != "issue_file" {
		t.Fatalf("review items %+v", items)
	}
	if code := post(t, base+"/review/"+items[0].ID+"/approve", testToken,
		`{"reason":"ok"}`); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	var runs []service.Status
	for range 600 {
		runs = getRuns(t, base)
		if len(runs) == 1 && runs[0].State == service.Done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(runs) != 1 || runs[0].State != service.Done ||
		runs[0].Outcome != "needs_human" || runs[0].Issue == 0 {
		t.Fatalf("runs %+v", runs)
	}
	// A connection that never sends a request, as a client's spare
	// dial or a port scan leaves, mustn't fail the shutdown.
	idle, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	stop(agent.ErrShutdown)
	if err := <-done; err != nil {
		t.Errorf("serve after shutdown: %v", err)
	}
}

func TestServeNeedsSecrets(t *testing.T) {
	c := config{repo: "..", state: t.TempDir(), defenses: "all", daily: 1}
	err := serve(t.Context(), func(string) string { return "" },
		&bytes.Buffer{}, c, models{main: streamedFake{&groupModel{}}}, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("serve with no secret: %v", err)
	}
	c.defenses = "none"
	env := map[string]string{"TRIAGE_INGEST_SECRET": testSecret}
	err = serve(t.Context(), func(k string) string { return env[k] },
		&bytes.Buffer{}, c, models{main: streamedFake{&groupModel{}}},
		"127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "defenses") {
		t.Errorf("serve with -defenses none: %v", err)
	}
	c.defenses, c.daily = "all", 0
	err = serve(t.Context(), func(k string) string { return env[k] },
		&bytes.Buffer{}, c, models{main: streamedFake{&groupModel{}}},
		"127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "-daily") {
		t.Errorf("serve with no -daily: %v", err)
	}
	if _, err := parseReviewers("ana"); err == nil {
		t.Error("a reviewer line without a token was accepted")
	}
}

func waitUp(t *testing.T, base string) {
	t.Helper()
	for range 200 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
			base+"/runs", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the service never came up")
}

func post(t *testing.T, url, token, body string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url,
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// get reads a review route's JSON into v.
func get(t *testing.T, url string, v any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url,
		nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.UnmarshalRead(resp.Body, v); err != nil {
		t.Fatal(err)
	}
}

func getRuns(t *testing.T, base string) []service.Status {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		base+"/runs", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []service.Status
	if err := json.UnmarshalRead(resp.Body, &runs); err != nil &&
		!errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return runs
}

// syncWriter lets the test read what serve wrote while it runs.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p) //nolint:wrapcheck // a buffer never fails
}

func TestParseReviewersDuplicate(t *testing.T) {
	_, err := parseReviewers("ana " + testToken + "\nbo " + testToken + "\n")
	if err == nil {
		t.Error("two reviewers with one token were accepted")
	}
}
