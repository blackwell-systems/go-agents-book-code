package durable

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// TestPruneKeepsLock checks the race Prune once had: a process that
// opened a run's lock file just before Prune deleted the run, and then
// took the lock, held the only lock on that run, since Prune leaves the
// lock file in place.
func TestPruneKeepsLock(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c, err := Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	c.noSync = true
	c.now = func() time.Time { return now.Add(-72 * time.Hour) }
	st := agent.NewState("run-1", nil)
	st.Phase = agent.PhaseDone
	if err := c.Save(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	c.Close()

	lockFile := filepath.Join(dir, "run-1.lock")
	b, err := os.OpenFile(lockFile, os.O_RDWR, 0o600) // B: about to lock
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if n, err := Prune(dir, 24*time.Hour, now); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if err := lock(b); err != nil { // B takes the lock
		t.Fatal(err)
	}
	if _, err := Open(dir, "run-1"); !errors.Is(err, ErrLocked) {
		t.Errorf("C opened the run while B holds its lock: %v", err)
	}
}

// TestLoadOtherRun checks that a checkpoint copied from another run is
// refused, since its keys would be sent again.
func TestLoadOtherRun(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	a.noSync = true
	if err := a.Save(t.Context(), agent.NewState("run-1", nil)); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := os.ReadFile(filepath.Join(dir, "run-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run-2.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, "run-2")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Load(); err == nil {
		t.Error("loaded run-1's checkpoint as run-2")
	}
}

// TestSaveOtherRun checks that Save refuses a state of another run,
// which Load would then refuse to read.
func TestSaveOtherRun(t *testing.T) {
	c, err := Open(t.TempDir(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.noSync = true
	if err := c.Save(t.Context(), agent.NewState("", nil)); err == nil {
		t.Error("saved a state with no run ID")
	}
}

// TestPruneForeign checks that stray files in the state directory, and
// a checkpoint this code can't read, don't stop Prune from pruning the
// rest.
func TestPruneForeign(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c, err := Open(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	c.noSync = true
	c.now = func() time.Time { return now.Add(-72 * time.Hour) }
	st := agent.NewState("old", nil)
	st.Phase = agent.PhaseDone
	if err := c.Save(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	c.Close()
	for name, body := range map[string]string{
		"notes.json":  "not json",
		"my run.json": "{}",
		"r2.json":     `{"Version":2,"RunID":"r2"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body),
			0o600); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Prune(dir, 24*time.Hour, now)
	if n != 1 {
		t.Errorf("Prune pruned %d, want 1 (err %v)", n, err)
	}
	if err == nil {
		t.Error("Prune didn't report the files it couldn't read")
	}
}
