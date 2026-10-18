package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// memCheckpoint keeps every saved State in memory, as a file would keep
// the last one.
type memCheckpoint struct {
	mu    sync.Mutex
	saves []State
}

func (c *memCheckpoint) Save(_ context.Context, st State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st.Messages = slices.Clone(st.Messages)
	st.Attempts = slices.Clone(st.Attempts)
	st.Pending = slices.Clone(st.Pending)
	c.saves = append(c.saves, st)
	return nil
}

func (c *memCheckpoint) last(t *testing.T) State {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.saves) == 0 {
		t.Fatal("nothing saved")
	}
	return c.saves[len(c.saves)-1]
}

// counted is a scripted model that counts its calls and charges a cent
// for each.
type counted struct {
	Fake
	calls int
}

func (m *counted) Generate(ctx context.Context, req Request) (Reply,
	error) {
	m.calls++
	r, err := m.Fake.Generate(ctx, req)
	r.Usage = Usage{PromptTokens: 100, Cost: 0.01}
	return r, err
}

func newCounted(script []Message) *counted {
	m := &counted{}
	m.Script = script
	return m
}

func call(id, name, args string) ToolCall {
	return ToolCall{ID: id, Name: name, Arguments: jsontext.Value(args)}
}

// fileTool is a side-effecting tool that counts its runs and the keys
// it was given.
type fileTool struct {
	mu   sync.Mutex
	runs int
	keys []string
	err  error // returned by every run, if set
}

func (f *fileTool) tool(idempotent bool) Tool {
	return Tool{Def: ToolDef{Name: "issue_file"}, Idempotent: idempotent,
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.runs++
			k, _ := KeyFrom(ctx)
			f.keys = append(f.keys, k)
			if f.err != nil {
				return "", f.err
			}
			return fmt.Sprintf("filed #%d", f.runs), nil
		}}
}

var fileScript = []Message{
	Calls(call("c1", "issue_file", `{"title":"Panic"}`)),
	Says("Filed it."),
}

func TestResumeDone(t *testing.T) {
	m := newCounted(fileScript)
	f := &fileTool{}
	cp := &memCheckpoint{}
	lim := Limits{MaxTurns: 5, Checkpoint: cp}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	res, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		st, lim)
	if err != nil || res.Turns != 2 {
		t.Fatalf("first run: %d turns, %v", res.Turns, err)
	}
	saved := cp.last(t)
	if saved.Phase != PhaseDone || len(saved.Attempts) != 0 {
		t.Errorf("saved phase %s, %d markers", saved.Phase,
			len(saved.Attempts))
	}
	again, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		saved, lim)
	if err != nil || m.calls != 2 || f.runs != 1 {
		t.Errorf("resuming a done run: %d model calls, %d tool runs, %v",
			m.calls, f.runs, err)
	}
	if again.Turns != 2 || again.Usage.Cost != res.Usage.Cost {
		t.Errorf("resumed done run: %d turns, $%.2f", again.Turns,
			again.Usage.Cost)
	}
	if want := Key("run-1", "c1"); len(f.keys) != 1 || f.keys[0] != want {
		t.Errorf("keys %q, want [%s]", f.keys, want)
	}
}

// TestResumeCounts checks that a resumed run's turns and usage count
// once: from the state, and never again from Limits.Before.
func TestResumeCounts(t *testing.T) {
	script := []Message{
		Calls(call("c1", "read_file", `{}`)),
		Calls(call("c2", "read_file", `{}`)),
		Calls(call("c3", "read_file", `{}`)),
		Says("done"),
	}
	read := Tool{Def: ToolDef{Name: "read_file"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "ok", nil
		}}
	m := newCounted(script)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(m), []Tool{read}, st,
		Limits{MaxTurns: 2, Checkpoint: cp})
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("first run: %v, want ErrMaxTurns", err)
	}
	saved := cp.last(t)
	if saved.Turns != 2 || saved.Usage.Cost != 0.02 {
		t.Fatalf("saved %d turns, $%.2f", saved.Turns, saved.Usage.Cost)
	}
	// Three turns in all: the saved two leave one.
	_, err = Resume(t.Context(), Streamed(m), []Tool{read}, saved,
		Limits{MaxTurns: 3, Checkpoint: cp})
	if !errors.Is(err, ErrMaxTurns) || m.calls != 3 {
		t.Errorf("resumed with 3 turns: %d calls, %v", m.calls, err)
	}
	res, err := Resume(t.Context(), Streamed(m), []Tool{read},
		cp.last(t), Limits{MaxTurns: 10, Checkpoint: cp})
	if err != nil || res.Turns != m.calls || res.Turns != 4 {
		t.Errorf("finished: %d turns for %d calls, %v", res.Turns,
			m.calls, err)
	}
	if res.Usage.Cost < 0.0399 || res.Usage.Cost > 0.0401 {
		t.Errorf("usage $%.4f, want $0.04", res.Usage.Cost)
	}
	before := res
	_, err = Resume(t.Context(), Streamed(m), []Tool{read}, saved,
		Limits{MaxTurns: 10, Before: &before})
	if !errors.Is(err, ErrBeforeAndState) {
		t.Errorf("Before with a State: %v", err)
	}
}

// TestOutcomeUnknownSaved checks that a call whose outcome is unknown
// is saved unanswered, with its marker, and that the resumed run asks
// a person instead of running it again.
func TestOutcomeUnknownSaved(t *testing.T) {
	f := &fileTool{err: fmt.Errorf("%w: issue_file timed out",
		ErrOutcomeUnknown)}
	m := newCounted(fileScript)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	res, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		st, Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("first run: %v", err)
	}
	if n := len(res.Messages); res.Messages[n-1].ToolCallID != "c1" {
		t.Errorf("Result lacks the outcome-unknown message")
	}
	saved := cp.last(t)
	if calls := saved.Unanswered(); len(calls) != 1 ||
		calls[0].ID != "c1" {
		t.Fatalf("saved unanswered calls %v", calls)
	}
	if _, ok := saved.Attempt("c1"); !ok || saved.Phase != PhaseTools {
		t.Fatalf("saved phase %s, marker %v", saved.Phase, ok)
	}

	// No approver: the run waits, with the call pending.
	f.err = nil
	res, err = Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		saved, Limits{MaxTurns: 5, Checkpoint: cp})
	p, ok := errors.AsType[*ApprovalPending](err)
	if !ok || p.Requests[0].Kind != KindUnknownOutcome ||
		len(res.Pending) != 1 || f.runs != 1 {
		t.Fatalf("no approver: %v, pending %v, %d runs", err,
			res.Pending, f.runs)
	}
	if cp.last(t).Phase != PhasePaused {
		t.Errorf("phase %s, want paused", cp.last(t).Phase)
	}

	// A person checks the tracker and says it was filed.
	var asked []ApprovalRequest
	person := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		asked = append(asked, r)
		return ApprovalDecision{Hash: r.Hash, By: "on-call",
			Reason: "filed as #1"}, nil
	})
	res, err = Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		cp.last(t), Limits{MaxTurns: 5, Checkpoint: cp, Approver: person})
	if err != nil || f.runs != 1 || len(asked) != 1 {
		t.Fatalf("with a person: %v, %d runs, %d asked", err, f.runs,
			len(asked))
	}
	if got := toolText(res.Messages, "c1"); got !=
		"not run again: on-call checked and said: filed as #1" {
		t.Errorf("answer %q", got)
	}
	if final := cp.last(t); final.Phase != PhaseDone ||
		len(final.Attempts) != 0 {
		t.Errorf("final phase %s, %d markers", final.Phase,
			len(final.Attempts))
	}
}

// TestIdempotentRetry checks the first rung: a call to an idempotent
// tool, interrupted, runs again with the same key, and nobody is asked.
func TestIdempotentRetry(t *testing.T) {
	f := &fileTool{err: fmt.Errorf("%w: lost", ErrOutcomeUnknown)}
	m := newCounted(fileScript)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	if _, err := Resume(t.Context(), Streamed(m),
		[]Tool{f.tool(true)}, st,
		Limits{MaxTurns: 5, Checkpoint: cp}); !errors.Is(err,
		ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	f.err = nil
	_, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(true)},
		cp.last(t), Limits{MaxTurns: 5, Checkpoint: cp})
	if err != nil || f.runs != 2 || f.keys[0] != f.keys[1] {
		t.Errorf("retry: %v, %d runs, keys %q", err, f.runs, f.keys)
	}
}

// TestPauseDropsMarker checks that a call that stopped at the approval
// gate, before it ran, is not taken for an unknown outcome on resume.
func TestPauseDropsMarker(t *testing.T) {
	f := &fileTool{}
	gated := f.tool(false)
	gated.Approval = func(ToolCall, []string) (string, error) {
		return "files an issue", nil
	}
	run := gated.Run
	gated.Run = func(ctx context.Context, args jsontext.Value) (string,
		error) {
		a := ApproverFrom(ctx)
		if a == nil {
			return "", &ApprovalPending{Requests: []ApprovalRequest{
				{Call: ToolCall{ID: "c1"}}}}
		}
		return run(ctx, args)
	}
	m := newCounted(fileScript)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(m), []Tool{gated}, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if _, ok := errors.AsType[*ApprovalPending](err); !ok {
		t.Fatalf("first run: %v", err)
	}
	saved := cp.last(t)
	if len(saved.Attempts) != 0 || saved.Phase != PhasePaused {
		t.Fatalf("paused: %d markers, phase %s", len(saved.Attempts),
			saved.Phase)
	}
	yes := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		if r.Kind != KindApproval {
			t.Errorf("asked about kind %d", r.Kind)
		}
		return ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	_, err = Resume(t.Context(), Streamed(m), []Tool{gated}, saved,
		Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err != nil || f.runs != 1 {
		t.Errorf("resumed: %v, %d runs", err, f.runs)
	}
}

// TestRunHasNoKey checks that Run, which has no run ID, gives tools no
// idempotency key, as before chapter 15.
func TestRunHasNoKey(t *testing.T) {
	f := &fileTool{}
	m := &Fake{Script: fileScript}
	_, err := Run(t.Context(), m, []Tool{f.tool(false)},
		[]Message{{Role: "user", Content: "go"}}, Limits{MaxTurns: 5})
	if err != nil || len(f.keys) != 1 || f.keys[0] != "" {
		t.Errorf("Run: %v, keys %q", err, f.keys)
	}
}

func toolText(msgs []Message, id string) string {
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == id {
			return m.Content
		}
	}
	return ""
}

// TestKeysTurnedOn checks a call made without a key, then resumed by a
// process whose tool sends keys: the other side never saw a key, so the
// call goes to a person rather than being retried.
func TestKeysTurnedOn(t *testing.T) {
	f := &fileTool{err: fmt.Errorf("%w: lost", ErrOutcomeUnknown)}
	m := newCounted(fileScript)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	if _, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		st, Limits{MaxTurns: 5, Checkpoint: cp}); !errors.Is(err,
		ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	first := cp.last(t)
	if a, _ := first.Attempt("c1"); a.Key != "" {
		t.Fatalf("marker records key %q, which was never sent", a.Key)
	}
	f.err = nil
	keyed := f.tool(false)
	keyed.Keyed = true
	_, err := Resume(t.Context(), Streamed(m), []Tool{keyed}, cp.last(t),
		Limits{MaxTurns: 5, Checkpoint: cp})
	if _, ok := errors.AsType[*ApprovalPending](err); !ok || f.runs != 1 {
		t.Errorf("keys turned on: %v, %d runs; want a question", err,
			f.runs)
	}
}

// TestShutdownReply checks what a shutdown leaves of a reply's calls:
// the call that finished is saved as answered, the one that never
// started stays unanswered, to run after the restart, and no new reply
// is needed for it.
func TestShutdownReply(t *testing.T) {
	script := []Message{
		Calls(call("c1", "issue_file", `{"title":"A"}`),
			call("c2", "issue_file", `{"title":"B"}`)),
		Says("Filed both."),
	}
	ctx, stop := context.WithCancelCause(t.Context())
	f := &fileTool{}
	tool := f.tool(false)
	run := tool.Run
	tool.Run = func(c context.Context, args jsontext.Value) (string,
		error) {
		out, err := run(c, args)
		stop(ErrShutdown) // the signal arrives during the first call
		return out, err
	}
	m := newCounted(script)
	cp := &memCheckpoint{}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(ctx, Streamed(m), []Tool{tool}, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if !errors.Is(err, ErrShutdown) {
		t.Fatalf("first run: %v, want ErrShutdown", err)
	}
	saved := cp.last(t)
	if calls := saved.Unanswered(); len(calls) != 1 ||
		calls[0].ID != "c2" || len(saved.Attempts) != 0 {
		t.Fatalf("saved: unanswered %v, markers %v", calls,
			saved.Attempts)
	}
	res, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)},
		saved, Limits{MaxTurns: 5, Checkpoint: cp})
	if err != nil || f.runs != 2 || m.calls != 2 {
		t.Errorf("resumed: %v, %d tool runs, %d model calls", err, f.runs,
			m.calls)
	}
	if toolText(res.Messages, "c2") != "filed #2" {
		t.Errorf("c2's answer %q", toolText(res.Messages, "c2"))
	}
}

// TestStateCoversResult checks that every field of Result is saved and
// restored: each is set to a value that isn't zero, the journal saves
// it, and State.Result must give back the same Result, except for the
// fields listed as not saved. A field added to Result later can't be
// dropped from checkpoints unnoticed.
func TestStateCoversResult(t *testing.T) {
	notSaved := map[string]bool{"Duration": true} // per process
	var want Result
	v := reflect.ValueOf(&want).Elem()
	for i := range v.NumField() {
		if !notSaved[v.Type().Field(i).Name] {
			v.Field(i).Set(nonZero(v.Field(i).Type()))
		}
	}
	want.Messages = []Message{{Role: "user", Content: "go"}}
	cp := &memCheckpoint{}
	j := newJournal(cp, NewState("run-1", nil))
	j.res = &want
	if err := j.save(t.Context(), PhaseModel); err != nil {
		t.Fatal(err)
	}
	saved := cp.last(t)
	if got := saved.Result(); !reflect.DeepEqual(got, want) {
		t.Errorf("saved and restored:\n%+v\nwant\n%+v", got, want)
	}
}

// nonZero returns a value of type t that isn't the zero value, with
// every field of a struct and the element of a slice set too.
func nonZero(t reflect.Type) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(0.5)
	case reflect.Uint8:
		v.SetUint('x')
	case reflect.Slice:
		v.Set(reflect.Append(v, nonZero(t.Elem())))
	case reflect.Struct:
		for i := range v.NumField() {
			if t.Field(i).IsExported() {
				v.Field(i).Set(nonZero(t.Field(i).Type))
			}
		}
	}
	return v
}

// failingSaves is a checkpointer whose saves fail from the nth on.
type failingSaves struct {
	n, saves int
}

func (f *failingSaves) Save(context.Context, State) error {
	f.saves++
	if f.saves >= f.n {
		return errors.New("disk full")
	}
	return nil
}

// TestSaveFails checks that a failed save stops the run before the next
// step: here the save after the reply, so its call never runs.
func TestSaveFails(t *testing.T) {
	f := &fileTool{}
	m := newCounted(fileScript)
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	_, err := Resume(t.Context(), Streamed(m), []Tool{f.tool(false)}, st,
		Limits{MaxTurns: 5, Checkpoint: &failingSaves{n: 2}})
	if err == nil || !strings.Contains(err.Error(), "disk full") ||
		f.runs != 0 {
		t.Errorf("failed save: %v, %d tool runs", err, f.runs)
	}
}

// TestUnknownToolMarker checks a marker for a tool this process doesn't
// have, as after a deploy that removed it: the call goes to a person,
// and an approval to run it again answers it as an unknown tool.
func TestUnknownToolMarker(t *testing.T) {
	st := NewState("run-1", []Message{{Role: "user", Content: "go"},
		Calls(call("c1", "old_tool", `{}`))})
	st.Phase = PhaseTools
	st.Attempts = []Attempt{{CallID: "c1", Tool: "old_tool"}}
	m := newCounted([]Message{fileScript[0], Says("ok")})
	cp := &memCheckpoint{}
	_, err := Resume(t.Context(), Streamed(m), nil, st,
		Limits{MaxTurns: 5, Checkpoint: cp})
	if p, ok := errors.AsType[*ApprovalPending](err); !ok ||
		p.Requests[0].Kind != KindUnknownOutcome {
		t.Fatalf("unknown tool's marker: %v", err)
	}
	yes := ApproverFunc(func(_ context.Context,
		r ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	res, err := Resume(t.Context(), Streamed(m), nil, cp.last(t),
		Limits{MaxTurns: 5, Checkpoint: cp, Approver: yes})
	if err != nil || !strings.Contains(toolText(res.Messages, "c1"),
		"no tool named") {
		t.Errorf("approved: %v, answer %q", err,
			toolText(res.Messages, "c1"))
	}
}

// TestResumedTurnEnds checks that a run resumed with calls left to run
// reports the end of that turn before the next model call, as the loop
// does for every turn, so a display numbers the next turn right.
func TestResumedTurnEnds(t *testing.T) {
	read := Tool{Def: ToolDef{Name: "read_file"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "ok", nil
		}}
	st := NewState("run-1", []Message{{Role: "user", Content: "go"}})
	st.Messages = append(st.Messages, Calls(call("c1", "read_file", `{}`)))
	st.Phase, st.Turns = PhaseTools, 1
	m := newCounted([]Message{Calls(call("c1", "read_file", `{}`)),
		Says("done")})
	var kinds []EventKind
	turn := 0
	for ev := range ResumeEvents(t.Context(), Streamed(m), []Tool{read}, st,
		Limits{MaxTurns: 5, Checkpoint: &memCheckpoint{}}) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == TurnEvent {
			turn = ev.Turn
		}
		if ev.Kind == StoppedEvent && ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	want := []EventKind{ToolResultEvent, TurnEvent, TextEvent, StoppedEvent}
	if !slices.Equal(kinds, want) || turn != 1 {
		t.Errorf("events %v, turn %d; want %v, turn 1", kinds, turn, want)
	}
}

// TestResumedTurnUncounted checks a conversation with calls left to
// run, given to Events without Limits.Before: no turn of it is counted,
// so no TurnEvent reports one before the next model call.
func TestResumedTurnUncounted(t *testing.T) {
	read := Tool{Def: ToolDef{Name: "read_file"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "ok", nil
		}}
	msgs := []Message{{Role: "user", Content: "go"},
		Calls(call("c1", "read_file", `{}`))}
	m := newCounted([]Message{Calls(call("c1", "read_file", `{}`)),
		Says("done")})
	var kinds []EventKind
	for ev := range Events(t.Context(), Streamed(m), []Tool{read}, msgs,
		Limits{MaxTurns: 5}) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == StoppedEvent && ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	want := []EventKind{ToolResultEvent, TextEvent, StoppedEvent}
	if !slices.Equal(kinds, want) {
		t.Errorf("events %v, want %v", kinds, want)
	}
}
