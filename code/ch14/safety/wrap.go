package safety

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// tag::policy-apply[]

// Apply returns middleware that sets Approval, from the policy, on each
// tool that could need a decision: those it names, and those that say
// what they change. dir is the workspace whose protected paths the
// policy checks. A tool Apply leaves alone never waits for a person.
func (p Policy) Apply(dir string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		name := t.Def.Name
		named := slices.Contains(p.Always, name) ||
			slices.Contains(p.Outside, name)
		if p.Off || t.Targets == nil && !named {
			return t
		}
		t.Approval = func(c agent.ToolCall, targets []string) (string,
			error) {
			return p.Needs(dir, c, targets)
		}
		return t
	}
}

// end::policy-apply[]

// tag::untrusted[]

// Untrusted returns middleware that marks the output of the named
// tools as data: what they return comes from outside the harness.
func (m Marker) Untrusted(names ...string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		if !slices.Contains(names, t.Def.Name) {
			return t
		}
		run, name := t.Run, t.Def.Name
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			out, err := run(ctx, args)
			if err != nil {
				return "", err
			}
			return m.Wrap(name, out), nil
		}
		return t
	}
}

// end::untrusted[]

// tag::outgoing[]

// Outgoing returns middleware that checks every string in the
// arguments of the named tools, whose text leaves the run, before they
// run. A finding refuses the call, and the model reads what was found.
func (e Egress) Outgoing(names ...string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		if !slices.Contains(names, t.Def.Name) {
			return t
		}
		run := t.Run
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			if found := e.findings(args); len(found) > 0 {
				return "", fmt.Errorf("not sent: the text holds %s; "+
					"issues carry no links, secrets, or credentials",
					strings.Join(found, ", ")) // <1>
			}
			return run(ctx, args)
		}
		return t
	}
}

// end::outgoing[]

// findings checks each string in args alone, then all of them joined,
// so a key split between the title and the body is found too.
func (e Egress) findings(args jsontext.Value) []string {
	var found, all []string
	for s := range stringsIn(args) {
		all = append(all, s)
	}
	for _, s := range append(all, strings.Join(all, "")) {
		for _, f := range e.Check(s) {
			if !slices.Contains(found, f.Kind+" "+f.Text) {
				found = append(found, f.Kind+" "+f.Text)
			}
		}
	}
	return found
}

// stringsIn yields every string value in a JSON document, object keys
// left out. Invalid JSON yields nothing, and the tool reports it.
func stringsIn(v jsontext.Value) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		d := jsontext.NewDecoder(strings.NewReader(string(v)))
		for {
			tok, err := d.ReadToken()
			if err != nil {
				return
			}
			if tok.Kind() != '"' {
				continue
			}
			if d.StackDepth() > 0 {
				if k, n := d.StackIndex(d.StackDepth()); k == '{' && n%2 == 1 {
					continue // an object's key
				}
			}
			if !yield(tok.String()) {
				return
			}
		}
	}
}
