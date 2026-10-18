package safety

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

var testKey = []byte("a key the agent can't read")

// scripted is an Approver that answers from a function of the request,
// as tests and evals use.
func scripted(f func(agent.ApprovalRequest) (agent.ApprovalDecision,
	error)) agent.Approver {
	return agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return f(r)
	})
}

// with returns a context whose run has a as its approver.
func with(ctx context.Context, a agent.Approver) context.Context {
	return agent.WithApprover(ctx, a)
}

func approveAll(r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	return agent.ApprovalDecision{Approved: true, Hash: r.Hash, By: "test"}, nil
}

func denyAll(r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	return agent.ApprovalDecision{Hash: r.Hash, By: "test", Reason: "not this one"}, nil
}

func patch(file, text string) agent.ApprovalRequest {
	return agent.ApprovalRequest{Run: "r1",
		Call: agent.ToolCall{ID: "c1", Name: "code_patch",
			Arguments: jsontext.Value(`{"path":"` + file + `","new":"` + text + `"}`)},
		Why: "protected: auth/", Targets: []string{file}}
}

func TestGate(t *testing.T) {
	dir := workspace(t)
	var log bytes.Buffer
	g := &Gate{Dir: dir, Audit: &Audit{W: &log, Key: testKey}}
	yes, no := with(t.Context(), scripted(approveAll)),
		with(t.Context(), scripted(denyAll))
	if err := g.Check(yes, patch("auth/auth.go", "x")); err != nil {
		t.Fatalf("approved call: %v", err)
	}

	err := g.Check(no, patch("auth/auth.go", "y"))
	if !errors.Is(err, agent.ErrDenied) || !strings.Contains(err.Error(), "not this one") {
		t.Fatalf("denied call: %v", err)
	}

	// The denial sticks to the file: another tool, another spelling of
	// the path, and an approver that would say yes can't change it.
	for _, path := range []string{"auth/auth.go", "./auth/auth.go",
		"auth//auth.go", "AUTH/auth.go", "store/../auth/auth.go"} {
		other := patch(path, "z")
		other.Call.Name = "file_write"
		err = g.Check(yes, other)
		if !errors.Is(err, agent.ErrDenied) {
			t.Errorf("after a denial, %s: %v", path, err)
		}
	}

	n, err := VerifyAudit(&log, testKey)
	if err != nil || n != 7 { // approved, denied, then five refusals
		t.Errorf("audit: %d entries, %v; want 7, nil", n, err)
	}
}

// TestGateHash checks that an approval counts only for the change it
// names and what the person was shown.
func TestGateHash(t *testing.T) {
	dir := workspace(t)
	r := patch("auth/auth.go", "x")
	h1, err := Hash(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	same := r
	same.Call.Arguments = jsontext.Value(`{ "new":"x", "path":"auth/auth.go" }`)
	if h, _ := Hash(dir, same); h != h1 {
		t.Error("the same arguments in another order hash differently")
	}
	shown := r
	shown.Evidence = "a different diff"
	if h, _ := Hash(dir, shown); h == h1 {
		t.Error("the hash ignores the evidence the person saw")
	}
	big1, big2 := r, r
	big1.Call.Arguments = jsontext.Value(`{"n":9007199254740993}`)
	big2.Call.Arguments = jsontext.Value(`{"n":9007199254740992}`)
	if a, _ := Hash(dir, big1); a == func() string { b, _ := Hash(dir, big2); return b }() {
		t.Error("two integers above 2^53 hash the same")
	}
	write(t, dir, "auth/auth.go", "changed")
	if h, _ := Hash(dir, r); h == h1 {
		t.Error("the hash ignores the target's content")
	}
	if _, err := Hash(dir, patch("../outside", "x")); err == nil {
		t.Error("Hash read a target outside the workspace")
	}
	err = mkfifo(filepath.Join(dir, "store", "fifo"))
	switch {
	case errors.Is(err, errors.ErrUnsupported): // no FIFOs here
	case err != nil:
		t.Fatal(err)
	default:
		if _, err := Hash(dir, patch("store/fifo", "x")); err != nil {
			t.Errorf("a FIFO target: %v", err) // must not hang or fail
		}
	}
}

// TestGateStale checks that a target changed while the person decided,
// or a decision about a different change, asks again instead of
// approving or denying, and is audited as stale.
func TestGateStale(t *testing.T) {
	dir := workspace(t)
	var log bytes.Buffer
	changes := scripted(func(r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		write(t, dir, "auth/auth.go", "changed during the wait")
		return agent.ApprovalDecision{Approved: true, Hash: r.Hash, By: "test"}, nil
	})
	g := &Gate{Dir: dir, Audit: &Audit{W: &log, Key: testKey}}
	var p *agent.ApprovalPending
	if err := g.Check(with(t.Context(), changes), patch("auth/auth.go", "x")); !errors.As(err, &p) {
		t.Errorf("changed during the wait: %v, want pending", err)
	}
	old := scripted(func(agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return agent.ApprovalDecision{Approved: true, Hash: "an old hash", By: "test"}, nil
	})
	if err := g.Check(with(t.Context(), old), patch("auth/auth.go", "x")); !errors.As(err, &p) {
		t.Errorf("a decision for another change: %v, want pending", err)
	}
	// Not stuck: a stale decision isn't a denial.
	if err := g.Check(with(t.Context(), scripted(approveAll)), patch("auth/auth.go", "x")); err != nil {
		t.Errorf("after stale decisions: %v", err)
	}
	if !strings.Contains(log.String(), `"outcome":"stale"`) {
		t.Errorf("no stale entry in the audit log:\n%s", log.String())
	}
}

// TestGateWait checks the three ways a decision doesn't arrive: no
// answer in time (a denial), the run cancelled (not a denial), and an
// approver that will answer later (a pause).
func TestGateWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := workspace(t)
		g := &Gate{Dir: dir, Wait: time.Minute}
		err := g.Check(with(t.Context(), silent{}), patch("auth/auth.go", "x"))
		if !errors.Is(err, agent.ErrDenied) || !strings.Contains(err.Error(), "in time") {
			t.Errorf("no answer: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		go func() { time.Sleep(time.Second); cancel() }()
		err = (&Gate{Dir: dir}).Check(with(ctx, silent{}), patch("store/store.go", "x"))
		if errors.Is(err, agent.ErrDenied) || !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled: %v", err)
		}
	})
	later := scripted(func(agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return agent.ApprovalDecision{}, agent.ErrNoDecision
	})
	for name, ctx := range map[string]context.Context{
		"later": with(t.Context(), later), "no approver": t.Context(),
	} {
		err := (&Gate{Dir: workspace(t)}).Check(ctx,
			patch("auth/auth.go", "x"))
		var p *agent.ApprovalPending
		if !errors.As(err, &p) || len(p.Requests) != 1 || p.Requests[0].Hash == "" {
			t.Errorf("%s: %v, want pending", name, err)
		}
	}
}

// silent is an approver whose person never answers; like a real one,
// it gives up when its context ends.
type silent struct{}

func (silent) Decide(ctx context.Context,
	_ agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	<-ctx.Done()
	return agent.ApprovalDecision{}, fmt.Errorf("decide: %w", ctx.Err())
}

// TestAudit checks that the chain catches an edited or removed line,
// can't be rebuilt without the key, and survives a restart.
func TestAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, f, err := OpenAudit(path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	record := func(a *Audit, i int, approved bool) {
		r := patch("auth/auth.go", "x")
		r.Hash = strings.Repeat("ab", i+1)
		d := agent.ApprovalDecision{Approved: approved, Hash: r.Hash, By: "reviewer"}
		if err := a.Record(r, d, outcome(d)); err != nil {
			t.Fatal(err)
		}
	}
	record(a, 0, true)
	record(a, 1, false)
	f.Close()
	a, f, err = OpenAudit(path, testKey) // a new process
	if err != nil {
		t.Fatal(err)
	}
	record(a, 2, true)
	f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := VerifyAudit(bytes.NewReader(data), testKey); err != nil || n != 3 {
		t.Fatalf("after a restart: %d entries, %v", n, err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	edited := strings.Replace(string(data), `"outcome":"denied"`, `"outcome":"approved"`, 1)
	var forged bytes.Buffer
	fa := &Audit{W: &forged, Key: []byte("a key the forger chose")}
	record(fa, 0, true)
	for name, text := range map[string]string{
		"edited":  edited,
		"removed": lines[0] + lines[2],
		"forged":  forged.String(),
	} {
		if _, err := VerifyAudit(strings.NewReader(text), testKey); !errors.Is(err, ErrAuditBroken) {
			t.Errorf("%s: %v, want ErrAuditBroken", name, err)
		}
	}
}

func TestPolicy(t *testing.T) {
	dir := workspace(t)
	p := DefaultPolicy(remindProtected)
	cases := []struct {
		call, target, want string
	}{
		{"issue_file", "", "sends text to the tracker, where people read it"},
		{"tracker__issue_comment", "", "sends text to the tracker, where people read it"},
		{"code_patch", "store/store.go", ""},
		{"code_patch", "auth/auth.go", "auth/auth.go: protected: auth/"},
		{"code_read", "", ""},
	}
	for _, c := range cases {
		var targets []string
		if c.target != "" {
			targets = []string{c.target}
		}
		got, err := p.Needs(dir, agent.ToolCall{Name: c.call}, targets)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q, %v; want %q", c.call, c.target, got, err, c.want)
		}
	}
	p.Off = true
	if got, _ := p.Needs(dir, agent.ToolCall{Name: "issue_file"}, nil); got != "" {
		t.Errorf("approval off: %q", got)
	}
}
