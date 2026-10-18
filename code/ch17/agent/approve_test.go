package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"testing"
)

// gated is a tool that waits for a decision until the run has an
// approver, as the approval gate does, counting its runs.
func gated(runs *int) Tool {
	return Tool{Def: ToolDef{Name: "issue_file"},
		Approval: func(ToolCall, []string) (string, error) { return "files", nil },
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			if ApproverFrom(ctx) == nil {
				return "", &ApprovalPending{}
			}
			*runs++
			return "filed", nil
		}}
}

// TestResumeCountsEarlierTurns checks that a resumed run's limits count
// the turns of the run before the pause, passed in Limits.Before.
func TestResumeCountsEarlierTurns(t *testing.T) {
	runs := 0
	m := &Fake{Script: []Message{
		Calls(Call("c1", "issue_file", `{}`)),
		Calls(Call("c2", "issue_file", `{}`)),
		Says("done"),
	}}
	start := []Message{{Role: "user", Content: "go"}}
	res, err := Run(t.Context(), m, []Tool{gated(&runs)}, start,
		Limits{MaxTurns: 2})
	if _, ok := errors.AsType[*ApprovalPending](err); !ok || res.Turns != 1 {
		t.Fatalf("first run: %d turns, %v", res.Turns, err)
	}
	yes := ApproverFunc(func(context.Context, ApprovalRequest) (
		ApprovalDecision, error) {
		return ApprovalDecision{Approved: true}, nil
	})
	res, err = Run(t.Context(), m, []Tool{gated(&runs)}, res.Messages,
		Limits{MaxTurns: 2, Approver: yes, Before: &res})
	if !errors.Is(err, ErrMaxTurns) || res.Turns != 2 {
		t.Errorf("resumed: %d turns in all, %v; want 2, ErrMaxTurns",
			res.Turns, err)
	}
}

// TestResumeAfterNewMessage checks that a paused conversation with a
// message added after the waiting calls is refused, not sent.
func TestResumeAfterNewMessage(t *testing.T) {
	runs := 0
	msgs := []Message{{Role: "user", Content: "go"},
		Calls(Call("c1", "issue_file", `{}`)),
		{Role: "user", Content: "approved; check the logs too"}}
	_, err := Run(t.Context(), &Fake{Script: []Message{Says("done")}},
		[]Tool{gated(&runs)}, msgs, Limits{MaxTurns: 2})
	if !errors.Is(err, ErrBadResume) || errors.Is(err, ErrBadReply) {
		t.Errorf("%v, want ErrBadResume, outside ErrBadReply", err)
	}
}

// TestNestedPending checks that a pending error from inside a tool that
// isn't gated, such as a sub-agent's, is an error result, and that the
// other call of the same stage isn't run again.
func TestNestedPending(t *testing.T) {
	reads := 0
	read := Tool{Def: ToolDef{Name: "code_read"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			reads++
			return "package auth", nil
		}}
	helper := Tool{Def: ToolDef{Name: "explain_package"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "", fmt.Errorf("the helper: %w", &ApprovalPending{})
		}}
	m := &Fake{Script: []Message{
		Calls(Call("c1", "code_read", `{}`), Call("c2", "explain_package", `{}`)),
		Says("done"),
	}}
	res, err := Run(t.Context(), m, []Tool{read, helper},
		[]Message{{Role: "user", Content: "go"}}, Limits{MaxTurns: 3})
	if err != nil || reads != 1 || len(res.Pending) != 0 {
		t.Fatalf("%v; reads %d, pending %v", err, reads, res.Pending)
	}
	if a := res.Messages[3]; !a.IsError || a.ToolCallID != "c2" {
		t.Errorf("the helper's answer: %+v", a)
	}
}
