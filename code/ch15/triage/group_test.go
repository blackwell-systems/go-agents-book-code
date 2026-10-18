package triage

import (
	"errors"
	"os"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// TestWorkspaceGone checks that the workspace tools fail with
// agent.ErrToolFatal once the workspace directory is deleted, and that
// the error is still redacted text the loop can report.
func TestWorkspaceGone(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	if err := os.RemoveAll(e.Dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"code_read", "run_tests", "code_patch"} {
		_, err := call(t, tools, name, map[string]any{"path": "store/store.go",
			"old": "a", "new": "b"})
		if !errors.Is(err, agent.ErrToolFatal) {
			t.Errorf("%s: err = %v, want ErrToolFatal", name, err)
		}
	}
	if _, err := call(t, tools, "crash_list", map[string]any{}); err != nil {
		t.Errorf("crash_list needs no workspace, but failed: %v", err)
	}
}

// TestGroupTools checks that a group agent can't write to the tracker,
// and that with FileLater a fixed group needs no issue number.
func TestGroupTools(t *testing.T) {
	e, _ := newTestEnv(t, "m1-done-after-delete")
	for _, tool := range GroupTools(e) {
		if tool.Def.Name == "issue_file" || tool.Def.Name == "issue_comment" {
			t.Errorf("group tools include %s", tool.Def.Name)
		}
	}
	d := []Diagnosis{{Fingerprint: "fp_a", Status: "fixed",
		Reports: reportIDs(e)}}
	if err := e.checkGroups(d); err == nil {
		t.Error("a fixed group without an issue passed without FileLater")
	}
	e.FileLater = true
	if err := e.checkGroups(d); err != nil {
		t.Errorf("with FileLater: %v", err)
	}
}

func reportIDs(e *Env) []string {
	var ids []string
	for _, r := range e.Reports {
		ids = append(ids, r.ID)
	}
	return ids
}
