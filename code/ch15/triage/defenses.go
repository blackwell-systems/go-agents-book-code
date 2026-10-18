package triage

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/safety"
)

// tag::defenses[]

// Protected is what the agent may not change in remind without a
// person: the authorization code, the file that applies it to the
// routes, the files that decide what the build pulls in, and git's.
// It names files, not behavior: a new file in package api can still
// change what the routes do.
var Protected = safety.Protected{Paths: []string{"auth/", "api/api.go",
	"go.mod", "go.sum", "go.work", "go.work.sum", "vendor/", ".git/"}}

// Outgoing names the tools whose text leaves the run for people to
// read: the issue tools, built in or through the tracker's MCP server.
var Outgoing = []string{"issue_file", "issue_comment",
	"tracker__issue_file", "tracker__issue_comment"}

// Untrusted names the tools whose output carries text from outside the
// harness: crash reports, issues, anything test code printed, and what
// the agent or a sub-agent wrote after reading them. The code tools
// are left out: the workspace's own code is the evidence.
var Untrusted = []string{"crash_list", "crash_read", "crash_search",
	"crash_logs", "crash_goroutines", "crash_similar", "issue_search",
	"issue_read", "tracker__issue_search", "tracker__issue_read",
	"run_tests", "code_patch", "submit_diagnosis", "notes_read",
	"explain_package"}

// end::defenses[]

// tag::defend[]

// Defend returns the middleware for the defenses that live in the
// tools, for a run in the workspace dir: what the issue tools target,
// the policy that says which calls need a person, and, unless m is nil,
// the marker around untrusted output. The egress check and the gate go
// outside everything else, in the program's wrappers.
func Defend(dir string, p safety.Policy, m *safety.Marker) []agent.ToolWare {
	p.Inspect = shadowing
	ws := []agent.ToolWare{issueTargets, p.Apply(dir)}
	if m != nil {
		ws = append(ws, m.Untrusted(Untrusted...))
	}
	return ws
}

// end::defend[]

// tag::issue-targets[]

// issueTargets gives the issue tools a target, the fingerprint group or the
// issue, so a denial sticks to it: a filing a person refused can't come
// back reworded.
func issueTargets(t agent.Tool) agent.Tool {
	if !slices.Contains(Outgoing, t.Def.Name) || t.Targets != nil {
		return t
	}
	t.Targets = func(args jsontext.Value) ([]string, error) {
		var a struct {
			Fingerprint string `json:"fingerprint"`
			ID          int    `json:"id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("%s: %w", t.Def.Name, err)
		}
		if a.Fingerprint != "" {
			return []string{"issue:" + a.Fingerprint}, nil
		}
		return []string{fmt.Sprintf("issue:#%d", a.ID)}, nil
	}
	return t
}

// end::issue-targets[]

// shadowing is the policy's look at what code_patch writes: a test
// file that could stop the package's other tests from running needs a
// person.
func shadowing(c agent.ToolCall) string {
	var a patchArgs
	if c.Name != "code_patch" || json.Unmarshal(c.Arguments, &a) != nil ||
		!strings.HasSuffix(strings.ToLower(a.Path), "_test.go") {
		return ""
	}
	return safety.Shadows(a.New)
}

// tag::egress-policy[]

// NewEgress returns the policy for issue text: no links at all, since
// an issue about a crash needs none, and none of the harness's own
// secrets, in any encoding, or the names they go by.
func NewEgress(secrets ...string) safety.Egress {
	names := []string{"OPENROUTER", "ANTHROPIC_API_KEY"}
	e := safety.Egress{Words: names}
	for _, s := range secrets {
		if s != "" { // an empty secret would match every text
			e.Secrets = append(e.Secrets, s)
		}
	}
	return e
}

// end::egress-policy[]

// tag::evidence[]

// Evidence returns what a person sees besides a call's arguments: for
// an issue, the raw report it is filed for, so the person judges the
// issue against what the reporter sent, not against the model's
// summary of it.
func Evidence(e *Env) func(agent.ToolCall) string {
	return func(c agent.ToolCall) string {
		var a struct {
			Fingerprint string `json:"fingerprint"`
		}
		if json.Unmarshal(c.Arguments, &a) != nil || a.Fingerprint == "" {
			return ""
		}
		group, err := e.groupOf(a.Fingerprint)
		if err != nil || len(group) == 0 {
			return "no crash group " + a.Fingerprint
		}
		r := group[0]
		var b strings.Builder
		fmt.Fprintf(&b, "report %s, one of %d in the group\n", r.ID,
			len(group))
		fmt.Fprintf(&b, "panic: %s\n", r.Panic)
		writeRequest(&b, r.Request)
		return Redact(b.String()) // the person needs no tokens
	}
}

// end::evidence[]
