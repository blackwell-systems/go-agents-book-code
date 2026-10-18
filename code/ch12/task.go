package main

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch12/taskrun"
)

// tag::policy[]

// contextPolicy returns the context policy for a -context setting. The
// default compacts once a request would pass Limit tokens, clearing old
// tool results from what it sends but not from what it summarizes, and
// marks cache points on what is left, so a long batch stays well
// inside the window and the repeated start of each request comes from
// the cache.
func contextPolicy(name string, m agent.Model) (agent.ContextPolicy, error) {
	const limit = 60_000 // tokens: far below a 1M window, and cheap
	switch name {
	case "none":
		return nil, nil
	case "cache":
		return agent.CacheMarks{}, nil
	case "clear":
		return agent.Chain(agent.ClearResults{Limit: limit},
			agent.CacheMarks{}), nil
	case "trim":
		return agent.Chain(agent.Trim{Limit: limit}, agent.CacheMarks{}), nil
	case "compact", "default":
		return agent.Chain(agent.Compact{Model: m, Limit: limit,
			Sent: agent.ClearResults{Limit: limit}}, agent.CacheMarks{}), nil
	}
	return nil, fmt.Errorf("no context policy %q; try none, cache, clear, "+
		"trim, or compact", name)
}

// end::policy[]

// tag::report[]

// report prints the agent's answer, the issues it filed, and the
// catalog's grade: the checks chapter 13 builds its evals from.
func report(ctx context.Context, p *printer, j *taskrun.Job,
	final jsontext.Value) error {
	out, err := j.Grade(ctx, final)
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	p.printf("\ndiagnosis:\n")
	for _, d := range out.Diagnoses {
		p.printf("  %s %s: %s, root cause %s %s", d.Fingerprint,
			strings.Join(d.Reports, ","), d.Status, d.RootCause.File,
			d.RootCause.Function)
		switch {
		case d.Issue > 0:
			p.printf(", issue #%d", d.Issue)
		case d.Duplicate > 0:
			p.printf(", duplicate of #%d", d.Duplicate)
		}
		p.printf("\n")
	}
	for _, is := range out.Filed {
		p.printf("filed #%d: %s\n", is.ID, is.Title)
	}
	g := out.Grade
	p.printf("\ngraded against the catalog: %d of %d checks passed\n",
		len(g.Checks)-len(g.Failed()), len(g.Checks))
	for _, c := range g.Failed() {
		p.printf("  failed: %s %s: %s\n", c.Name, c.Group, c.Detail)
	}
	return nil
}

// end::report[]
