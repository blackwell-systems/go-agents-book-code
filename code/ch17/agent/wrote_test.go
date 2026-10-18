package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writer is a side-effecting tool that writes a.go in dir and records
// the write, as code_patch does.
func writer(dir string) Tool {
	return Tool{Def: ToolDef{Name: "code_patch"},
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			data := []byte("package a\n")
			if err := os.WriteFile(filepath.Join(dir, "a.go"), data,
				0o644); err != nil {
				return "", fmt.Errorf("write: %w", err)
			}
			sum := sha256.Sum256(data)
			if err := Wrote(ctx, "a.go", hex.EncodeToString(sum[:])); err != nil {
				return "", err
			}
			return "changed a.go", nil
		}}
}

// TestWrote: a write is in the checkpoint before its call returns, and
// a resumed run whose workspace changed stops before any model call.
func TestWrote(t *testing.T) {
	dir := t.TempDir()
	script := []Message{
		Calls(call("c1", "code_patch", `{}`)),
		Calls(call("c2", "code_patch", `{}`)), // the same file again
		Says("done"),
	}
	m := newCounted(script)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	st.Workspace = dir
	lim := Limits{MaxTurns: 2, Checkpoint: cp}
	_, err := Resume(t.Context(), Streamed(m), []Tool{writer(dir)}, st, lim)
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("first run: %v, want ErrMaxTurns", err)
	}
	saved := cp.last(t)
	if len(saved.Files) != 1 || saved.Files[0].Path != "a.go" ||
		saved.Workspace != dir {
		t.Fatalf("saved files %+v in %q", saved.Files, saved.Workspace)
	}

	// Unchanged: the resumed run goes on.
	calls := m.calls
	if _, err := Resume(t.Context(), Streamed(m), []Tool{writer(dir)},
		saved, Limits{MaxTurns: 3, Checkpoint: cp}); err != nil {
		t.Fatalf("resume over an unchanged workspace: %v", err)
	}
	if m.calls == calls {
		t.Error("the resumed run made no model call")
	}

	// Changed: it stops at once.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"),
		0o644); err != nil {
		t.Fatal(err)
	}
	calls = m.calls
	_, err = Resume(t.Context(), Streamed(m), []Tool{writer(dir)}, saved,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrWorkspaceChanged) || m.calls != calls {
		t.Errorf("resume over a changed file: %v after %d model calls",
			err, m.calls-calls)
	}

	// Gone: the same.
	gone := saved
	gone.Workspace = filepath.Join(dir, "missing")
	_, err = Resume(t.Context(), Streamed(m), nil, gone,
		Limits{MaxTurns: 5})
	if !errors.Is(err, ErrWorkspaceChanged) {
		t.Errorf("resume with the workspace gone: %v", err)
	}
}

// TestWroteOutsideRun: outside a saved run, and for a read-only tool,
// Wrote does nothing.
func TestWroteOutsideRun(t *testing.T) {
	if err := Wrote(t.Context(), "a.go", "h"); err != nil {
		t.Errorf("Wrote outside a run: %v", err)
	}
	dir := t.TempDir()
	m := newCounted([]Message{Calls(call("c1", "code_patch", `{}`)),
		Says("done")})
	res, err := Run(t.Context(), m, []Tool{writer(dir)},
		[]Message{{Role: "user", Content: "go"}}, Limits{MaxTurns: 3})
	if err != nil || res.Turns != 2 {
		t.Errorf("a run with no checkpoint: %d turns, %v", res.Turns, err)
	}
}
