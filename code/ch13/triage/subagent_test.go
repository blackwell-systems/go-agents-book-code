package triage

import (
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
)

// TestExplainPackage checks that the sub-agent's answer becomes the
// tool's result, and that what it read doesn't count as read by the
// parent.
func TestExplainPackage(t *testing.T) {
	e, _ := newTestEnv(t, "long-weekly-zone")
	sub := agent.Streamed(&agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("s1", "code_read", `{"path": "schedule/weekly.go"}`)),
		agent.Says("Next truncates to midnight in UTC (schedule/weekly.go)."),
	}})
	tool := ExplainPackage(sub, e, agent.Limits{MaxTurns: 5})
	out, err := tool.Run(t.Context(),
		[]byte(`{"package": "schedule", "question": "how is Next computed?"}`))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "midnight in UTC")
	if e.hasRead("schedule/weekly.go") {
		t.Error("the sub-agent's read counted as the parent's")
	}
}

// TestExplainUnfinished checks that a sub-agent out of turns comes back
// as an error result, and the parent's run goes on and answers.
func TestExplainUnfinished(t *testing.T) {
	e, _ := newTestEnv(t, "long-weekly-zone")
	var script []agent.Message
	for i := range 3 {
		script = append(script, agent.Calls(agent.Call(
			"s"+string(rune('0'+i)), "code_list", `{}`)))
	}
	sub := agent.Streamed(&agent.Fake{Script: script})
	explain := ExplainPackage(sub, e, agent.Limits{MaxTurns: 3})
	parent := &agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("p1", "explain_package",
			`{"package": "schedule", "question": "everything"}`)),
		agent.Says("I'll read it myself."),
	}}
	res, err := agent.Run(t.Context(), parent, []agent.Tool{explain},
		[]agent.Message{{Role: "user", Content: "triage"}},
		agent.Limits{MaxTurns: 5})
	if err != nil {
		t.Fatalf("the parent stopped: %v", err)
	}
	answer := res.Messages[2]
	if !answer.IsError || !strings.Contains(answer.Content, "didn't finish") ||
		!strings.Contains(answer.Content, "after 3 turns") {
		t.Errorf("explain_package answered %+v", answer)
	}
}
