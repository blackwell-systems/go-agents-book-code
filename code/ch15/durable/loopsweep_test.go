package durable

// The crash sweep again, on the agent's own loop: agent.Resume with a
// file checkpoint, crashed at every point and restarted until done.
// A crash inside a tool, which runs on a goroutine of its own whose
// panics the loop recovers, marks the process dead instead; the next
// save, on the loop's goroutine, panics before it writes anything, and
// nothing in between leaves the process.

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

var loopScript = []agent.Message{
	agent.Calls(
		agent.ToolCall{ID: "c1", Name: "issue_search",
			Arguments: jsontext.Value(`{}`)},
		agent.ToolCall{ID: "c2", Name: "issue_file",
			Arguments: jsontext.Value(`{"title":"Panic in store.Get"}`)}),
	agent.Calls(
		agent.ToolCall{ID: "c3", Name: "issue_file",
			Arguments: jsontext.Value(`{"title":"Deadlock in notify"}`)},
		agent.ToolCall{ID: "c4", Name: "issue_comment",
			Arguments: jsontext.Value(`{"id":1,"body":"Seen again."}`)}),
	agent.Says("Filed #1 and #2; commented on #1."),
}

// A process is one life of the agent: its crasher, and whether a tool
// already "killed" it.
type process struct {
	c    *crasher
	dead atomic.Bool
}

// die is a crash point outside the tools: the process stops there.
// It also marks the process dead, since a point reached on a tool's
// goroutine, such as a marker saved after a person's yes, panics into
// the tool's recovery, and the loop's next save must still stop.
func (p *process) die(name string) {
	p.c.n++
	if p.c.n == p.c.at {
		p.c.where = name
		p.dead.Store(true)
		panic(crash{name})
	}
}

func (p *process) kill(name string) {
	p.c.n++
	if p.c.n == p.c.at {
		p.c.where = name
		p.dead.Store(true)
	}
}

// dying is a checkpointer that names its save points for the crasher
// and refuses to write once the process is dead.
type dying struct {
	cp   *Checkpoint
	p    *process
	prev agent.State
}

func (d *dying) Save(ctx context.Context, st agent.State) error {
	if d.p.dead.Load() {
		panic(crash{d.p.c.where})
	}
	if err := d.cp.Save(ctx, st); err != nil {
		return err
	}
	switch {
	case len(st.Attempts) > len(d.prev.Attempts):
		d.p.die("marker saved")
	case len(st.Messages) > len(d.prev.Messages) &&
		st.Messages[len(st.Messages)-1].Role == "tool":
		d.p.die("result saved")
	}
	d.prev = st
	return nil
}

// dyingModel names the moment a reply has arrived and isn't saved.
type dyingModel struct {
	m agent.StreamModel
	p *process
}

func (d dyingModel) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		for x, err := range d.m.Stream(ctx, req) {
			if !yield(x, err) {
				return
			}
		}
		d.p.die("reply received")
	}
}

type loopSim struct {
	keys   bool
	mode   string // "", failAfter, or gated
	dir    string
	issues *tracker.Store
	calls  atomic.Int32
	asks   int
	failed map[string]bool // calls that failed after their effect
}

// The sweep's modes, beyond the plain one: a tool that fails after the
// tracker accepted its request, such as an MCP server that dies, once
// per call; and tools behind an approval gate, where a crash can land
// while a person decides.
const (
	failAfter = "fail after the effect"
	gated     = "behind a gate"
)

func (s *loopSim) tools(p *process) []agent.Tool {
	file := func(ctx context.Context, args jsontext.Value) (string,
		error) {
		var a struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("issue_file: %w", err)
		}
		key := ""
		if s.keys {
			key, _ = agent.KeyFrom(ctx)
		}
		is, _, err := s.issues.CreateOnce(key, a.Title, "", nil, "")
		if err != nil {
			return "", fmt.Errorf("issue_file: %w", err)
		}
		p.kill("tracker accepted")
		if err := s.failOnce(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("filed #%d", is.ID), nil
	}
	comment := func(ctx context.Context, args jsontext.Value) (string,
		error) {
		var a struct {
			ID   int    `json:"id"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("issue_comment: %w", err)
		}
		key := ""
		if s.keys {
			key, _ = agent.KeyFrom(ctx)
		}
		if _, _, err := s.issues.CommentOnce(a.ID, key,
			a.Body); err != nil {
			return "", fmt.Errorf("issue_comment: %w", err)
		}
		p.kill("tracker accepted")
		if err := s.failOnce(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("commented on #%d", a.ID), nil
	}
	search := func(context.Context, jsontext.Value) (string, error) {
		return fmt.Sprintf("%d open issues", len(s.issues.Search(
			tracker.Query{Status: "open"}))), nil
	}
	returned := func(t agent.Tool) agent.Tool {
		run := t.Run
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			out, err := run(ctx, args)
			if !t.ReadOnly {
				p.kill("tool returned")
			}
			return out, err
		}
		return t
	}
	return []agent.Tool{
		returned(agent.Tool{Def: agent.ToolDef{Name: "issue_search"},
			ReadOnly: true, Run: search}),
		s.gate(p, returned(agent.Tool{Def: agent.ToolDef{Name: "issue_file"},
			Keyed: s.keys, Run: file})),
		s.gate(p, returned(agent.Tool{
			Def: agent.ToolDef{Name: "issue_comment"}, Keyed: s.keys,
			Run: comment})),
	}
}

// failOnce, in the failAfter mode, fails a call the first time, after
// its effect, as a dead MCP server does.
func (s *loopSim) failOnce(ctx context.Context) error {
	c, _ := agent.CallFrom(ctx)
	if s.mode != failAfter || s.failed[c.ID] {
		return nil
	}
	s.failed[c.ID] = true
	return fmt.Errorf("%w: MCP server tracker: server gone",
		agent.ErrToolFatal)
}

// gate, in the gated mode, puts a tool behind an approval gate: the
// run's approver decides, a crash can land while it does, and the
// call is marked only after a yes, as safety.Gate does.
func (s *loopSim) gate(p *process, t agent.Tool) agent.Tool {
	if s.mode != gated {
		return t
	}
	run := t.Run
	t.Approval = func(agent.ToolCall, []string) (string, error) {
		return "changes the tracker", nil
	}
	t.Run = func(ctx context.Context, args jsontext.Value) (string,
		error) {
		c, _ := agent.CallFrom(ctx)
		r := agent.ApprovalRequest{Call: c, Hash: "h-" + c.ID}
		p.kill("waiting for a person")
		if p.dead.Load() { // the process died while the person decided
			return "", errors.New("gone")
		}
		d, err := agent.ApproverFrom(ctx).Decide(ctx, r)
		if err != nil || !d.Approved {
			return "", fmt.Errorf("not approved: %w", err)
		}
		if err := agent.MarkStart(ctx); err != nil {
			return "", fmt.Errorf("mark: %w", err)
		}
		return run(ctx, args)
	}
	return t
}

func (s *loopSim) person() agent.Approver {
	ts := &sim{issues: s.issues}
	return agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		if r.Kind == agent.KindApproval { // the gate's question: yes
			return agent.ApprovalDecision{Approved: true, Hash: r.Hash},
				nil
		}
		s.asks++
		return agent.ApprovalDecision{Approved: !ts.tookEffect(r.Call),
				Hash: r.Hash, By: "on-call", Reason: "it is in the tracker"},
			nil
	})
}

// life runs the agent once, from its checkpoint if it has one.
func (s *loopSim) life(ctx context.Context, p *process) (crashed bool,
	err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	cp, err := Open(s.dir, runID)
	if err != nil {
		return false, err
	}
	defer cp.Close()
	cp.noSync, cp.crash = true, p.die
	st, err := cp.Restore(ctx, maxResumes)
	if errors.Is(err, ErrNoCheckpoint) {
		st, err = agent.NewState(runID, []agent.Message{
			{Role: "user", Content: "Triage the new crash reports."}}), nil
	}
	if err != nil {
		return false, err //nolint:wrapcheck // a test reports it as is
	}
	model := &counting{m: agent.Streamed(&agent.Fake{Script: loopScript}),
		n: &s.calls}
	_, err = agent.Resume(ctx, dyingModel{model, p}, s.tools(p), st,
		agent.Limits{MaxTurns: 10, Approver: s.person(),
			Checkpoint: &dying{cp: cp, p: p, prev: st}})
	return false, err //nolint:wrapcheck // a test reports it as is
}

type counting struct {
	m agent.StreamModel
	n *atomic.Int32
}

func (c *counting) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	c.n.Add(1)
	return c.m.Stream(ctx, req)
}

// loopScenario crashes the first life at point first and the second at
// point second, then restarts until the run is done.
func loopScenario(t *testing.T, keys bool, mode, dir string, first,
	second int) (outcome, int) {
	t.Helper()
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &loopSim{keys: keys, mode: mode, dir: dir, issues: issues,
		failed: map[string]bool{}}
	var o outcome
	points := 0
	for life := range maxResumes {
		p := &process{c: &crasher{}}
		switch life {
		case 0:
			p.c.at = first
		case 1:
			p.c.at = second
		}
		crashed, err := s.life(t.Context(), p)
		if life == 0 {
			points = p.c.n
		}
		if errors.Is(err, agent.ErrToolFatal) && mode == failAfter {
			continue // the run stopped; the next start resumes it
		}
		if err != nil {
			t.Fatalf("keys %v, %s, crashes at %d and %d: life %d: %v",
				keys, mode, first, second, life, err)
		}
		if !crashed {
			break
		}
		if life == 0 {
			o.where = p.c.where
		}
		o.again = o.again || life == 1
	}
	o.asks, o.extraCalls = s.asks, int(s.calls.Load())-len(loopScript)
	o.duplicates, o.missing = count(s.issues)
	o.messages = finalMessages(t, dir)
	return o, points
}

// finalMessages is the conversation a finished run saved.
func finalMessages(t *testing.T, dir string) []agent.Message {
	t.Helper()
	cp, err := Open(dir, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	st, err := cp.Load()
	if err != nil || st.Phase != agent.PhaseDone {
		t.Fatalf("final state: %s, %v", st.Phase, err)
	}
	return st.Messages
}

// TestLoopCrashSweep is TestCrashSweep on agent.Resume, for the two
// ways the chapter's agent runs: with a tracker that honors keys, and
// with one that doesn't. Neither may duplicate or lose an effect; with
// keys, nobody is asked.
func TestLoopCrashSweep(t *testing.T) {
	base := t.TempDir()
	for _, mode := range []string{"", failAfter, gated} {
		for _, keys := range []bool{true, false} {
			sweepLoop(t, base, mode, keys)
		}
	}
}

// sweepLoop sweeps one mode, with or without keys.
func sweepLoop(t *testing.T, base, mode string, keys bool) {
	t.Helper()
	name := fmt.Sprintf("keys %v, %q", keys, mode)
	dry, points := loopScenario(t, keys, mode,
		fresh(t, filepath.Join(base, "dry")), 0, 0)
	differ := 0 // scenarios whose conversation isn't the dry run's
	var all outcome
	scenarios := 0
	where := map[string]int{}
	for first := 1; first <= points; first++ {
		for second := 0; ; second++ {
			o, _ := loopScenario(t, keys, mode,
				fresh(t, filepath.Join(base, "run")), first, second)
			if second > 0 && !o.again {
				break
			}
			if second == 0 {
				where[o.where]++
			}
			if !slices.EqualFunc(o.messages, dry.messages, sameMessage) {
				differ++
			}
			scenarios++
			all.duplicates += o.duplicates
			all.missing += o.missing
			all.asks += o.asks
			all.extraCalls += o.extraCalls
		}
	}
	t.Logf("%s: %d points %v, %d scenarios, %d dups, %d missing, "+
		"%d asks, %d repeated model calls, %d conversations unlike the "+
		"uncrashed run's", name, points, where, scenarios,
		all.duplicates, all.missing, all.asks, all.extraCalls, differ)
	switch {
	case all.duplicates > 0 || all.missing > 0:
		t.Errorf("%s: %d duplicates, %d missing", name, all.duplicates,
			all.missing)
	case keys && differ > 0: // with keys, a resumed run is the same run
		t.Errorf("%s: %d conversations differ", name, differ)
	case keys && all.asks > 0:
		t.Errorf("%s: asked a person %d times", name, all.asks)
	case !keys && all.asks == 0:
		t.Errorf("%s: never asked a person", name)
	}
	want := []string{"reply received", "marker saved",
		"tracker accepted", "tool returned", "result saved", "mid-save",
		"after rename"}
	if mode == gated {
		want = append(want, "waiting for a person")
	}
	for _, p := range want {
		if where[p] == 0 {
			t.Errorf("%s: no crash landed at %s", name, p)
		}
	}
}

// sameMessage compares two messages as a conversation would: by role,
// text, calls, and answers.
func sameMessage(a, b agent.Message) bool {
	return a.Role == b.Role && a.Content == b.Content &&
		a.ToolCallID == b.ToolCallID && a.IsError == b.IsError &&
		slices.EqualFunc(a.ToolCalls, b.ToolCalls,
			func(x, y agent.ToolCall) bool {
				return x.ID == y.ID && x.Name == y.Name &&
					string(x.Arguments) == string(y.Arguments)
			})
}
