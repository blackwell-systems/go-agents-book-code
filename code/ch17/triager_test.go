package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
)

// TestAskAttempts: before a group restarts after its workspace changed,
// the calls it had in flight go to a person; their answers go with the
// restart.
func TestAskAttempts(t *testing.T) {
	st := agent.NewState("run-1", nil)
	st.Messages = append(st.Messages, agent.Calls(agent.ToolCall{ID: "c1",
		Name: "issue_comment", Arguments: []byte(`{"id":3}`)}))
	st.Attempts = []agent.Attempt{{CallID: "c1", Tool: "issue_comment",
		Started: time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)}}
	cause := agent.ErrWorkspaceChanged

	err := askAttempts(t.Context(), "run-1", st, nil, cause)
	p, ok := errors.AsType[*agent.ApprovalPending](err)
	if !ok || p.Requests[0].Kind != agent.KindUnknownOutcome ||
		p.Requests[0].Call.Name != "issue_comment" {
		t.Fatalf("with nobody to ask: %v", err)
	}
	done := agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return agent.ApprovalDecision{Hash: r.Hash, By: "ana",
			Reason: "posted at 09:00"}, nil
	})
	err = askAttempts(t.Context(), "run-1", st, done, cause)
	r, ok := errors.AsType[*service.Restart](err)
	if !ok || !errors.Is(err, agent.ErrWorkspaceChanged) ||
		!strings.Contains(r.Note, "took effect, ana said: posted at 09:00") {
		t.Errorf("after the answer: %v", err)
	}
}
