// Package groups investigates a batch of crash reports with one agent
// per fingerprint group, several at once: the harness, not the model, starts
// the agents, gives each its own copy of the code, merges their changes
// afterwards, and files the issues (chapter 9).
package groups

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch15/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch15/triage"
)

// tag::group[]

// A Group is the reports one agent investigates: those with the same
// fingerprint (chapter 7).
type Group struct {
	Fingerprint string
	Reports     []string // report IDs
}

// Split groups reports by fingerprint, in the order each fingerprint
// first appears.
func Split(reports []triage.Report) []Group {
	var out []Group
	at := map[string]int{}
	for _, r := range reports {
		fp := triage.Fingerprint(r)
		i, ok := at[fp]
		if !ok {
			i, at[fp] = len(out), len(out)
			out = append(out, Group{Fingerprint: fp})
		}
		out[i].Reports = append(out[i].Reports, r.ID)
	}
	return out
}

// end::group[]

// tag::config[]

// A Config is how a batch is investigated.
type Config struct {
	Repo     string            // the book's code directory
	Model    agent.StreamModel // shared by every agent
	Parallel int               // agents at once; 0 means 6
	Limits   agent.Limits      // each agent's; Spend is shared by all
	Tests    agent.Slots       // run_tests calls at once, across agents

	// Tools, if set, wraps each agent's tools, such as in chapter 12's
	// retries, timeouts, and spans.
	Tools func([]agent.Tool) []agent.Tool
}

// An Outcome is what one group's agent did.
type Outcome struct {
	Group     Group
	Diagnoses []triage.Diagnosis
	Usage     agent.Usage
	Err       error // why the agent stopped without an answer, if it did
	job       *taskrun.Job
}

// end::config[]

// tag::events[]

// Events investigates every group of task, Parallel at a time, and
// reports each agent's events as they happen, tagged with the group's
// fingerprint. When every agent has stopped it sends the outcomes, in
// group order, to done; the caller merges and files them (Finish).
func Events(ctx context.Context, c Config, task catalog.Task,
	groups []Group, done func([]*Outcome)) iter.Seq[agent.Event] {
	return func(yield func(agent.Event) bool) {
		slots := agent.NewSlots(cmp.Or(c.Parallel, 6))
		outs := make([]*Outcome, len(groups))
		runs := map[string]func(context.Context) iter.Seq[agent.Event]{}
		for i, g := range groups {
			outs[i] = &Outcome{Group: g}
			runs[g.Fingerprint] = func(ctx context.Context) iter.Seq[agent.Event] {
				return investigate(ctx, c, task, slots, outs[i]) // <1>
			}
		}
		for ev := range agent.Merge(ctx, runs) {
			if !yield(ev) {
				break
			}
		}
		done(outs) // <2>
	}
}

// end::events[]

// tag::investigate[]

// investigate runs one group's agent, once a slot is free, and records
// what it found in out. A dropped connection is the model's retries'
// to handle (chapter 12), so the group's agent runs once.
func investigate(ctx context.Context, c Config, task catalog.Task,
	slots agent.Slots, out *Outcome) iter.Seq[agent.Event] {
	return func(yield func(agent.Event) bool) {
		if err := slots.Acquire(ctx); err != nil { // <1>
			out.Err = err
			yield(agent.Event{Kind: agent.StoppedEvent, Err: err})
			return
		}
		defer slots.Release()
		ctx = replay.WithScript(ctx, out.Group.Fingerprint) // <2>
		runOnce(agent.WithName(ctx, out.Group.Fingerprint), c, task,
			out, yield)
	}
}

// end::investigate[]

// errUnfinished means a group's run ended without its stop event: the
// caller stopped reading before the agent finished.
var errUnfinished = errors.New("the group's agent didn't finish")

// tag::investigate-setup[]

// runOnce runs the group's agent in a new workspace and records what it
// found in out. It reports false if yield did.
func runOnce(ctx context.Context, c Config, task catalog.Task, out *Outcome,
	yield func(agent.Event) bool) bool {
	j, err := taskrun.SetUp(ctx, c.Repo, only(task, out.Group)) // <1>
	if err != nil {
		out.Err = fmt.Errorf("%w: set up: %w", agent.ErrToolFatal, err)
		return yield(agent.Event{Kind: agent.StoppedEvent, Err: out.Err})
	}
	out.job = j
	j.Env.FileLater, j.Env.TestSlots = true, c.Tests // <2>
	// end::investigate-setup[]
	// tag::investigate-run[]
	out.Err = errUnfinished // until the run's stop event says otherwise
	msgs := []agent.Message{
		{Role: "system", Content: triage.GroupPrompt},
		{Role: "user", Content: "Triage the crash reports of group " +
			out.Group.Fingerprint + "."},
	}
	tools := triage.GroupTools(j.Env)
	if c.Tools != nil {
		tools = c.Tools(tools)
	}
	for ev := range agent.Events(ctx, c.Model, tools,
		msgs, c.Limits) {
		if ev.Kind == agent.StoppedEvent {
			out.Usage, out.Err = out.Usage.Add(ev.Result.Usage), ev.Err
			out.Diagnoses, err = diagnoses(ev.Result.Final)
			out.Err = errors.Join(out.Err, err)
		}
		if !yield(ev) {
			return false
		}
	}
	return true
}

// end::investigate-run[]

// only returns task with just the group's crash reports.
func only(task catalog.Task, g Group) catalog.Task {
	t := task
	t.Reports = slices.DeleteFunc(slices.Clone(task.Reports),
		func(r catalog.Report) bool { return !slices.Contains(g.Reports, r.ID) })
	return t
}

// diagnoses reads a submit_diagnosis call's groups.
func diagnoses(final []byte) ([]triage.Diagnosis, error) {
	if final == nil {
		return nil, nil
	}
	var answer struct {
		Groups []triage.Diagnosis `json:"groups"`
	}
	if err := json.Unmarshal(final, &answer); err != nil {
		return nil, fmt.Errorf("read the answer: %w", err)
	}
	return answer.Groups, nil
}

// Close removes every group's workspace.
func Close(outs []*Outcome) {
	for _, o := range outs {
		if o.job != nil {
			o.job.Close()
		}
	}
}
