package triage

import (
	"context"
	"fmt"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// HistoryLine is the system prompt's line for the history tools, for a
// run that has them (Env.History; chapter 17). It isn't in Prompt, so
// runs without the tools, and their recordings, are unchanged.
const HistoryLine = "When the workspace has git history, code_history " +
	"and code_blame show which commit changed a function or a line; " +
	"give that commit as introduced_by."

// DiagnoseRule is the line a diagnose-only run's instruction gains:
// what it may still do (chapter 17).
const DiagnoseRule = "\n\nIn this run you can't change remind's " +
	"code: write a reproducer test with test_write, file an issue with " +
	"what you found, and give the group the status needs_human."

type testWriteArgs struct {
	Path    string `json:"path" desc:"a new test file, such as store/list_test.go"`
	Content string `json:"content" desc:"the whole file"`
}

// tag::test-write[]

// testWrite creates a new test file and nothing else: the one change a
// diagnose-only run may make, so the agent can write a reproducer but
// has no tool that changes remind's code (chapter 17).
func testWrite(e *Env) agent.Tool {
	t := agent.NewTool("test_write",
		"Create a new test file, such as a reproducer for a crash. It "+
			"can't change an existing file, and the path must end in "+
			"_test.go.",
		func(ctx context.Context, a testWriteArgs) (string, error) {
			p, err := clean(a.Path)
			if err != nil {
				return "", err
			}
			if !strings.HasSuffix(p, "_test.go") {
				return "", fmt.Errorf("%s is not a test file; this run "+
					"can only create files ending in _test.go", p)
			}
			return e.create(ctx, p, a.Content)
		})
	t.Targets = patchTargets // its path, for the gate and protected paths
	return t
}

// end::test-write[]

// tag::diagnose-tools[]

// DiagnoseTools returns Tools for a diagnose-only run: code_patch is
// left out and test_write takes its place, so the run can't change
// remind's code however its prompt or its input reads.
func DiagnoseTools(e *Env) []agent.Tool {
	var out []agent.Tool
	for _, t := range Tools(e) {
		if t.Def.Name == "code_patch" {
			t = redacted(inWorkspace(e, testWrite(e)))
		}
		out = append(out, t)
	}
	return out
}

// end::diagnose-tools[]
