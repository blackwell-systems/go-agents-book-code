package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// testResult matches run_tests' answer as the run prints it, a call
// and its one-line preview, such as
//
//	model asks:  run_tests
//	               package: "./store ./auth ./notify"
//	tool result: get_deleted_test.go:18: Get(deleted) err... (42 lines)
//
// The preview is the first line of the tests' output, and with tests in
// several packages that is the failure of whichever package finished
// first, which changes from run to run; the golden comparison masks it.
var testResult = regexp.MustCompile(
	`(?m)^(  model asks:  run_tests.*\n(?: {17}.*\n)*)  tool result: .*$`)

// TestRecordedProviders replays the m1 task through chapter 11's two
// new clients, each from its own recording: OpenRouter's Go SDK, and
// the Messages API through OpenRouter. The tools, the tracker's MCP
// server included, run for real.
func TestRecordedProviders(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"sdk", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			getenv := func(string) string { return "" }
			err := run(t.Context(), getenv, &out, config{
				task: "m1-done-after-delete", repo: "..",
				context: "default", maxTurns: 40, maxCost: 0.25,
				tracker: "mcp", provider: provider})
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			if !strings.Contains(out.String(), "16 of 16 checks passed") {
				t.Errorf("the replay didn't pass:\n%s", out.String())
			}
			got := testTimes.ReplaceAll(out.Bytes(), []byte("${1}0.000s"))
			golden := filepath.Join("testdata", "out", "m1-"+provider+".txt")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			mask := func(b []byte) []byte {
				return testResult.ReplaceAll(b, []byte("${1}  tool result: ..."))
			}
			if !bytes.Equal(mask(got), mask(want)) {
				t.Errorf("output differs from %s; run go test -update", golden)
			}
		})
	}
}

// TestFaultsEveryProvider replays the m1 task through each of the three
// clients with a 503 on the first request and a dropped connection on
// the second: the retries (chapter 12) carry the run through, and it
// passes as the unfaulted replay does.
func TestFaultsEveryProvider(t *testing.T) {
	t.Parallel()
	now := agent.Backoff{Rand: func() float64 { return 0 }}
	for _, provider := range []string{"openrouter", "sdk", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			getenv := func(string) string { return "" }
			err := run(t.Context(), getenv, &out, config{
				task: "m1-done-after-delete", repo: "..",
				context: "default", maxTurns: 40, maxCost: 0.25,
				tracker: "mcp", provider: provider, backoff: now,
				faults: []agent.Fault{{Status: 503}, {Drop: true}}})
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			for _, want := range []string{"(retry 1 in 0s, status:",
				"(retry 2 in 0s, network:", "16 of 16 checks passed"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("no %q in:\n%s", want, out.String())
				}
			}
		})
	}
}
