package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Tests for the fold-in review (notes/ch15-review-foldin.md).

// effectTool is a tool that takes effect (counted) and then may fail:
// the failure comes after the effect, as when a server dies after the
// tracker accepted the request.
type effectTool struct {
	effects int
	keys    []string
	fail    func(n int) error // the error after effect n, or nil
}

func (e *effectTool) tool(keyed bool) Tool {
	return Tool{Def: ToolDef{Name: "issue_file"}, Keyed: keyed,
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			k, _ := KeyFrom(ctx)
			e.keys = append(e.keys, k)
			seen := keyed && len(e.keys) > 1 && k == e.keys[0] &&
				e.effects > 0 // the other side recognizes the key
			if !seen {
				e.effects++
			}
			if e.fail != nil {
				if err := e.fail(e.effects); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("filed #%d", e.effects), nil
		}}
}

// TestFatalAfterEffect checks that a call which took effect and then
// failed in a way that stops the run keeps its marker: the resumed run
// settles it on the ladder instead of reading "not run" and filing
// again.
func TestFatalAfterEffect(t *testing.T) {
	for _, keyed := range []bool{true, false} {
		e := &effectTool{}
		e.fail = func(int) error {
			if len(e.keys) == 1 { // the first call: the server dies
				return fmt.Errorf("%w: MCP server gone", ErrToolFatal)
			}
			return nil
		}
		m := newCounted(fileScript)
		cp := &memCheckpoint{}
		st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
		_, err := Resume(t.Context(), Streamed(m), []Tool{e.tool(keyed)},
			st, Limits{MaxTurns: 5, Checkpoint: cp})
		if !errors.Is(err, ErrToolFatal) {
			t.Fatalf("keyed %v: %v", keyed, err)
		}
		saved := cp.last(t)
		if _, ok := saved.Attempt("c1"); !ok ||
			len(saved.Unanswered()) != 1 {
			t.Fatalf("keyed %v: marker %v, unanswered %v", keyed, ok,
				saved.Unanswered())
		}
		person := ApproverFunc(func(_ context.Context,
			r ApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Hash: r.Hash, By: "on-call",
				Reason: "it is #1"}, nil
		})
		_, err = Resume(t.Context(), Streamed(m), []Tool{e.tool(keyed)},
			saved, Limits{MaxTurns: 5, Checkpoint: cp, Approver: person})
		if err != nil || e.effects != 1 {
			t.Errorf("keyed %v: resumed: %v, %d effects", keyed, err,
				e.effects)
		}
	}
}

// netErr is a retryable failure, such as a connection reset.
type netErr struct{}

func (netErr) Error() string   { return "connection reset by peer" }
func (netErr) Retryable() bool { return true }

// TestKeyedGiveUp checks that a keyed call whose retries ran out stops
// the run with an unknown outcome, so the model never files again with
// a new key, and that the ladder repeats it with the same key.
func TestKeyedGiveUp(t *testing.T) {
	e := &effectTool{fail: func(int) error { return netErr{} }}
	retry := RetryTools(Policy{Attempts: 2,
		Backoff: Backoff{Rand: func() float64 { return 0 }}})
	m := newCounted(fileScript)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(m),
		[]Tool{retry(e.tool(true))}, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrOutcomeUnknown) || m.calls != 1 {
		t.Fatalf("gave up: %v after %d model calls", err, m.calls)
	}
	e.fail = nil
	_, err = Resume(t.Context(), Streamed(m),
		[]Tool{retry(e.tool(true))}, cp.last(t),
		Limits{MaxTurns: 5, Checkpoint: cp})
	if err != nil || e.effects != 1 {
		t.Errorf("resumed: %v, %d effects, keys %q", err, e.effects,
			e.keys)
	}
}

// TestCancelledReadNotSaved checks that a read-only call cut off by a
// shutdown is saved unanswered, to run again, rather than as a failure
// the model would read.
func TestCancelledReadNotSaved(t *testing.T) {
	ctx, stop := context.WithCancelCause(t.Context())
	read := Tool{Def: ToolDef{Name: "code_search"}, ReadOnly: true,
		Run: func(c context.Context, _ jsontext.Value) (string, error) {
			stop(ErrShutdown)
			return "", context.Cause(c)
		}}
	script := []Message{Calls(call("c1", "code_search", `{}`)), Says("ok")}
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(ctx, Streamed(newCounted(script)), []Tool{read}, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrShutdown) {
		t.Fatal(err)
	}
	saved := cp.last(t)
	if calls := saved.Unanswered(); len(calls) != 1 {
		t.Errorf("saved unanswered %v, want c1", calls)
	}
}

// TestStoppedCallsNotSaved checks that calls a fatal error kept from
// running are saved unanswered, not as "not run" failures.
func TestStoppedCallsNotSaved(t *testing.T) {
	fatal := Tool{Def: ToolDef{Name: "a"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "", fmt.Errorf("%w: gone", ErrToolFatal)
		}}
	other := Tool{Def: ToolDef{Name: "b"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "ok", nil
		}}
	script := []Message{Calls(call("c1", "a", `{}`), call("c2", "b", `{}`)),
		Says("ok")}
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(newCounted(script)),
		[]Tool{fatal, other}, st, Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrToolFatal) {
		t.Fatal(err)
	}
	saved := cp.last(t)
	if toolText(saved.Messages, "c2") != "" {
		t.Errorf("c2's \"not run\" answer was saved")
	}
}

// gate is a minimal approval gate: it asks the run's approver, then
// marks the call and runs it, as safety.Gate does.
func gate(t Tool) Tool {
	run := t.Run
	t.Approval = func(ToolCall, []string) (string, error) {
		return "files an issue", nil
	}
	t.Run = func(ctx context.Context, args jsontext.Value) (string,
		error) {
		c, _ := CallFrom(ctx)
		r := ApprovalRequest{Call: c, Hash: "h-" + c.ID}
		a := ApproverFrom(ctx)
		if a == nil {
			return "", &ApprovalPending{Requests: []ApprovalRequest{r}}
		}
		d, err := a.Decide(ctx, r)
		if err != nil {
			return "", fmt.Errorf("decide: %w", err)
		}
		if !d.Approved {
			return "", Denial(d.Reason)
		}
		if err := MarkStart(ctx); err != nil {
			return "", err
		}
		return run(ctx, args)
	}
	return t
}

// TestMarkAfterApproval checks that a gated call is marked only after
// the person said yes: a run paused at the gate leaves no marker, and
// the marker is on disk before the call goes out.
func TestMarkAfterApproval(t *testing.T) {
	cp := &memCheckpoint{}
	var markedBefore bool
	inner := Tool{Def: ToolDef{Name: "issue_file"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			last := cp.last(t)
			_, markedBefore = last.Attempt("c1")
			return "filed #1", nil
		}}
	m := newCounted(fileScript)
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(m), []Tool{gate(inner)}, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if _, ok := errors.AsType[*ApprovalPending](err); !ok {
		t.Fatalf("no approver: %v", err)
	}
	for _, s := range cp.saves {
		if len(s.Attempts) > 0 {
			t.Fatalf("a marker was saved while waiting: %+v", s.Attempts)
		}
	}
	yes := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	_, err = Resume(t.Context(), Streamed(m), []Tool{gate(inner)},
		cp.last(t), Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err != nil || !markedBefore {
		t.Errorf("approved: %v, marked before the call: %v", err,
			markedBefore)
	}
}

// TestRetryDenied checks the Retry rung through the gate: a person who
// refuses to run an interrupted call again is asked whether it took
// effect, and the refusal doesn't settle it.
func TestRetryDenied(t *testing.T) {
	e := &effectTool{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"},
		fileScript[0]})
	st.Phase = PhaseTools
	st.Attempts = []Attempt{{CallID: "c1", Tool: "issue_file",
		Key: Key("run-1", "c1")}}
	var kinds []ApprovalKind
	person := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		kinds = append(kinds, r.Kind)
		if r.Kind == KindApproval {
			return ApprovalDecision{Hash: r.Hash, Reason: "not now"}, nil
		}
		return ApprovalDecision{Hash: r.Hash, By: "on-call",
			Reason: "it is #1"}, nil
	})
	cp := &memCheckpoint{}
	res, err := Resume(t.Context(), Streamed(newCounted(fileScript)),
		[]Tool{gate(e.tool(true))}, st,
		Limits{MaxTurns: 5, Checkpoint: cp, Approver: person})
	if err != nil || e.effects != 0 || len(kinds) != 2 ||
		kinds[1] != KindUnknownOutcome {
		t.Fatalf("%v; effects %d; asked %v", err, e.effects, kinds)
	}
	if got := toolText(res.Messages, "c1"); got !=
		"not run again: on-call checked and said: it is #1" {
		t.Errorf("answer %q", got)
	}
}

// TestEmptyOutcomeDenial checks that "it took effect" with no word of
// what was found decides nothing, since the reason becomes the call's
// answer.
func TestEmptyOutcomeDenial(t *testing.T) {
	blank := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Hash: r.Hash}, nil
	})
	_, err := Resolve(t.Context(), Tool{}, "r",
		ToolCall{ID: "c1", Name: "issue_file"}, Attempt{CallID: "c1"},
		blank)
	if _, ok := errors.AsType[*ApprovalPending](err); !ok {
		t.Errorf("blank denial: %v, want the run to wait", err)
	}
}

// TestPendingCleared checks that a run resumed after a pause and then
// finished doesn't report the calls it once waited on.
func TestPendingCleared(t *testing.T) {
	cp := &memCheckpoint{}
	inner := Tool{Def: ToolDef{Name: "issue_file"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "filed #1", nil
		}}
	m := newCounted(fileScript)
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	if _, err := Resume(t.Context(), Streamed(m), []Tool{gate(inner)}, st,
		Limits{MaxTurns: 5, Checkpoint: cp}); err == nil {
		t.Fatal("expected a pause")
	}
	yes := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	res, err := Resume(t.Context(), Streamed(m), []Tool{gate(inner)},
		cp.last(t), Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err != nil || len(res.Pending) != 0 || len(cp.last(t).Pending) != 0 {
		t.Errorf("finished: %v, Pending %v, saved %v", err, res.Pending,
			cp.last(t).Pending)
	}
	again, _ := Resume(t.Context(), Streamed(m), []Tool{gate(inner)},
		cp.last(t), Limits{MaxTurns: 5})
	if len(again.Pending) != 0 {
		t.Errorf("done run reports Pending %v", again.Pending)
	}
}

// TestNestedKey checks that a run started inside a tool, such as a
// sub-agent, doesn't inherit the parent call's key.
func TestNestedKey(t *testing.T) {
	var inner []string
	sub := Tool{Def: ToolDef{Name: "write"},
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			k, _ := KeyFrom(ctx)
			inner = append(inner, k)
			return "ok", nil
		}}
	parent := Tool{Def: ToolDef{Name: "explain"}, ReadOnly: true,
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			_, err := Run(ctx, &Fake{Script: []Message{
				Calls(call("s1", "write", `{}`)), Says("done")}},
				[]Tool{sub}, nil, Limits{MaxTurns: 3})
			return "explained", err
		}}
	script := []Message{Calls(call("c1", "explain", `{}`)), Says("ok")}
	st := NewState("run-1", nil)
	if _, err := Resume(t.Context(), Streamed(newCounted(script)),
		[]Tool{parent}, st, Limits{MaxTurns: 3}); err != nil {
		t.Fatal(err)
	}
	if len(inner) != 1 || inner[0] != "" {
		t.Errorf("the sub-agent's call saw key %q", inner)
	}
}

// TestCheckpointNeedsRunID checks that Run refuses a checkpoint, which
// it couldn't save under a run ID.
func TestCheckpointNeedsRunID(t *testing.T) {
	_, err := Run(t.Context(), &Fake{Script: fileScript}, nil, nil,
		Limits{MaxTurns: 3, Checkpoint: &memCheckpoint{}})
	if !errors.Is(err, ErrNoRunID) {
		t.Errorf("Run with a Checkpoint: %v", err)
	}
}

// flaky is a checkpointer whose nth save fails; the others are kept.
type flaky struct {
	memCheckpoint
	n, saves int
}

func (f *flaky) Save(ctx context.Context, st State) error {
	f.saves++
	if f.saves == f.n {
		return errors.New("disk full")
	}
	return f.memCheckpoint.Save(ctx, st)
}

// TestMarkSaveFails checks a gated call whose marker can't be saved:
// it never went out, so it is answered "not run", keeps no marker, and
// runs after the restart without anyone being asked whether it acted.
func TestMarkSaveFails(t *testing.T) {
	e := &effectTool{}
	// Saves: 1 start, 2 the reply, 3 the marker (fails).
	cp := &flaky{n: 3}
	m := newCounted(fileScript)
	yes := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		if r.Kind != KindApproval {
			t.Errorf("asked about kind %d", r.Kind)
		}
		return ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	res, err := Resume(t.Context(), Streamed(m), []Tool{gate(e.tool(false))},
		st, Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err == nil || e.effects != 0 ||
		!strings.HasPrefix(toolText(res.Messages, "c1"), "not run") {
		t.Fatalf("first run: %v, %d effects, answer %q", err, e.effects,
			toolText(res.Messages, "c1"))
	}
	saved := cp.last(t)
	if len(saved.Attempts) != 0 || len(saved.Unanswered()) != 1 {
		t.Fatalf("saved markers %v, unanswered %v", saved.Attempts,
			saved.Unanswered())
	}
	_, err = Resume(t.Context(), Streamed(m), []Tool{gate(e.tool(false))},
		saved, Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err != nil || e.effects != 1 {
		t.Errorf("resumed: %v, %d effects", err, e.effects)
	}
}

// TestFatalReadNotSaved checks that a read-only call ended by a fatal
// error, such as a dead MCP server, is saved unanswered, to run again.
func TestFatalReadNotSaved(t *testing.T) {
	read := Tool{Def: ToolDef{Name: "issue_search"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "", fmt.Errorf("%w: server gone", ErrToolFatal)
		}}
	script := []Message{Calls(call("c1", "issue_search", `{}`)),
		Says("ok")}
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(newCounted(script)),
		[]Tool{read}, st, Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrToolFatal) {
		t.Fatal(err)
	}
	saved := cp.last(t)
	if calls := saved.Unanswered(); len(calls) != 1 {
		t.Errorf("saved unanswered %v, want c1", calls)
	}
}
