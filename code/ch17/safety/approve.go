package safety

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// tag::gate[]

// A Gate decides whether a call may run. It asks the run's approver,
// Limits.Approver, waits no longer than Wait, remembers every denial
// for the rest of the run, and writes every outcome to the audit log.
// Keep one Gate for a run and its resumptions, so a denial outlives a
// pause.
type Gate struct {
	Wait  time.Duration // 0 means 10 minutes
	Audit *Audit
	Dir   string // the workspace, for hashing targets' contents

	// Evidence, if set, returns what a person needs besides the call's
	// arguments to judge it, such as the report excerpt it came from.
	Evidence func(call agent.ToolCall) string

	mu     sync.Mutex
	denied map[string]string // a hash or a target, to the reason
}

// end::gate[]

// tag::gate-wrap[]

// Wrap is the gate as tool middleware: a call whose tool's Approval
// gives a reason goes through Check before it runs, and what happened
// when it ran goes into the audit log. It belongs outermost (chapter
// 12), so the wait for a person is never retried or timed.
func (g *Gate) Wrap(t agent.Tool) agent.Tool {
	if t.Approval == nil {
		return t
	}
	run := t.Run
	t.Run = func(ctx context.Context, args jsontext.Value) (string,
		error) {
		r, err := g.request(ctx, t, args)
		if err != nil || r.Why == "" { // <1>
			return orRun(ctx, run, args, err)
		}
		if err := g.Check(ctx, r); err != nil {
			return "", err
		}
		if err := agent.MarkStart(ctx); err != nil { // chapter 15
			return "", markFailed(err)
		}
		out, err := run(ctx, args)
		return out, g.ran(r, err) // <2>
	}
	return t
}

// end::gate-wrap[]

// orRun runs a call that needs no decision, or returns err if building
// its request failed.
func orRun(ctx context.Context, run func(context.Context,
	jsontext.Value) (string, error), args jsontext.Value,
	err error) (string, error) {
	if err != nil {
		return "", err
	}
	if err := agent.MarkStart(ctx); err != nil { // chapter 15
		return "", markFailed(err)
	}
	return run(ctx, args)
}

// markFailed is the error for a call whose attempt marker couldn't be
// saved: the call doesn't go out, and the run stops, as it does when
// any save fails (chapter 15).
func markFailed(err error) error {
	return fmt.Errorf("%w: mark the call: %w", agent.ErrToolFatal, err)
}

// ran records what an approved call did, and returns its error. A log
// that can't be written stops the run: the gate can't work without it.
func (g *Gate) ran(r agent.ApprovalRequest, err error) error {
	d := agent.ApprovalDecision{Hash: r.Hash, By: "gate"}
	what := "ran"
	if err != nil {
		what, d.Reason = "failed", err.Error()
	}
	if aerr := g.Audit.Record(r, d, what); aerr != nil {
		return errors.Join(err, unlogged(aerr))
	}
	return err
}

// request builds the request for one call: its targets, and why it
// needs a decision, or no reason if it doesn't. Targets that can't be
// read from the arguments are bad arguments; a call the policy refuses
// outright is a denial. Both are audited.
func (g *Gate) request(ctx context.Context, t agent.Tool,
	args jsontext.Value) (agent.ApprovalRequest, error) {
	call, _ := agent.CallFrom(ctx)
	call.Arguments = args
	r := agent.ApprovalRequest{Run: agent.NameFrom(ctx), Call: call}
	if t.Targets != nil {
		targets, err := t.Targets(args)
		if err != nil {
			return r, g.refuse(r, "bad arguments",
				agent.ArgsError(err.Error()))
		}
		r.Targets = targets
	}
	why, err := t.Approval(call, r.Targets)
	if err != nil {
		return r, g.refuse(r, "refused", denied(err.Error()))
	}
	r.Why = why
	return r, nil
}

// tag::check-call[]

// Check returns nil to run the call, a denial, or an ApprovalPending.
func (g *Gate) Check(ctx context.Context,
	r agent.ApprovalRequest) error {
	r, err := g.prepare(r) // <1>
	if err != nil {
		return g.refuse(r, "refused", err)
	}
	d, err := g.decide(ctx, r)
	if _, ok := errors.AsType[*agent.ApprovalPending](err); ok {
		return g.refuse(r, "pending", err)
	}
	if err != nil {
		return err
	}
	now, err := Hash(g.Dir, r) // <2>
	if err != nil {
		return err
	}
	if d.Hash != r.Hash || now != r.Hash {
		return g.refuse(r, "stale", pending(r))
	}
	if err := g.Audit.Record(r, d, outcome(d)); err != nil {
		return unlogged(err)
	}
	if !d.Approved {
		g.deny(r, d.Reason)
		return denied(d.Reason)
	}
	return nil
}

// end::check-call[]

// denied returns agent.Denial's error, unwrapped: its message is what
// the model reads.
func denied(why string) error {
	return agent.Denial(why) //nolint:wrapcheck // the model reads it as is
}

func pending(r agent.ApprovalRequest) error {
	return &agent.ApprovalPending{Requests: []agent.ApprovalRequest{r}}
}

func outcome(d agent.ApprovalDecision) string {
	if d.Approved {
		return "approved"
	}
	return "denied"
}

// refuse audits a call that didn't reach a decision and returns err.
func (g *Gate) refuse(r agent.ApprovalRequest, what string,
	err error) error {
	d := agent.ApprovalDecision{Hash: r.Hash, By: "gate",
		Reason: err.Error()}
	if aerr := g.Audit.Record(r, d, what); aerr != nil {
		return errors.Join(err, unlogged(aerr))
	}
	return err
}

// unlogged is the error for an audit log that can't be written: the
// gate can't work without it, so the run stops.
func unlogged(err error) error {
	return fmt.Errorf("%w: %w", agent.ErrToolFatal, err)
}

// tag::decide[]

// decide asks the run's approver and waits at most Wait. No answer in
// time is a denial; a cancelled run is not; a run with no approver
// waits for one.
func (g *Gate) decide(ctx context.Context, r agent.ApprovalRequest) (
	agent.ApprovalDecision, error) {
	a := agent.ApproverFrom(ctx)
	if a == nil {
		return agent.ApprovalDecision{}, pending(r)
	}
	limit := orDefault(g.Wait, 10*time.Minute)
	wait, cancel := context.WithTimeoutCause(ctx, limit, errNoAnswer)
	defer cancel()
	d, err := a.Decide(wait, r)
	switch {
	case errors.Is(err, agent.ErrNoDecision):
		return d, pending(r)
	case ctx.Err() != nil:
		return d, fmt.Errorf("approval: %w", context.Cause(ctx)) // <1>
	case errors.Is(context.Cause(wait), errNoAnswer):
		return agent.ApprovalDecision{Hash: r.Hash, By: "timeout",
			Reason: "no decision in time"}, nil
	case err != nil:
		return d, fmt.Errorf("approval: %w", err)
	}
	return d, nil
}

// end::decide[]

var errNoAnswer = errors.New("no decision in time")

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// tag::prepare[]

// prepare checks the targets, hashes the change, and refuses it at once
// if it, or any target, was denied earlier in the run. Targets are
// keyed by their path in lower case, with no ".." and no links, so
// AUTH/auth.go and ./auth/auth.go are the file a denial already covers.
func (g *Gate) prepare(r agent.ApprovalRequest) (
	agent.ApprovalRequest, error) {
	r.Targets = slices.Clone(r.Targets)
	for i, t := range r.Targets {
		if resource(t) {
			continue
		}
		if _, err := direct(g.Dir, t); err != nil {
			return r, denied(err.Error())
		}
		r.Targets[i] = filepath.ToSlash(filepath.Clean(t))
	}
	if g.Evidence != nil { // shown to the person, so hashed
		r.Evidence = g.Evidence(r.Call)
	}
	hash, err := Hash(g.Dir, r)
	if err != nil {
		return r, err
	}
	r.Hash = hash
	if what, why, ok := g.stuck(r); ok {
		return r, denied(fmt.Sprintf(
			"%s was denied earlier in this run: %s", what, why))
	}
	return r, nil
}

// end::prepare[]

// resource reports whether a target names something other than a file,
// such as issue:fp_1234 for a fingerprint group's issue. A file's name can't
// hold a colon, since direct refuses one.
func resource(target string) bool { return strings.Contains(target, ":") }

func key(target string) string { return strings.ToLower(target) }

// stuck reports whether this exact change, or any of its targets, was
// denied earlier in the run, and if so what and why.
func (g *Gate) stuck(r agent.ApprovalRequest) (string, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if why, ok := g.denied[r.Hash]; ok {
		return "the same call", why, true
	}
	for _, t := range r.Targets {
		if why, ok := g.denied[key(t)]; ok {
			return t, why, true
		}
	}
	return "", "", false
}

// deny remembers a denial by the change's hash and by each target, so
// the same change can't come back through another tool or spelling.
func (g *Gate) deny(r agent.ApprovalRequest, why string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.denied == nil {
		g.denied = map[string]string{}
	}
	g.denied[r.Hash] = why
	for _, t := range r.Targets {
		g.denied[key(t)] = why
	}
}

// maxTarget is the largest target file Hash reads.
const maxTarget = 16 << 20

// tag::hash[]

// Hash identifies one exact call and what the person was shown: the
// run, the call's ID and tool, its arguments with object keys sorted
// and spacing removed, the reason and evidence, and the current content
// of each file it targets, read through an os.Root so no target can
// lead outside the workspace. The ID makes a yes count for one call.
func Hash(dir string, r agent.ApprovalRequest) (string, error) {
	args := r.Call.Arguments.Clone()
	err := args.Format(jsontext.ReorderRawObjects(true)) // <1>
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", r.Call.Name, err)
	}
	h := sha256.New()
	for _, part := range []string{r.Run, r.Call.ID, r.Call.Name,
		string(args), r.Why, r.Evidence} {
		fmt.Fprintf(h, "%s\x00", part)
	}
	for _, t := range slices.Sorted(slices.Values(r.Targets)) {
		sum, err := contentHash(dir, t)
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", t, err)
		}
		fmt.Fprintf(h, "%s\x00%s\x00", t, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// end::hash[]

// contentHash hashes one target: a regular file's content, up to
// maxTarget bytes, or the word for what else is there. Only regular
// files are read, so a FIFO can't hang the gate.
func contentHash(dir, name string) (string, error) {
	if resource(name) {
		return "a resource", nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("open the workspace: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "absent", nil // a file the call would create
	case err != nil:
		return "", fmt.Errorf("stat: %w", err)
	case !info.Mode().IsRegular():
		return "not a regular file: " + info.Mode().Type().String(), nil
	case info.Size() > maxTarget:
		return "", fmt.Errorf("over %d bytes", maxTarget)
	}
	f, err := root.Open(name)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxTarget+1)); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
