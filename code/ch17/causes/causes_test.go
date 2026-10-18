package causes

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// filer is an issue_file that numbers what it files.
type filer struct {
	mu   sync.Mutex
	n    int
	err  error
	gate chan struct{} // if set, each call waits on it
}

func (f *filer) tool() agent.Tool {
	return agent.Tool{Def: agent.ToolDef{Name: "issue_file"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			if f.gate != nil {
				<-f.gate
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.err != nil {
				return "", f.err
			}
			f.n++
			return fmt.Sprintf("filed #%d", 100+f.n), nil
		}}
}

func args(file, fn string) jsontext.Value {
	return jsontext.Value(fmt.Sprintf(
		`{"title":"x","fingerprint":"fp_a","root_cause":{"file":%q,`+
			`"function":%q}}`, file, fn))
}

func open(t *testing.T) *Record {
	t.Helper()
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGuard(t *testing.T) {
	r := open(t)
	f := &filer{}
	a, b := r.Guard("run-a", "")(f.tool()), r.Guard("run-b", "")(f.tool())
	ctx := t.Context()
	if out, err := a.Run(ctx, args("store/store.go", "(*Store).List")); err != nil ||
		out != "filed #101" {
		t.Fatalf("first filing: %q, %v", out, err)
	}
	// Another group, the same cause written another way: refused.
	_, err := b.Run(ctx, args("./store/store.go", "Store.List"))
	if !errors.Is(err, agent.ErrToolArgs) ||
		!strings.Contains(err.Error(), "#101") {
		t.Errorf("second group: %v, want a refusal naming #101", err)
	}
	// A different cause: filed.
	if _, err := b.Run(ctx, args("store/store.go", "Get")); err != nil {
		t.Errorf("another cause: %v", err)
	}
	// No root cause: the record stays out of it.
	if _, err := b.Run(ctx, jsontext.Value(`{"title":"y"}`)); err != nil {
		t.Errorf("no root cause: %v", err)
	}
	// The issue closed: the cause can be filed again.
	r.Open = func(_ context.Context, issue int) (bool, error) {
		return issue != 101, nil
	}
	if out, err := b.Run(ctx, args("store/store.go", "List")); err != nil ||
		!strings.HasPrefix(out, "filed #") {
		t.Errorf("after the issue closed: %q, %v", out, err)
	}
	if e, ok, _ := r.Lookup("store/store.go", "List"); !ok || e.Run != "run-b" {
		t.Errorf("entry %+v", e)
	}
}

// TestGuardAtOnce: two groups reach one cause together; one files.
func TestGuardAtOnce(t *testing.T) {
	r := open(t)
	f := &filer{gate: make(chan struct{})}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, run := range []string{"run-a", "run-b"} {
		wg.Go(func() {
			_, errs[i] = r.Guard(run, "")(f.tool()).Run(t.Context(),
				args("a/a.go", "F"))
		})
	}
	close(f.gate)
	wg.Wait()
	if f.n != 1 || (errs[0] == nil) == (errs[1] == nil) {
		t.Errorf("filed %d, errors %v; want one filing and one refusal",
			f.n, errs)
	}
}

// TestGuardKeeps: a call that pauses for a person keeps the cause; one
// that fails frees it; a stopped run's reservation is taken over.
func TestGuardKeeps(t *testing.T) {
	r := open(t)
	f := &filer{err: &agent.ApprovalPending{}}
	ctx := t.Context()
	if _, err := r.Guard("run-a", "")(f.tool()).Run(ctx, args("a/a.go", "F")); err == nil {
		t.Fatal("want the pause")
	}
	f.err = nil
	if _, err := r.Guard("run-b", "")(f.tool()).Run(ctx, args("a/a.go", "F")); err == nil {
		t.Error("a paused group's cause was taken")
	}
	r.Active = func(run string) bool { return run != "run-a" }
	if _, err := r.Guard("run-b", "")(f.tool()).Run(ctx, args("a/a.go", "F")); err != nil {
		t.Errorf("a stopped run's reservation: %v", err)
	}
	f.err = errors.New("tracker down")
	if _, err := r.Guard("run-c", "")(f.tool()).Run(ctx, args("b/b.go", "G")); err == nil {
		t.Fatal("want the failure")
	}
	if _, ok, _ := r.Lookup("b/b.go", "G"); ok {
		t.Error("a failed call kept its cause")
	}
}

func TestNormalize(t *testing.T) {
	same := [][2]string{
		{"store/store.go", "(*Store).Get"},
		{"./store/store.go", "Store.Get"},
		{"remind/store/store.go", "store.(*Store).Get"},
		{"example.com/remind/store/store.go", "Store.Get.func1"},
	}
	want := Normalize(same[0][0], same[0][1])
	for _, s := range same[1:] {
		if got := Normalize(s[0], s[1]); got != want {
			t.Errorf("Normalize(%q, %q) = %q, want %q", s[0], s[1], got,
				want)
		}
	}
	if Normalize("store/store.go", "(*Cache).Get") == want {
		t.Error("two receivers' Get are one cause")
	}
	if got := Normalize("store/store.go", "store.List"); got !=
		"store/store.go:List" {
		t.Errorf("a package function: %q", got)
	}
}

// TestGuardWaits: a group whose cause another group is filing waits
// (KindWait) instead of finishing; when that filing is denied, it
// files; Busy says when to wake it.
func TestGuardWaits(t *testing.T) {
	r := open(t)
	f := &filer{err: &agent.ApprovalPending{}}
	ctx := t.Context()
	if _, err := r.Guard("run-a", "")(f.tool()).Run(ctx,
		args("a/a.go", "F")); err == nil {
		t.Fatal("want A's pause")
	}
	f.err = nil
	_, err := r.Guard("run-b", "")(f.tool()).Run(ctx, args("a/a.go", "F"))
	p, ok := errors.AsType[*agent.ApprovalPending](err)
	if !ok || p.Requests[0].Kind != agent.KindWait {
		t.Fatalf("B: %v, want a wait", err)
	}
	if !r.Busy(p.Requests[0].Targets[0]) {
		t.Error("the cause isn't busy while A waits")
	}
	// A is denied: its call frees the cause.
	f.err = agent.Denial("no")
	_, _ = r.Guard("run-a", "")(f.tool()).Run(ctx, args("a/a.go", "F"))
	if r.Busy(p.Requests[0].Targets[0]) {
		t.Error("still busy after A was denied")
	}
	f.err = nil
	if out, err := r.Guard("run-b", "")(f.tool()).Run(ctx,
		args("a/a.go", "F")); err != nil || !strings.HasPrefix(out, "filed") {
		t.Errorf("B after A's denial: %q, %v", out, err)
	}
}

// TestGuardByKey: a run that stopped after the tracker took its call
// but before the record heard back did file; the next run is refused,
// not handed the cause.
func TestGuardByKey(t *testing.T) {
	r := open(t)
	f := &filer{gate: make(chan struct{})}
	ctx := agent.WithKey(t.Context(), "key-a")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Guard("run-a", "")(f.tool()).Run(ctx, args("a/a.go", "F"))
	}()
	waitFile(t, r, "a/a.go", "F")
	r.Active = func(run string) bool { return run != "run-a" } // A died
	r.ByKey = func(key string) (int, bool) { return 42, key == "key-a" }
	_, err := r.Guard("run-b", "")(f.tool()).Run(t.Context(),
		args("a/a.go", "F"))
	if err == nil || !strings.Contains(err.Error(), "#42") {
		t.Errorf("B after A died mid-call: %v, want a refusal naming #42", err)
	}
	close(f.gate)
	<-done
}

func waitFile(t *testing.T, r *Record, file, fn string) {
	t.Helper()
	for range 500 {
		if _, ok, _ := r.Lookup(file, fn); ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no reservation")
}

// TestGuardWorkspace: a root cause outside the workspace is refused;
// the same group's earlier filing is named as the group's own.
func TestGuardWorkspace(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a", "a.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := open(t)
	f := &filer{}
	g := r.Guard("run-a", ws)(f.tool())
	if _, err := g.Run(t.Context(), args("../etc/passwd", "F")); err == nil {
		t.Error("a file outside the workspace was accepted")
	}
	if _, err := g.Run(t.Context(), args("remind/a/a.go", "F")); err != nil {
		t.Errorf("a file in the workspace: %v", err)
	}
	r.Active = func(string) bool { return false }
	_, err := r.Guard("run-b", ws)(f.tool()).Run(t.Context(),
		args("a/a.go", "F"))
	if err == nil || !strings.Contains(err.Error(), "this group, by an earlier run") {
		t.Errorf("a later run of the same group: %v", err)
	}
}

// TestGuardMixedCase: a file whose name has capitals is found on a
// file system that tells cases apart; the cause's key is lowercased.
func TestGuardMixedCase(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "Store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "Store", "LRU.go"), nil,
		0o644); err != nil {
		t.Fatal(err)
	}
	r := open(t)
	f := &filer{}
	if _, err := r.Guard("run-a", ws)(f.tool()).Run(t.Context(),
		args("Store/LRU.go", "Get")); err != nil {
		t.Errorf("a mixed-case file: %v", err)
	}
}
