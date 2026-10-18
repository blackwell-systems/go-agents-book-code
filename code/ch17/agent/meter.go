package agent

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
)

// tag::meter[]

// A Meter records the usage of every model call, attempt by attempt,
// with the agent that made it and what the request added since that
// agent's previous call, so a run's cost can be broken down by agent
// and by turn.
type Meter struct {
	mu    sync.Mutex
	calls []Metered
	last  map[string]prev // each agent's previous call
}

// prev is what the meter remembers of an agent's previous call.
type prev struct {
	tokens int // its prompt tokens
	msgs   int // the messages its request held
}

// A Metered is one attempt of one model call.
type Metered struct {
	Agent       string
	Call        int    // the agent's call number, from 1
	ID          string // the generation's ID, to look a failure up
	Usage       Usage
	Growth      int // prompt tokens more than the agent's previous call
	ResultBytes int // bytes of tool results added since that call
	Failed      bool
}

// end::meter[]

// tag::metered[]

// Wrap returns a ModelWare that records each attempt's usage in m. It
// goes inside the retries, so failed attempts are recorded too.
func (m *Meter) Wrap(next StreamModel) StreamModel {
	return StreamFunc(func(ctx context.Context,
		req Request) iter.Seq2[Delta, error] {
		return func(yield func(Delta, error) bool) {
			var c Metered
			defer func() { m.record(NameFrom(ctx), req, c) }()
			for d, err := range next.Stream(ctx, req) {
				if d.Usage != nil {
					c.Usage = *d.Usage
				}
				c.ID = cmp.Or(d.ID, c.ID)
				c.Failed = err != nil
				if !yield(d, err) {
					return
				}
			}
		}
	})
}

// end::metered[]

// record adds one attempt to the meter, filling in what c, holding the
// attempt's usage, ID, and outcome, leaves out.
func (m *Meter) record(agent string, req Request, c Metered) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		m.last = map[string]prev{}
	}
	c.Agent, c.Call = agent, 1
	for _, prior := range m.calls {
		if prior.Agent == agent {
			c.Call++
		}
	}
	p := m.last[agent]
	results := 0
	for _, msg := range req.Messages[min(p.msgs, len(req.Messages)):] {
		if msg.Role == "tool" {
			results += len(msg.Content)
		}
	}
	c.Growth = max(c.Usage.PromptTokens-p.tokens, 0)
	c.ResultBytes = results
	m.calls = append(m.calls, c)
	m.last[agent] = prev{cmp.Or(c.Usage.PromptTokens, p.tokens),
		len(req.Messages)}
}

// Calls returns a copy of what the meter recorded.
func (m *Meter) Calls() []Metered {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// tag::meter-report[]

// Report writes the cost per agent, with each agent's sub-agents
// rolled up into it, then a line per call.
func (m *Meter) Report(w io.Writer) error {
	calls := m.Calls()
	type total struct {
		Usage
		calls int
	}
	totals := map[string]total{}
	for _, c := range calls {
		top, _, _ := strings.Cut(c.Agent, "/") // <1>
		t := totals[top]
		totals[top] = total{t.Add(c.Usage), t.calls + 1}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "agent\tcalls\tin\tcached\tout\tcost")
	for _, a := range slices.Sorted(maps.Keys(totals)) {
		t := totals[a]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t$%.4f%s\n",
			cmp.Or(a, "run"), t.calls, t.PromptTokens, t.CachedTokens,
			t.CompletionTokens, t.Cost, estimated(t.Usage))
	}
	writeCalls(tw, calls)
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("meter report: %w", err)
	}
	return nil
}

// end::meter-report[]

// writeCalls writes a line per call, under the totals, grouped by
// agent with each sub-agent after its parent.
func writeCalls(w io.Writer, calls []Metered) {
	calls = slices.Clone(calls)
	slices.SortStableFunc(calls, func(a, b Metered) int {
		return cmp.Compare(a.Agent, b.Agent)
	})
	fmt.Fprintln(w, "\nagent\tcall\tin\tgrowth\tcached\tresults added\tcost")
	for _, c := range calls {
		fail := ""
		if c.Failed {
			fail = " (failed " + cmp.Or(c.ID, "with no ID") + ")"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t+%d\t%d\t%d bytes\t$%.4f%s%s\n",
			cmp.Or(c.Agent, "run"), c.Call, c.Usage.PromptTokens, c.Growth,
			c.Usage.CachedTokens, c.ResultBytes, c.Usage.Cost,
			estimated(c.Usage), fail)
	}
}

// estimated marks a cost that came from a price table.
func estimated(u Usage) string {
	if u.CostEstimated {
		return " est."
	}
	return ""
}
