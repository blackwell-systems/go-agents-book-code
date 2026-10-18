package triage

import (
	"context"
	_ "embed"
	"encoding/json/jsontext"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/history"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// Prompt is the triage agent's system prompt: chapter 7's, plus a
// line on the tools chapter 8 adds. The quality check measures changes
// to it (chapter 8).
//
//go:embed prompt.md
var Prompt string

// GroupPrompt is the system prompt of an agent that investigates one
// fingerprint group and leaves filing to the harness (chapter 9).
//
//go:embed group.md
var GroupPrompt string

// tag::tools[]

// Tools returns the triage agent's tools, working on e. Every tool's
// output and errors pass through Redact before the model sees them.
func Tools(e *Env) []agent.Tool {
	tools := []agent.Tool{
		readOnly(crashList(e)), readOnly(crashRead(e)),
		readOnly(crashSearch(e)), readOnly(crashLogs(e)),
		readOnly(crashGoroutines(e)), readOnly(crashSimilar(e)),
		readOnly(codeList(e)), readOnly(codeRead(e)),
		readOnly(codeSearch(e)),
		idempotent(runTests(e)),                          // <1>
		withLookup(codePatch(e), e.patched),              // <2>
		readOnly(issueSearch(e)), readOnly(issueRead(e)), // issue_read: chapter 8
		issueFile(e), issueComment(e), // <3>
		notesWrite(e), readOnly(notesRead(e)), // chapter 8
		idempotent(submitDiagnosis(e)), // it checks, then ends the run
	}
	if e.History { // chapter 17
		if repo, err := history.OpenChecked(e.Dir, e.Original); err == nil {
			tools = append(tools, history.Tools(repo)...)
		}
	}
	for i, t := range tools {
		if needsWorkspace[t.Def.Name] {
			tools[i] = inWorkspace(e, t)
		}
		tools[i] = redacted(tools[i])
	}
	return tools
}

// end::tools[]

// tag::group-tools[]

// GroupTools returns the tools of an agent that investigates one crash
// group for a harness that files the issues afterwards: Tools, without
// the two that write to the tracker.
func GroupTools(e *Env) []agent.Tool {
	var out []agent.Tool
	for _, t := range Tools(e) {
		if t.Def.Name != "issue_file" && t.Def.Name != "issue_comment" {
			out = append(out, t)
		}
	}
	return out
}

// end::group-tools[]

// needsWorkspace names the tools that read or change the workspace.
var needsWorkspace = map[string]bool{"code_list": true, "code_read": true,
	"code_search": true, "code_patch": true, "run_tests": true,
	"submit_diagnosis": true, "code_history": true, "code_blame": true,
	"test_write": true}

// inWorkspace makes t fail with agent.ErrToolFatal once the workspace
// is gone, instead of reporting a confusing file error the model would
// try to work around (chapter 9).
func inWorkspace(e *Env, t agent.Tool) agent.Tool {
	run := t.Run
	t.Run = func(ctx context.Context, args jsontext.Value) (string, error) {
		if err := e.present(); err != nil {
			return "", err
		}
		return run(ctx, args)
	}
	return t
}

func readOnly(t agent.Tool) agent.Tool {
	t.ReadOnly, t.Idempotent = true, true
	return t
}

func idempotent(t agent.Tool) agent.Tool {
	t.Idempotent = true
	return t
}

// tag::issue-tools[]

// IssueNames are the tools that reach the tracker, which chapter 10
// offers from the tracker's own MCP server instead.
var IssueNames = []string{"issue_search", "issue_read", "issue_file",
	"issue_comment"}

// IssueTools returns the tracker tools, working on issues alone: what
// the tracker's MCP server offers.
func IssueTools(issues *tracker.Store) []agent.Tool {
	var out []agent.Tool
	for _, t := range Tools(&Env{Issues: issues, Keys: true}) {
		if slices.Contains(IssueNames, t.Def.Name) {
			out = append(out, t)
		}
	}
	return out
}

// Without returns tools without the ones named.
func Without(tools []agent.Tool, names ...string) []agent.Tool {
	var out []agent.Tool
	for _, t := range tools {
		if !slices.Contains(names, t.Def.Name) {
			out = append(out, t)
		}
	}
	return out
}

// end::issue-tools[]

// Redacted wraps a tool from elsewhere, such as an MCP server's, so its
// output and errors pass through Redact, as the triage tools' do.
func Redacted(t agent.Tool) agent.Tool { return redacted(t) }
