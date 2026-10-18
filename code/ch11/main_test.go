package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch11/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch11/replay"
)

var update = flag.Bool("update", false, "rewrite the golden output files")

// TestContextPolicy checks that every -context name builds a policy,
// and that an unknown name is an error that lists the names.
func TestContextPolicy(t *testing.T) {
	for _, name := range []string{"none", "cache", "clear", "trim", "compact", "default"} {
		p, err := contextPolicy(name, nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if (p == nil) != (name == "none") {
			t.Errorf("%s: policy = %v", name, p)
		}
	}
	if _, err := contextPolicy("everything", nil); err == nil ||
		!strings.Contains(err.Error(), "compact") {
		t.Errorf("unknown name: err = %v", err)
	}
	var _ agent.ContextPolicy = agent.CacheMarks{}
}

// TestRecordedRun replays a run recorded on the batch, with every tool
// running for real, and checks what the agent achieved and how the
// catalog graded it. It is skipped until the run is recorded (see
// testdata/replies/README.md).
func TestRecordedRun(t *testing.T) {
	const task = "batch"
	if _, err := fs.Stat(embedded, "testdata/replies/"+task+"-01.json"); err != nil {
		t.Skipf("no recorded run for %s yet", task)
	}
	var out bytes.Buffer
	getenv := func(string) string { return "" }
	err := run(t.Context(), getenv, &out, config{task: task, repo: "..",
		context: "default", maxTurns: 120, maxCost: 2,
		tracker: "builtin"}) // recorded with chapter 9's tools
	if err != nil && !errors.Is(err, agent.ErrBudget) {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "graded against the catalog") {
		t.Errorf("no grade in the output:\n%s", out.String())
	}
	golden := filepath.Join("testdata", "out", task+".txt")
	got := testTimes.ReplaceAll(out.Bytes(), []byte("${1}0.000s"))
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err == nil && !bytes.Equal(got, want) {
		t.Errorf("output differs from %s; run go test -update to rewrite it", golden)
	}
}

// testTimes matches the durations go test prints for each package,
// which change from run to run.
var testTimes = regexp.MustCompile(`(\t)\d+\.\d+s`)

// failing is a transport whose first fails calls fail before any reply.
type failing struct {
	fails, calls int
	bodies       []string
}

func (f *failing) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	f.bodies = append(f.bodies, string(b))
	if f.calls <= f.fails {
		return nil, errors.New("remote error: tls: bad record MAC")
	}
	return &http.Response{StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

// TestResend checks that a failed connection is tried once more, with
// the same body, and that a second failure is returned.
func TestResend(t *testing.T) {
	for _, tc := range []struct {
		fails, calls int
		ok           bool
	}{{0, 1, true}, {1, 2, true}, {2, 2, false}} {
		f := &failing{fails: tc.fails}
		req, err := http.NewRequestWithContext(t.Context(),
			http.MethodPost, "http://example.com", strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := resend{f}.RoundTrip(req)
		if (err == nil) != tc.ok || f.calls != tc.calls {
			t.Errorf("%d failures: err %v after %d calls, want ok %v after %d",
				tc.fails, err, f.calls, tc.ok, tc.calls)
		}
		if resp != nil {
			resp.Body.Close()
		}
		for _, b := range f.bodies {
			if b != "body" {
				t.Errorf("%d failures: sent body %q", tc.fails, b)
			}
		}
	}
}

// TestSaveInvalidArguments checks that a conversation whose tool call
// has broken arguments still saves as valid JSON, with the arguments
// kept as a string, and that the messages passed in are unchanged.
func TestSaveInvalidArguments(t *testing.T) {
	bad := `{"path": "a.go", , "end": 3}`
	msgs := []agent.Message{{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "1", Name: "read", Arguments: []byte(`{"path": "a.go"}`)},
		{ID: "2", Name: "read", Arguments: []byte(bad)},
	}}}
	file := filepath.Join(t.TempDir(), "run.json")
	if err := saveConversation(file, msgs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"{\"path\": \"a.go\", , \"end\": 3}"`) {
		t.Errorf("saved file doesn't hold the arguments as a string:\n%s", b)
	}
	if got := string(msgs[0].ToolCalls[1].Arguments); got != bad {
		t.Errorf("caller's arguments changed to %s", got)
	}
}

// TestDropped checks which failures count as a dropped connection, worth
// starting a group over for.
func TestDropped(t *testing.T) {
	op := &net.OpError{Op: "remote error", Err: errors.New("tls: bad record MAC")}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a broken stream", fmt.Errorf("read stream: %w", op), true},
		{"a failed send", &url.Error{Op: "Post", URL: "u", Err: op}, true},
		{"a deadline", &url.Error{Op: "Post", URL: "u", Err: context.DeadlineExceeded}, false},
		{"Ctrl-C", &url.Error{Op: "Post", URL: "u", Err: context.Canceled}, false},
		{"a bad reply", agent.ErrBadReply, false},
	} {
		if got := dropped(tc.err); got != tc.want {
			t.Errorf("%s: dropped = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRecordedGroups replays the chapter's recorded -groups run of
// g3-mixed-batch, with the tools, merges, and grading running for real,
// and checks what the harness made of it. The agents' events interleave
// in a different order each time, so only the summary is compared.
func TestRecordedGroups(t *testing.T) {
	var out bytes.Buffer
	getenv := func(string) string { return "" }
	err := run(t.Context(), getenv, &out, config{task: "g3-mixed-batch",
		repo: "..", context: "default", groups: true, parallel: 4,
		maxTurns: 40, maxCost: 0.5})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"6 crash reports in 5 groups, 4 at a time",
		"4 diagnoses; 0 groups unresolved; 0 groups' changes left out",
		"filed #4 (*API).Routes in api/api.go: nil value",
		"graded against the catalog: 22 of 22 checks passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output has no %q:\n%s", want, out.String())
		}
	}
}

// TestNewClient checks which client each -provider gets, that a direct
// Anthropic key alone is enough to call the live API, and that a model
// the price table doesn't list is refused instead of costing $0.
func TestNewClient(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	m, err := newClient(env(map[string]string{"ANTHROPIC_API_KEY": "k"}),
		io.Discard, config{provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := m.(*anthropic.Client)
	if !ok || a.BaseURL != anthropic.DefaultBaseURL ||
		a.Model != "claude-haiku-5-5" || a.APIKey != "k" {
		t.Fatalf("direct client = %+v", m)
	}
	if _, replays := a.HTTP.Transport.(*replay.Transport); replays {
		t.Error("a direct Anthropic key replays saved calls")
	}
	_, err = newClient(env(map[string]string{"OPENROUTER_API_KEY": "k",
		"OPENROUTER_MODEL": "some/unpriced-model"}), io.Discard,
		config{provider: "anthropic"})
	if err == nil || !strings.Contains(err.Error(), "no price") {
		t.Errorf("unpriced model: err = %v", err)
	}
	if _, err := newClient(env(nil), io.Discard,
		config{provider: "other"}); err == nil {
		t.Error("an unknown provider was accepted")
	}
}
