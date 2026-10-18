package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

var (
	// retryLines matches the lines chapter 12's retries print.
	retryLines = regexp.MustCompile(`(?m)^  \(retry \d+ in .*\)\n`)
	// testResult matches run_tests' answer as the run prints it: its
	// first line, which, for tests in several packages, is the failure
	// of whichever package finished first.
	testResult = regexp.MustCompile(
		`(?m)^(  model asks:  run_tests.*\n(?: {17}.*\n)*)  tool result: .*$`)
)

// TestRecordedProviders replays the m1 task through each of the three
// clients from its own recording: OpenRouter's chat completions with
// the tracker's tools served from its MCP server (chapter 10),
// OpenRouter's Go SDK, and the Messages API through OpenRouter (chapter
// 11). Every tool runs for real. Each replay meets a 503 on its first
// request and a dropped connection on its second: the retries (chapter
// 12) carry the run through, and apart from the retry lines its output
// is the recorded run's, golden file for golden file.
func TestRecordedProviders(t *testing.T) {
	t.Parallel()
	now := agent.Backoff{Rand: func() float64 { return 0 }}
	for _, tc := range []struct{ provider, golden, want string }{
		{"openrouter", "m1-mcp", "model asks:  tracker__issue_file"},
		{"sdk", "m1-sdk", ""},
		{"anthropic", "m1-anthropic", ""},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			getenv := func(string) string { return "" }
			err := run(t.Context(), getenv, &out, config{
				task: "m1-done-after-delete", repo: "..",
				context: "default", maxTurns: 40, maxCost: 0.25,
				tracker: "mcp", provider: tc.provider, backoff: now,
				faults: []agent.Fault{{Status: 503}, {Drop: true}}})
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			for _, want := range []string{"(retry 1 in 0s, status:",
				"(retry 2 in 0s, network:", "16 of 16 checks passed",
				tc.want} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("no %q in:\n%s", want, out.String())
				}
			}
			got := testTimes.ReplaceAll(out.Bytes(), []byte("${1}0.000s"))
			got = retryLines.ReplaceAll(got, nil)
			golden := filepath.Join("testdata", "out", tc.golden+".txt")
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
