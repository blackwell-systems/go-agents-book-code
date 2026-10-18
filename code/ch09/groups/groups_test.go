package groups

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch09/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch09/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch09/triage"
)

const repo = "../.." // the book's code directory

// perGroup is a model that answers each group's agent from that group's
// own script, found by the fingerprint in the agent's task message, so
// counting assistant messages still works with several agents at once.
type perGroup map[string]*agent.Fake

func (p perGroup) Generate(ctx context.Context, req agent.Request) (agent.Reply, error) {
	for fp, f := range p {
		if strings.Contains(req.Messages[1].Content, fp) {
			r, err := f.Generate(ctx, req)
			if err != nil {
				return r, fmt.Errorf("%s: %w", fp, err)
			}
			return r, nil
		}
	}
	return agent.Reply{}, agent.ErrScriptExhausted
}

// submit is a script that submits one needs_human diagnosis for g.
func submit(t *testing.T, g Group, fn string) *agent.Fake {
	b, err := json.Marshal(map[string]any{"groups": []triage.Diagnosis{{
		Fingerprint: g.Fingerprint, Reports: g.Reports, Category: "other",
		RootCause: triage.Location{File: "store/store.go", Function: fn},
		Evidence:  "a test script", Status: "needs_human"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &agent.Fake{Script: []agent.Message{agent.Calls(
		agent.Call("1", "submit_diagnosis", string(b)))}}
}

// tag::test-unresolved[]

// TestUnresolvedGroup runs the groups of a mixed batch at once. One
// agent fails at its first call, and is reported, not fatal; another
// loses its connection on its first call, and its group starts over
// and comes through with the rest.
func TestUnresolvedGroup(t *testing.T) {
	ctx := t.Context()
	tasks, err := taskrun.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := taskrun.Find(tasks, "g3-mixed-batch")
	if err != nil {
		t.Fatal(err)
	}
	j, err := taskrun.SetUp(ctx, repo, task)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	gs := Split(j.Env.Reports)
	m := perGroup{}
	for i, g := range gs[1:] {
		m[g.Fingerprint] = submit(t, g, "F"+string(rune('a'+i)))
	}
	m[gs[0].Fingerprint] = &agent.Fake{} // fails at its first call
	drop := &dropOnce{Model: m, group: gs[1].Fingerprint}
	c := Config{Repo: repo, Model: agent.Streamed(drop), Parallel: 4,
		Limits: agent.Limits{MaxTurns: 3}, Tests: agent.NewSlots(1),
		Again: func(err error) bool {
			_, ok := errors.AsType[*net.OpError](err)
			return ok
		}}
	var outs []*Outcome
	agents := map[string]bool{}
	for ev := range Events(ctx, c, task, gs, func(o []*Outcome) { outs = o }) {
		agents[ev.Agent] = true
	}
	defer Close(outs)
	r, err := Finish(ctx, j, outs)
	if err != nil {
		t.Fatal(err)
	}
	if !drop.dropped {
		t.Error("no connection was dropped")
	}
	if len(agents) != len(gs) || len(r.Unresolved) != 1 ||
		len(r.Diagnoses) != len(gs)-1 || len(r.Outcome.Filed) != len(gs)-1 {
		t.Errorf("%d agents, %d unresolved, %d diagnoses, %d filed",
			len(agents), len(r.Unresolved), len(r.Diagnoses), len(r.Outcome.Filed))
	}
}

// end::test-unresolved[]

// dropOnce fails one group's first call with a dropped connection, the
// way a TLS error reading a stream does, and passes every other call on.
type dropOnce struct {
	agent.Model
	group string

	mu      sync.Mutex
	dropped bool
}

func (d *dropOnce) Generate(ctx context.Context, req agent.Request) (agent.Reply, error) {
	d.mu.Lock()
	drop := !d.dropped && strings.Contains(req.Messages[1].Content, d.group)
	d.dropped = d.dropped || drop
	d.mu.Unlock()
	if drop {
		return agent.Reply{}, fmt.Errorf("read stream: %w", &net.OpError{
			Op: "remote error", Err: errors.New("tls: bad record MAC")})
	}
	r, err := d.Model.Generate(ctx, req)
	if err != nil {
		return r, fmt.Errorf("pass on: %w", err)
	}
	return r, nil
}

// TestUnfinishedGroup stops reading a group's run after its first
// event, before the run's stop event: the outcome says the group didn't
// finish, so its partial work is never merged as if it had.
func TestUnfinishedGroup(t *testing.T) {
	ctx := t.Context()
	tasks, err := taskrun.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := taskrun.Find(tasks, "d1-empty-snooze")
	if err != nil {
		t.Fatal(err)
	}
	j, err := taskrun.SetUp(ctx, repo, task)
	if err != nil {
		t.Fatal(err)
	}
	g := Split(j.Env.Reports)[0]
	j.Close()
	c := Config{Repo: repo, Limits: agent.Limits{MaxTurns: 3},
		Model: agent.Streamed(perGroup{g.Fingerprint: submit(t, g, "F")})}
	out := &Outcome{Group: g}
	defer Close([]*Outcome{out})
	runOnce(ctx, c, task, out, func(agent.Event) bool { return false })
	if !errors.Is(out.Err, errUnfinished) {
		t.Fatalf("outcome err = %v, want errUnfinished", out.Err)
	}
}
