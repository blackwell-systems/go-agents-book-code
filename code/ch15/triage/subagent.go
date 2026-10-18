package triage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// tag::explain[]

type explainArgs struct {
	Package  string `json:"package" desc:"a directory of remind's source, such as schedule"`
	Question string `json:"question" desc:"what to find out, such as how Next computes the next run time"`
}

// ExplainPackage returns a tool that hands one question about one
// package to a sub-agent: the same loop, started fresh, with only the
// read-only code tools and its own limits. The parent's context grows
// by the answer, not by every file the sub-agent read.
func ExplainPackage(m agent.StreamModel, e *Env, lim agent.Limits) agent.Tool {
	return agent.NewTool("explain_package", explainDesc,
		func(ctx context.Context, a explainArgs) (string, error) {
			msgs := []agent.Message{
				{Role: "system", Content: explainPrompt},
				{Role: "user", Content: "Package " + a.Package + ": " + a.Question},
			}
			ctx = agent.WithName(ctx, "explain_package") // its costs roll up
			res, err := agent.RunStream(ctx, m, explainTools(e.view()), msgs, lim)
			if errors.Is(err, agent.ErrBudget) { // <1>
				return "", fmt.Errorf("the helper didn't finish (%v) "+
					"after %d turns; ask a narrower question, or read "+
					"the code yourself", err, turns(res))
			}
			if err != nil {
				return "", fmt.Errorf("the helper failed: %w", err) // <2>
			}
			return short(res.Messages[len(res.Messages)-1].Content, 4000), nil
		})
}

// end::explain[]

// tag::explain-tools[]

// explainTools are the sub-agent's tools: reading code, and nothing
// else. explain_package isn't among them, so a sub-agent can't start
// sub-agents of its own: one level only.
func explainTools(e *Env) []agent.Tool {
	var out []agent.Tool
	for _, t := range Tools(e) {
		if slices.Contains([]string{"code_list", "code_read", "code_search"},
			t.Def.Name) {
			out = append(out, t)
		}
	}
	return out
}

// end::explain-tools[]

// explain_package isn't marked read-only, though it changes nothing,
// so two sub-agents never run at once: that would double the spending
// rate, and a recorded run replays the parent's and the sub-agent's
// calls under one name, in the order they were made.

const explainDesc = "Ask a helper to read one package of remind and " +
	"answer one question about how it works, with file and line " +
	"references. Use it for questions that would take many file reads; " +
	"its answer is short, and your own context stays small."

const explainPrompt = `You answer one question about one package of ` +
	`remind, a Go web service, by reading its code with code_list, ` +
	`code_search, and code_read; paths are relative to the module root, ` +
	`such as schedule/weekly.go. You can't change anything. Answer in ` +
	`under 300 words, and give the file and line for every claim.`

// turns counts the assistant messages in a run.
func turns(res agent.Result) int {
	n := 0
	for _, m := range res.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// view returns an Env on the same workspace with bookkeeping of its
// own, for a sub-agent: the files it reads don't count as read by the
// parent, which must still read a file itself before patching it.
func (e *Env) view() *Env {
	return &Env{Reports: e.Reports, Issues: e.Issues, Dir: e.Dir,
		Original: e.Original, root: e.root, TestSlots: e.TestSlots,
		read: map[string]bool{}, created: map[string]bool{},
		notes: map[string][]string{}}
}
