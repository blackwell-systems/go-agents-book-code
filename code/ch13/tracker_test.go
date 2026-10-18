package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch13/mcptools"
	"github.com/blackwell-systems/go-agents-book/code/ch13/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch13/trackermcp"
	"github.com/blackwell-systems/go-agents-book/code/ch13/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// TestMain lets the test binary serve the tracker, as the program does
// for itself, so startTracker works in tests.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "-serve-tracker" {
		err := trackermcp.Serve(context.Background(), os.Args[2], taskrun.Now,
			os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestTrackerOverMCP starts the tracker's MCP server as a process, as
// the program does, and checks the agent's tools: the four issue tools
// come from the server with its prefix, read-only where the server
// says so, and an issue filed through the server shows in the job's
// issues, which submit_diagnosis and the grade read.
func TestTrackerOverMCP(t *testing.T) {
	ctx := t.Context()
	j, err := setUp(ctx, "..", "m1-done-after-delete")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	server, err := startTracker(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	tools, err := trackerTools(ctx, server, j, triage.Tools(j.Env), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]agent.Tool{}
	var names []string
	for _, tl := range tools {
		byName[tl.Def.Name] = tl
		names = append(names, tl.Def.Name)
	}
	for _, n := range triage.IssueNames {
		if slices.Contains(names, n) || !slices.Contains(names, "tracker__"+n) {
			t.Errorf("%s: tools are %v", n, names)
		}
	}
	if !byName["tracker__issue_search"].ReadOnly || byName["tracker__issue_file"].ReadOnly {
		t.Error("safety flags didn't follow the trusted server's hints")
	}
	before := len(j.Env.Issues.Search(tracker.Query{}))
	out, err := byName["tracker__issue_file"].Run(ctx, jsontext.Value(
		`{"title":"store.Get returns a deleted reminder","body":"...","fingerprint":"fp_4828a6"}`))
	if err != nil || !strings.HasPrefix(out, "filed #") {
		t.Fatalf("issue_file over MCP = %q, %v", out, err)
	}
	if after := len(j.Env.Issues.Search(tracker.Query{})); after != before+1 {
		t.Errorf("the job sees %d issues after filing, %d before", after, before)
	}
	if got := renameIssueTools(triage.Prompt); strings.Contains(got, " issue_file") ||
		!strings.Contains(got, "tracker__issue_file") {
		t.Errorf("prompt not renamed:\n%s", got)
	}
}

// TestRecordedMCPRun replays chapter 10's recorded run: the m1 task,
// with the tracker's tools served from the tracker's MCP server, started
// as a process, and every tool running for real.
func TestRecordedMCPRun(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	getenv := func(string) string { return "" }
	err := run(t.Context(), getenv, &out, config{task: "m1-done-after-delete",
		repo: "..", context: "default", maxTurns: 40, maxCost: 0.25,
		tracker: "mcp"})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "model asks:  tracker__issue_file") ||
		!strings.Contains(out.String(), "16 of 16 checks passed") {
		t.Errorf("the replay didn't file through the tracker and pass:\n%s", out.String())
	}
	got := testTimes.ReplaceAll(out.Bytes(), []byte("${1}0.000s"))
	golden := filepath.Join("testdata", "out", "m1-mcp.txt")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s; run go test -update to rewrite it", golden)
	}
}

// TestTrackerPins checks pinning as a reader does it: print the
// digests, save them, and run with them; a pin that no longer matches
// stops the tools from loading, with ErrUnreviewed.
func TestTrackerPins(t *testing.T) {
	ctx := t.Context()
	var out bytes.Buffer
	if err := printDigests(ctx, &out); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "pins.json")
	if err := os.WriteFile(file, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pins, err := loadPins(file)
	if err != nil || len(pins) != 4 {
		t.Fatalf("pins %v, %v", pins, err)
	}
	j, err := setUp(ctx, "..", "m1-done-after-delete")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	server, err := startTracker(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := trackerTools(ctx, server, j, triage.Tools(j.Env), pins); err != nil {
		t.Errorf("with the printed pins: %v", err)
	}
	pins["issue_search"] = strings.Repeat("0", 64)
	_, err = trackerTools(ctx, server, j, triage.Tools(j.Env), pins)
	if !errors.Is(err, mcptools.ErrUnreviewed) {
		t.Errorf("with a changed pin: %v", err)
	}
}
