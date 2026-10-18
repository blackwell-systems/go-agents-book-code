package durable_test

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
)

// tag::wiring[]

// runSaved runs a conversation under a checkpoint in dir. The first
// start saves a new run; every later start with the same run ID
// resumes it from where the last one stopped.
func runSaved(ctx context.Context, dir, run string, m agent.StreamModel,
	tools []agent.Tool, msgs []agent.Message) (agent.Result, error) {
	cp, err := durable.Open(dir, run) // <1>
	if err != nil {
		return agent.Result{}, fmt.Errorf("run %s: %w", run, err)
	}
	defer cp.Close()
	st, err := cp.Restore(ctx, 10) // <2>
	if errors.Is(err, durable.ErrNoCheckpoint) {
		st = agent.NewState(run, msgs) // <3>
		err = cp.Save(ctx, st)
	}
	if err != nil {
		return agent.Result{}, fmt.Errorf("run %s: %w", run, err)
	}
	lim := agent.Limits{MaxTurns: 20, Checkpoint: cp} // <4>
	res, err := agent.Resume(ctx, m, tools, st, lim)
	if err != nil {
		return res, fmt.Errorf("run %s: %w", run, err)
	}
	return res, nil
}

// end::wiring[]

// down is a model that fails after a number of calls, standing in for a
// process that stops partway through a run.
type down struct {
	m     agent.Model
	calls *int
	after int
}

func (d down) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	if *d.calls == d.after {
		return agent.Reply{}, errors.New("provider down")
	}
	*d.calls++
	r, err := d.m.Generate(ctx, req)
	if err != nil {
		return r, fmt.Errorf("scripted model: %w", err)
	}
	return r, nil
}

// Example_resume starts one run three times: the first start stops
// after one model call, the second resumes it and finishes, and the
// third finds it done.
func Example_resume() {
	dir, err := os.MkdirTemp("", "resume")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	clock := agent.NewTool("clock", "Tell the time.",
		func(context.Context, struct{}) (string, error) {
			return "12:00", nil
		})
	clock.ReadOnly = true
	script := &agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("c1", "clock", `{}`)),
		agent.Says("It is 12:00."),
	}}
	msgs := []agent.Message{{Role: "user", Content: "What time is it?"}}
	for i, after := range []int{1, 99, 99} {
		calls := 0
		m := agent.Streamed(down{m: script, calls: &calls, after: after})
		res, err := runSaved(context.Background(), dir, "run-1", m,
			[]agent.Tool{clock}, msgs)
		last := res.Messages[len(res.Messages)-1]
		fmt.Printf("start %d: %d model calls, last %s message %q, "+
			"error: %v\n", i+1, calls, last.Role, last.Content, err)
	}
	// Output:
	// start 1: 1 model calls, last tool message "12:00", error: run run-1: generate: provider down
	// start 2: 1 model calls, last assistant message "It is 12:00.", error: <nil>
	// start 3: 0 model calls, last assistant message "It is 12:00.", error: <nil>
}
