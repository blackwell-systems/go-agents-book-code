package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
)

// TestStateDone runs a recorded task with -state, then starts the same
// command again: the run is resumed as done, with no model call (the
// replay has no recording for one), and reports the same grade. The
// run's files are readable only by their owner.
func TestStateDone(t *testing.T) {
	t.Parallel()
	const task = "m1-done-after-delete"
	if _, err := fs.Stat(embedded, "testdata/replies/"+task+"-01.json"); err != nil {
		t.Skipf("no recorded run for %s", task)
	}
	state := filepath.Join(t.TempDir(), "state")
	c := config{task: task, repo: "..", context: "default", maxTurns: 40,
		maxCost: 0.25, tracker: "mcp", state: state, maxResumes: 3,
		runID: task + "-1"}
	getenv := func(string) string { return "" }
	var first, second bytes.Buffer
	if err := run(t.Context(), getenv, &first, c); err != nil {
		t.Fatalf("first run: %v\n%s", err, first.String())
	}
	if err := run(t.Context(), getenv, &second, c); err != nil {
		t.Fatalf("second run: %v\n%s", err, second.String())
	}
	if !strings.Contains(second.String(), "resuming run "+c.runID+": done") {
		t.Errorf("second run didn't resume a done run:\n%s", second.String())
	}
	grade := func(out string) string {
		_, after, _ := strings.Cut(out, "graded against the catalog")
		return after
	}
	if g := grade(second.String()); g == "" || g != grade(first.String()) {
		t.Errorf("grades differ:\nfirst:\n%s\nsecond:\n%s", first.String(),
			second.String())
	}
	for _, name := range []string{state, filepath.Join(state, c.runID+".json")} {
		fi, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: mode %v, readable by others", name, fi.Mode().Perm())
		}
	}
}

// TestPickRun checks how a start picks its run: the newest unfinished
// run of the task, then a new one; and a pruned ID is refused.
func TestPickRun(t *testing.T) {
	state := t.TempDir()
	save := func(id string, phase agent.Phase, at time.Time) {
		cp, err := durable.Open(state, id)
		if err != nil {
			t.Fatal(err)
		}
		defer cp.Close()
		st := agent.NewState(id, nil)
		st.Phase = phase
		if err := cp.Save(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(state, id+".json"), at,
			at); err != nil {
			t.Fatal(err)
		}
	}
	save("m1-20261016T100000-AAAAAAAA", agent.PhaseTools, time.Now())
	time.Sleep(10 * time.Millisecond) // Saved times differ
	save("m1-20261016T110000-BBBBBBBB", agent.PhaseModel, time.Now())
	save("m1-20261016T120000-CCCCCCCC", agent.PhaseDone, time.Now())
	if id, ok := latestRun(state, "m1"); !ok ||
		id != "m1-20261016T110000-BBBBBBBB" {
		t.Errorf("latestRun = %q, %v; want the B run", id, ok)
	}
	save("m1-done-after-delete-20261016T130000-DDDDDDDD", agent.PhaseTools,
		time.Now())
	if id, _ := latestRun(state, "m1"); id != "m1-20261016T110000-BBBBBBBB" {
		t.Errorf("latestRun picked %q, another task's run", id)
	}
	if _, ok := latestRun(state, "other"); ok {
		t.Error("found a run for another task")
	}
	if n, err := durable.Prune(state, time.Nanosecond,
		time.Now().Add(time.Hour)); n != 1 || err != nil {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	c := config{state: state, task: "m1", runID: "m1-20261016T120000-CCCCCCCC"}
	if !pruned(c) {
		t.Error("a pruned run's ID was taken as new")
	}
	// A start that failed before its first save leaves only a lock
	// file; its ID never ran, and can be used.
	failed := config{state: state, task: "m1", runID: "m1-failed"}
	if saved, err := lockRun(failed); err != nil {
		t.Fatal(err)
	} else {
		saved.cp.Close()
	}
	if pruned(failed) {
		t.Error("an ID whose first start failed is refused")
	}
	var out bytes.Buffer
	if _, err := pickRun(&c, &printer{w: &out}); err == nil {
		t.Error("pickRun reused a pruned ID")
	}
}

// TestKeptMarker checks that a resumed run keeps its data marker's
// boundary, which its saved conversation's rule names, from the file
// boundary or, for a run saved before the rename, marker.
func TestKeptMarker(t *testing.T) {
	work := t.TempDir()
	first, err := keptMarker(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "boundary")); err != nil {
		t.Errorf("no boundary file: %v", err)
	}
	again, err := keptMarker(work)
	if err != nil || again.Boundary() != first.Boundary() {
		t.Errorf("restart: boundary %q, want %q (%v)", again.Boundary(),
			first.Boundary(), err)
	}
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "marker"),
		[]byte("oldboundary1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m, err := keptMarker(old); err != nil || m.Boundary() != "oldboundary1" {
		t.Errorf("old name: boundary %q, %v", m.Boundary(), err)
	}
}

// TestMissingWorkspace checks that a run with a checkpoint whose
// workspace isn't whole stops instead of starting over in a fresh one.
func TestMissingWorkspace(t *testing.T) {
	c := config{state: t.TempDir(), task: "m1-done-after-delete",
		runID: "m1-x", repo: ".."}
	saved, err := lockRun(c)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.cp.Close()
	if err := saved.cp.Save(t.Context(), agent.NewState("m1-x",
		nil)); err != nil {
		t.Fatal(err)
	}
	_, err = setUpRun(t.Context(), c, saved)
	if !errors.Is(err, agent.ErrWorkspaceChanged) {
		t.Errorf("setUpRun: %v, want ErrWorkspaceChanged", err)
	}
}

// TestFirstTurn checks the number of the first turn heading a start
// prints: a new run's is 1; a resumed run with calls still to run, or
// waiting for a person, is in its saved turn; one saved after a turn's
// calls were all answered, as when a crash cuts off the next model
// call, is in the turn after; and a finished run has none.
func TestFirstTurn(t *testing.T) {
	calls := agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "c1", Name: "code_read"}}}
	answer := agent.Message{Role: "tool", ToolCallID: "c1", Content: "ok"}
	saved := func(phase agent.Phase, msgs ...agent.Message) *savedRun {
		st := agent.NewState("run-1", append([]agent.Message{
			{Role: "user", Content: "go"}}, msgs...))
		st.Phase, st.Turns = phase, 1
		return &savedRun{st: st, resumed: true}
	}
	for _, tc := range []struct {
		name  string
		saved *savedRun
		want  int
	}{
		{"new run", nil, 1},
		{"model", saved(agent.PhaseModel, calls, answer), 2},
		{"tools, a call left", saved(agent.PhaseTools, calls), 1},
		{"tools, all answered", saved(agent.PhaseTools, calls, answer), 2},
		{"paused", saved(agent.PhasePaused, calls), 1},
		{"done", saved(agent.PhaseDone, calls, answer,
			agent.Message{Role: "assistant", Content: "done"}), 0},
	} {
		if got := firstTurn(tc.saved); got != tc.want {
			t.Errorf("%s: turn %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestDieAfterNames checks -die-after's checks: it needs -state, and
// names a built-in tool of the run; a name no such tool has, such as
// one served over MCP, is refused with the names it can be.
func TestDieAfterNames(t *testing.T) {
	tools := []agent.Tool{
		{Def: agent.ToolDef{Name: "code_patch"}},
		{Def: agent.ToolDef{Name: "tracker__issue_file"}},
	}
	builtin := []string{"code_patch", "issue_file"} // issue_file dropped
	for _, tc := range []struct {
		name, state, want string
	}{
		{"code_patch", "", "needs -state"},
		{"issue_file", "/s", `no built-in tool "issue_file"`},
		{"tracker__issue_file", "/s", "try one of code_patch"},
		{"code_patch", "/s", ""},
	} {
		got, err := withDieAfter(tools, builtin, tc.name, tc.state)
		switch {
		case tc.want == "" && (err != nil || len(got) != 2):
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil ||
			!strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s, state %q: %v, want %q", tc.name, tc.state, err,
				tc.want)
		}
	}
}
