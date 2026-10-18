package safety

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// TestLineApproverOutcome checks the terminal's answers to a question
// about an unknown outcome: "rerun CODE" runs the call again, "done
// CODE what you found" settles it, and an empty line, a plain yes, or
// a done with nothing found decides nothing, so the run stays paused.
func TestLineApproverOutcome(t *testing.T) {
	in, typed := io.Pipe()
	var out strings.Builder
	a := &LineApprover{In: in, Out: &out}
	defer a.Close()
	r := agent.ApprovalRequest{Kind: agent.KindUnknownOutcome, Run: "r1",
		Call: agent.ToolCall{ID: "c1", Name: "issue_file"},
		Why:  "outcome unknown", Hash: "abc123def456"}
	tests := []struct {
		line     string
		decided  bool
		approved bool
		reason   string
	}{
		{"", false, false, ""},
		{"yes abc123", false, false, ""},
		{"done abc123", false, false, ""},
		{"done abc123 filed as #12", true, false, "filed as #12"},
		{"rerun abc123", true, true, ""},
		{"rerun fff000", false, false, ""},
	}
	for _, tt := range tests {
		go func() { _, _ = io.WriteString(typed, tt.line+"\n") }()
		d, err := a.Decide(t.Context(), r)
		decided := err == nil
		if !decided && !errors.Is(err, agent.ErrNoDecision) {
			t.Fatalf("%q: %v", tt.line, err)
		}
		if decided != tt.decided || d.Approved != tt.approved ||
			d.Reason != tt.reason {
			t.Errorf("%q: decided %v, %+v", tt.line, decided, d)
		}
	}
	if !strings.Contains(out.String(), "did issue_file take effect?") {
		t.Errorf("the question isn't asked as one:\n%s", out.String())
	}
}
