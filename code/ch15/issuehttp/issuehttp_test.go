package issuehttp

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

const args = `{"title": "nil reminder on get", "body": "store.Get ` +
	`returns nil", "fingerprint": "fp_4828a6"}`

func serve(t *testing.T) (Tracker, *tracker.Store) {
	t.Helper()
	s, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tracker.NewHandler(s))
	t.Cleanup(srv.Close)
	return Tracker{HTTP: srv.Client(), URL: srv.URL}, s
}

// TestFileKeyed checks that the same key files one issue however often
// the call is sent, and that another key files another.
func TestFileKeyed(t *testing.T) {
	tr, s := serve(t)
	tool := tr.File()
	for _, tc := range []struct {
		key, want string
	}{
		{"k1", "filed #1"},
		{"k1", "filed #1"}, // a repeat after a restart
		{"k2", "filed #2"}, // a new call
	} {
		ctx := agent.WithKey(t.Context(), tc.key)
		got, err := tool.Run(ctx, jsontext.Value(args))
		if err != nil || got != tc.want {
			t.Errorf("key %s: got %q, %v; want %q", tc.key, got, err,
				tc.want)
		}
	}
	if n := len(s.Search(tracker.Query{})); n != 2 {
		t.Errorf("tracker holds %d issues, want 2", n)
	}
}

// TestKeyReused checks that a key sent with a different request is
// refused, as the tracker answers it with 422.
func TestKeyReused(t *testing.T) {
	tr, _ := serve(t)
	ctx := agent.WithKey(t.Context(), "k1")
	if _, err := tr.File().Run(ctx, jsontext.Value(args)); err != nil {
		t.Fatal(err)
	}
	other := `{"title": "another", "body": "b", "fingerprint": "fp_1"}`
	if _, err := tr.File().Run(ctx, jsontext.Value(other)); err == nil {
		t.Error("a reused key filed an issue")
	}
}

// TestResolve takes the tool up the ladder: a call whose key the
// tracker still holds is run again, an older one is looked up, and a
// lookup that finds nothing sends the call to a person.
func TestResolve(t *testing.T) {
	tr, _ := serve(t)
	tool := tr.File()
	call := agent.Call("c1", "issue_file", args)
	old := time.Now().Add(-25 * time.Hour)

	fresh := agent.Attempt{CallID: "c1", Tool: "issue_file", Key: "k1",
		Started: time.Now()}
	r, err := agent.Resolve(t.Context(), tool, "run", call, fresh, nil)
	if err != nil || r.Rung != agent.Retry || !r.Run {
		t.Errorf("held key: got %+v, %v; want a retry", r, err)
	}

	expired := fresh
	expired.Started = old
	_, err = agent.Resolve(t.Context(), tool, "run", call, expired, nil)
	if _, ok := errors.AsType[*agent.ApprovalPending](err); !ok {
		t.Errorf("nothing filed: got %v, want *ApprovalPending", err)
	}

	ctx := agent.WithKey(t.Context(), "k1")
	if _, err := tool.Run(ctx, jsontext.Value(args)); err != nil {
		t.Fatal(err)
	}
	keyless := agent.Attempt{CallID: "c1", Tool: "issue_file",
		Started: old}
	for _, a := range []agent.Attempt{expired, keyless} {
		r, err := agent.Resolve(t.Context(), tool, "run", call, a, nil)
		if err != nil || r.Rung != agent.Checked || r.Run ||
			r.Answer.Content != "filed #1 (found after a restart)" {
			t.Errorf("key %q, filed: got %+v, %v; want the lookup's "+
				"answer", a.Key, r, err)
		}
	}
}

var update = flag.Bool("update", false, "rewrite testdata/filing.txt")

// TestWire records a filing sent twice with the same key, as the
// tracker receives it and answers it, for the chapter: the request with
// its Idempotency-Key header, then each answer's status line and body.
func TestWire(t *testing.T) {
	s, err := tracker.Open("", func() time.Time {
		return time.Date(2026, 10, 17, 2, 21, 42, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	var first string
	h := tracker.NewHandler(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		r.Host = "tracker.example"
		if b.Len() == 0 {
			dump, err := httputil.DumpRequest(r, true)
			if err != nil {
				t.Error(err)
			}
			b.Write(dump)
			b.WriteString("\n\n")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", rec.Code,
			http.StatusText(rec.Code))
		body := jsontext.Value(bytes.Clone(rec.Body.Bytes()))
		switch {
		case first == "":
			first = string(body)
			if err := body.Indent(); err != nil {
				t.Error(err)
			}
			fmt.Fprintf(&b, "%s\n\n", body)
		case string(body) == first:
			b.WriteString("(the same body as the first answer)\n")
		default:
			fmt.Fprintf(&b, "%s\n", body)
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	defer srv.Close()
	tr := Tracker{HTTP: srv.Client(), URL: srv.URL}
	ctx := agent.WithKey(t.Context(), agent.Key("k1", "toolu_01"))
	short := `{"title": "Get panics", "body": "nil reminder", ` +
		`"fingerprint": "fp_4828a6"}`
	for range 2 {
		if _, err := tr.File().Run(ctx, jsontext.Value(short)); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.ReplaceAll(b.String(), "\r\n", "\n")
	path := filepath.Join("testdata", "filing.txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("wire differs from %s; run go test -update:\n%s", path,
			got)
	}
}

// gateway passes each request on to the tracker and answers a filing
// with 502 once the tracker has filed it, as a proxy that timed out on
// its upstream does.
func gateway(t *testing.T, s *tracker.Store, posts *atomic.Int32) Tracker {
	t.Helper()
	h := tracker.NewHandler(s)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if r.Method == http.MethodPost {
			posts.Add(1)
			http.Error(w, "upstream timeout", http.StatusBadGateway)
			return
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	return Tracker{HTTP: srv.Client(), URL: srv.URL}
}

// TestFilingUnknown checks a filing answered with 502 after the tracker
// filed it: the error is ErrOutcomeUnknown, never a tool error the model
// would answer with a new call and a new key. Under RetryTools a keyed
// filing is sent again with the same key, the tracker holds one issue,
// and when the retries run out the outcome is still unknown. Without a
// key it is not retried. A refusal, such as 400, stays a tool error.
func TestFilingUnknown(t *testing.T) {
	retry := agent.RetryTools(agent.Policy{Attempts: 3,
		Backoff: agent.Backoff{Rand: func() float64 { return 0 }}})
	for _, tc := range []struct {
		name  string
		key   string
		wrap  bool
		posts int32
	}{
		{"keyed, no retries", "k1", false, 1},
		{"keyed, under RetryTools", "k1", true, 3},
		{"no key", "", true, 1},
	} {
		s, err := tracker.Open("", time.Now)
		if err != nil {
			t.Fatal(err)
		}
		var posts atomic.Int32
		tool := gateway(t, s, &posts).File()
		if tc.wrap {
			tool = retry(tool)
		}
		ctx := t.Context()
		if tc.key != "" {
			ctx = agent.WithKey(ctx, tc.key)
		}
		_, err = tool.Run(ctx, jsontext.Value(args))
		if !errors.Is(err, agent.ErrOutcomeUnknown) {
			t.Errorf("%s: %v, want ErrOutcomeUnknown", tc.name, err)
		}
		n := len(s.Search(tracker.Query{}))
		if n != 1 || posts.Load() != tc.posts {
			t.Errorf("%s: %d issues after %d requests, want 1 after %d",
				tc.name, n, posts.Load(), tc.posts)
		}
	}
	tr, _ := serve(t)
	_, err := tr.File().Run(t.Context(), jsontext.Value(`{"title": ""}`))
	if err == nil || errors.Is(err, agent.ErrOutcomeUnknown) {
		t.Errorf("refused filing: %v, want a tool error", err)
	}
}

// TestLookupScope checks what the lookup takes as a call's issue: the
// one filed with its key; else one open issue with its fingerprint and
// title. A closed issue, such as last week's for a crash that came
// back, is not it, and two candidates are a question for a person.
func TestLookupScope(t *testing.T) {
	tr, s := serve(t)
	call := agent.Call("c1", "issue_file", args)
	old, _, err := s.CreateOnce("old-key", "nil reminder on get",
		"store.Get returns nil", nil, "fp_4828a6")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(old.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.lookup(t.Context(), call, ""); !errors.Is(err,
		errNotSure) {
		t.Errorf("closed issue only: %v, want errNotSure", err)
	}
	if _, _, err := tr.lookup(t.Context(), call, "k1"); !errors.Is(err,
		errNotSure) {
		t.Errorf("key not filed: %v, want errNotSure", err)
	}
	ctx := agent.WithKey(t.Context(), "k1")
	if _, err := tr.File().Run(ctx, jsontext.Value(args)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"k1", ""} {
		out, found, err := tr.lookup(t.Context(), call, key)
		if err != nil || !found || out != "filed #2 (found after a restart)" {
			t.Errorf("key %q: %q, %v, %v; want #2", key, out, found, err)
		}
	}
	if _, _, err := s.CreateOnce("k2", "nil reminder on get", "again",
		nil, "fp_4828a6"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.lookup(t.Context(), call, ""); !errors.Is(err,
		errNotSure) {
		t.Errorf("two open issues: %v, want errNotSure", err)
	}
	if out, found, err := tr.lookup(t.Context(), call, "k1"); err != nil ||
		!found || out != "filed #2 (found after a restart)" {
		t.Errorf("two open, by key: %q, %v, %v; want #2", out, found, err)
	}
}

// TestNilClient checks that a Tracker with no HTTP client uses the
// default one.
func TestNilClient(t *testing.T) {
	tr, _ := serve(t)
	tr.HTTP = nil
	if _, err := tr.File().Run(t.Context(), jsontext.Value(args)); err != nil {
		t.Error(err)
	}
}
