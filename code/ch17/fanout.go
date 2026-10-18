package main

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/groups"
)

// tag::run-groups-setup[]

// runGroups investigates the task with one agent per fingerprint group, up
// to c.parallel at once, sharing one model, one spending pool, a pace
// for model calls, and a limit on test runs; then it merges what the
// agents changed, files the issues, and prints the grade.
func runGroups(ctx context.Context, getenv func(string) string,
	w io.Writer, model model, c config) error {
	start := time.Now()
	j, err := setUp(ctx, c.repo, c.task) // the tree the groups merge into
	if err != nil {
		return err
	}
	defer j.Close()
	pace := &agent.Limiter{Every: 200 * time.Millisecond}
	p := &printer{w: w}
	_, direct := providerKey(getenv, c.provider)
	wr := newWrapped(c, hostName(c, direct), modelName(getenv, c, direct),
		p, pace) // the pace is each attempt's Wait (chapter 12)
	sm := wr.Model(model)
	// Summaries are model calls too, so they get the same wrappers.
	policy, err := contextPolicy(cmp.Or(c.context, "default"),
		agent.Generator(sm))
	if err != nil {
		return err
	}
	pool := &agent.SpendPool{Limit: c.maxCost}
	cfg := groups.Config{Repo: c.repo, Parallel: c.parallel, Model: sm,
		Limits: agent.Limits{MaxTurns: cmp.Or(c.maxTurns, 40),
			Context: policy, Spend: pool},
		Tests: agent.NewSlots(2), Tools: wr.Tools}
	// end::run-groups-setup[]
	// tag::run-groups-run[]
	gs := groups.Split(j.Env.Reports)
	p.printf("task: %s, %d crash reports in %d groups, %d at a time\n\n",
		j.Task.ID, len(j.Env.Reports), len(gs), cmp.Or(c.parallel, 6))
	var outs []*groups.Outcome
	for ev := range groups.Events(ctx, cfg, j.Task, gs,
		func(o []*groups.Outcome) { outs = o }) {
		printGroupEvent(p, ev)
	}
	defer groups.Close(outs)
	if ctx.Err() != nil { // stopped: nothing finished to merge
		p.printf("\nstopped before every group finished: %v\n"+
			"spent $%.4f in all\n", context.Cause(ctx), pool.Spent())
		return &stoppedError{context.Cause(ctx)}
	}
	r, err := groups.Finish(ctx, j, outs)
	if err != nil {
		return err //nolint:wrapcheck // Finish's errors say what failed
	}
	printGroups(p, r, pool)
	wr.report(c, w, p)
	if getenv("OPENROUTER_API_KEY") != "" {
		p.printf("took %s\n", time.Since(start).Round(time.Second))
	}
	if p.err != nil {
		return p.err
	}
	if len(r.Unresolved) == len(outs) { // nothing was investigated
		return &stoppedError{errors.New("every group's agent failed")}
	}
	return nil
}

// end::run-groups-run[]

// tag::print-group[]

// printGroupEvent prints one line for each call, result, and stop,
// marked with the group the agent works on.
func printGroupEvent(p *printer, ev agent.Event) {
	switch ev.Kind {
	case agent.CallEvent:
		p.printf("%s  asks  %s %s\n", ev.Agent, ev.Call.Name,
			short(string(ev.Call.Arguments), 60))
	case agent.ToolResultEvent:
		kind := "got  "
		if ev.Tool.IsError {
			kind = "error"
		}
		p.printf("%s  %s %s\n", ev.Agent, kind, summary(ev.Tool.Content))
	case agent.StoppedEvent:
		p.printf("%s  stopped: %s, $%.4f\n", ev.Agent, whyStopped(ev.Err),
			ev.Result.Usage.Cost)
	}
}

// end::print-group[]

// printGroups prints what the harness made of the groups' work.
func printGroups(p *printer, r groups.Report, pool *agent.SpendPool) {
	p.printf("\n%d diagnoses; %d groups unresolved; %d groups' changes left out\n",
		len(r.Diagnoses), len(r.Unresolved), len(r.Conflicts))
	for _, o := range r.Unresolved {
		p.printf("  unresolved %s: %v\n", o.Group.Fingerprint, o.Err)
	}
	for _, fp := range slices.Sorted(maps.Keys(r.Conflicts)) {
		// One line each: a build failure's error holds go test's output.
		why := strings.Join(strings.Fields(r.Conflicts[fp].Error()), " ")
		p.printf("  left out %s: %s\n", fp, short(why, 80))
	}
	for _, is := range r.Outcome.Filed {
		p.printf("filed #%d %s\n", is.ID, is.Title)
	}
	g := r.Outcome.Grade
	p.printf("graded against the catalog: %d of %d checks passed\n",
		len(g.Checks)-len(g.Failed()), len(g.Checks))
	for _, c := range g.Failed() {
		p.printf("  failed: %s %s: %s\n", c.Name, c.Group, short(c.Detail, 80))
	}
	if pool != nil {
		p.printf("spent $%.4f in all\n", pool.Spent())
	}
}

// short cuts s to n bytes.
func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
