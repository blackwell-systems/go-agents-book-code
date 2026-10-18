package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch14/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch14/replay"
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

// TestRecordedGroups replays the chapter's recorded -groups run of
// g3-mixed-batch, with the tools, merges, and grading running for real,
// and checks what the harness made of it. The agents' events interleave
// in a different order each time, so only the summary is compared.
func TestRecordedGroups(t *testing.T) {
	t.Parallel()
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
