package safety

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// TestRecall checks that a denial survives a crash: a new gate, as a
// restarted process has, recalls it from the audit log and refuses the
// same change, by hash and by target, even with an approver that would
// say yes; another run's denials and approvals don't count.
func TestRecall(t *testing.T) {
	dir := workspace(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, f, err := OpenAudit(path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gate{Dir: dir, Audit: a}
	no := with(t.Context(), scripted(denyAll))
	yes := with(t.Context(), scripted(approveAll))
	if err := g.Check(no, patch("auth/auth.go", "y")); !errors.Is(err,
		agent.ErrDenied) {
		t.Fatalf("denied call: %v", err)
	}
	other := patch("store/store.go", "y")
	other.Run = "r2"
	if err := g.Check(no, other); !errors.Is(err, agent.ErrDenied) {
		t.Fatalf("another run's call: %v", err)
	}
	if err := g.Check(yes, patch("api/api.go", "y")); err != nil {
		t.Fatalf("approved call: %v", err)
	}
	if err := f.Close(); err != nil { // the process ends
		t.Fatal(err)
	}

	log, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	restarted := &Gate{Dir: dir}
	n, err := restarted.Recall(log, testKey, "r1")
	if err != nil || n != 1 {
		t.Fatalf("Recall = %d, %v; want 1", n, err)
	}
	for _, r := range []agent.ApprovalRequest{
		patch("auth/auth.go", "y"),     // the same change
		patch("./auth/auth.go", "zzz"), // the same file
	} {
		if err := restarted.Check(yes, r); !errors.Is(err,
			agent.ErrDenied) {
			t.Errorf("after a restart, %s: %v", r.Targets[0], err)
		}
	}
	for _, file := range []string{"store/store.go", "api/api.go"} {
		if err := restarted.Check(yes, patch(file, "y")); err != nil {
			t.Errorf("after a restart, %s: %v", file, err)
		}
	}
	// An edited log is refused, whether the edit breaks the JSON or
	// only the chain.
	if _, err := (&Gate{}).Recall(bytesOf(t, path, true), testKey,
		""); err == nil {
		t.Errorf("Recall of an edited log: %v", err)
	}
}

// bytesOf returns the file at path, with one byte changed if edit.
func bytesOf(t *testing.T, path string, edit bool) *os.File {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if edit {
		i := len(b) / 3
		b[i] ^= 0x01
	}
	edited := filepath.Join(t.TempDir(), "edited.jsonl")
	if err := os.WriteFile(edited, b, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(edited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
