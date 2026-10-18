package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
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
		context: "default", maxTurns: 120, maxCost: 2})
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
