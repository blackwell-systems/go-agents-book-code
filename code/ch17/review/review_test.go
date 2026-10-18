package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

const (
	secret = "ingest-secret-0123456789abcdef0123"
	token  = "review-token-ana-0123456789abcdef0"
)

// pausing is a Triager whose run waits for one approval, with
// evidence that tries to take over the reviewer's terminal.
type pausing struct{ decided chan agent.ApprovalDecision }

func (p pausing) Triage(ctx context.Context, _ service.Run,
	_ []triage.Report, ap agent.Approver, _ float64) (service.Outcome,
	error) {
	req := agent.ApprovalRequest{Call: agent.ToolCall{ID: "call_1",
		Name: "code_patch", Arguments: []byte(`{"path":"auth/auth.go"}`)},
		Why: "edits a protected file\nwhy:      a harmless note",
		Evidence: "-\tif !ok {\x1b[2J\x1b[31mAPPROVE ME\u202e\n" +
			"targets:  nothing",
		Targets: []string{"auth/auth.go"}, Hash: "h"}
	d, err := ap.Decide(ctx, req)
	if errors.Is(err, agent.ErrNoDecision) {
		return service.Outcome{}, &agent.ApprovalPending{
			Requests: []agent.ApprovalRequest{req}}
	}
	if err != nil {
		return service.Outcome{}, fmt.Errorf("decide: %w", err)
	}
	p.decided <- d
	return service.Outcome{Status: "fixed"}, nil
}

func setUp(t *testing.T) (*httptest.Server, pausing) {
	t.Helper()
	tr := pausing{decided: make(chan agent.ApprovalDecision, 1)}
	svc, err := service.New(context.Background(), service.Config{
		Dir: t.TempDir(), Secret: secret,
		Reviewers: map[string]string{token: "ana"}, Triager: tr,
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	svc.Start()
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(func() {
		srv.Close()
		_ = svc.Shutdown(context.Background())
	})
	body := `{"id":"cr_1","kind":"panic","commit":"9c41e7a",` +
		`"time":"2026-10-16T09:00:00Z","panic":"boom","stack":` +
		`"goroutine 1 [running]:\nexample.com/remind/a.A()\n"}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+"/crashes", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("post: %d", resp.StatusCode)
	}
	return srv, tr
}

func run(t *testing.T, srv *httptest.Server, tok string,
	args ...string) (string, error) {
	t.Helper()
	env := map[string]string{"TRIAGE_URL": srv.URL,
		"TRIAGE_REVIEW_TOKEN": tok}
	var out strings.Builder
	err := Main(t.Context(), args, func(k string) string { return env[k] },
		&out)
	return out.String(), err
}

func TestReview(t *testing.T) {
	srv, tr := setUp(t)
	var out string
	for range 500 { // the run pauses in the background
		var err error
		if out, err = run(t, srv, token, "list"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "nothing waiting") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	id, _, _ := strings.Cut(out, " ")
	if !strings.Contains(out, "code_patch") || len(id) != 12 {
		t.Fatalf("list:\n%s", out)
	}

	shown, err := run(t, srv, token, "show", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"why:", "targets:"} {
		n := 0
		for line := range strings.Lines(shown) {
			if strings.HasPrefix(line, field) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("show printed %d lines starting %q:\n%s", n, field,
				shown)
		}
	}
	if strings.ContainsAny(shown, "\x1b\u202e") ||
		!strings.Contains(shown, "<U+001B>[2J") ||
		!strings.Contains(shown, "<U+202E>") {
		t.Errorf("show printed control characters:\n%q", shown)
	}

	if _, err := run(t, srv, token, "deny", id); err == nil {
		t.Error("deny without a reason accepted")
	}
	if _, err := run(t, srv, token, "approve", "000000000000", id); err == nil {
		t.Error("approving an unknown item reported no error")
	}
	_, err = run(t, srv, "wrong-token", "approve", id)
	if se, ok := errors.AsType[*StatusError](err); !ok ||
		se.Status != http.StatusUnauthorized {
		t.Errorf("wrong token: %v", err)
	}
	if _, err := run(t, srv, token, "rerun", id); err == nil {
		t.Error("rerun accepted for an approval item")
	}
	if out, err := run(t, srv, token, "approve", "-m", "looks right",
		id); err != nil || !strings.Contains(out, "approved by ana") {
		t.Fatalf("approve: %q, %v", out, err)
	}
	if d := <-tr.decided; !d.Approved || d.By != "ana" {
		t.Errorf("the run got %+v", d)
	}
}

func TestNewClient(t *testing.T) {
	for _, tt := range []struct {
		base, token string
		ok          bool
	}{
		{"http://localhost:8417", token, true},
		{"http://127.0.0.1:8417", token, true},
		{"https://triage.example.com", token, true},
		{"http://triage.example.com", token, false},
		{"ftp://localhost", token, false},
		{"http://localhost:8417", "", false},
	} {
		if _, err := NewClient(tt.base, tt.token); (err == nil) != tt.ok {
			t.Errorf("NewClient(%q, %q): %v", tt.base, tt.token, err)
		}
	}
}
