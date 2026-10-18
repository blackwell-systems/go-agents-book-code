package safety

import (
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// tag::policy[]

// A Policy says which calls need a person's decision. Approval is on by
// default for everything that sends text to people or reaches outside
// the workspace, and for writes to protected paths; Off turns it off,
// for a run whose input you trust.
type Policy struct {
	Always    []string // tools that always need a decision
	Outside   []string // tools that reach outside the workspace
	Protected Protected
	Off       bool

	// Inspect, if set, gives a reason a call needs a decision from what
	// it writes, such as a test that could stop others running.
	Inspect func(c agent.ToolCall) string
}

// end::policy[]

// tag::policy-default[]

// DefaultPolicy asks before filing or commenting, through the agent's
// own tools or the tracker's MCP server, and before protected writes.
func DefaultPolicy(p Protected) Policy {
	return Policy{Always: []string{"issue_file", "issue_comment",
		"tracker__issue_file", "tracker__issue_comment"}, Protected: p}
}

// end::policy-default[]

// tag::policy-needs[]

// Needs returns why a call needs a decision, or "" if it doesn't. A
// target that can't be judged, such as one reached through a link, is
// an error, and the call doesn't run.
func (p Policy) Needs(dir string, c agent.ToolCall, targets []string) (
	string, error) {
	switch {
	case p.Off:
		return "", nil
	case slices.Contains(p.Always, c.Name):
		return "sends text to the tracker, where people read it", nil
	case slices.Contains(p.Outside, c.Name):
		return "reaches outside the workspace", nil
	}
	for _, t := range targets {
		if resource(t) {
			continue // not a file, such as issue:fp_1234
		}
		v, hit, err := p.Protected.Check(dir, t)
		if err != nil {
			return "", err
		}
		if hit {
			return v.Path + ": " + v.Why, nil
		}
	}
	if p.Inspect != nil {
		return p.Inspect(c), nil
	}
	return "", nil
}

// end::policy-needs[]
