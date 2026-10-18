package triage

import (
	_ "embed"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
)

// Prompt is the triage agent's system prompt: chapter 7's, plus a
// line on the tools chapter 8 adds, plus the three sentences chapter
// 8's quality check measured as helping (the version named "all").
//
//go:embed prompt.md
var Prompt string

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
		codePatch(e),                                     // <2>
		readOnly(issueSearch(e)), readOnly(issueRead(e)), // issue_read: chapter 8
		issueFile(e), issueComment(e), // <3>
		notesWrite(e), readOnly(notesRead(e)), // chapter 8
		submitDiagnosis(e),
	}
	for i, t := range tools {
		tools[i] = redacted(t)
	}
	return tools
}

func readOnly(t agent.Tool) agent.Tool {
	t.ReadOnly, t.Idempotent = true, true
	return t
}

func idempotent(t agent.Tool) agent.Tool {
	t.Idempotent = true
	return t
}

// end::tools[]
