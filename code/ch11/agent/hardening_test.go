package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"
)

// replies is a Model that returns its replies in order.
type replies []Reply

func (r *replies) Generate(context.Context, Request) (Reply, error) {
	if len(*r) == 0 {
		return Reply{}, ErrScriptExhausted
	}
	next := (*r)[0]
	*r = (*r)[1:]
	return next, nil
}

func callReply(cost float64, ids ...string) Reply {
	var calls []ToolCall
	for _, id := range ids {
		calls = append(calls, ToolCall{ID: id, Name: "echo",
			Arguments: jsontext.Value(`{}`)})
	}
	return Reply{Message: Message{Role: "assistant", ToolCalls: calls},
		FinishReason: "tool_calls", Usage: Usage{Cost: cost}}
}

func echoTool(run func(context.Context) (string, error)) []Tool {
	return []Tool{{Def: ToolDef{Name: "echo"},
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			return run(ctx)
		}}}
}

var okTools = echoTool(func(context.Context) (string, error) { return "ok", nil })

func task() []Message { return []Message{{Role: "user", Content: "go"}} }

func TestCheckRejectsBadReplies(t *testing.T) {
	answer := Reply{Message: Message{Role: "assistant", Content: "x"}}
	tests := []struct {
		name  string
		reply Reply
		want  error
	}{
		{"empty ID", callReply(0, ""), ErrBadToolCall},
		{"repeated ID", callReply(0, "a", "b", "a"), ErrBadToolCall},
		{"cut off", Reply{Message: answer.Message, FinishReason: "length"},
			ErrOutputTruncated},
		{"filtered", Reply{FinishReason: "content_filter"}, ErrOutputFiltered},
		{"provider failed", Reply{FinishReason: "error"}, ErrProviderFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.reply.Usage.Cost = 0.25
			m := &replies{tt.reply}
			res, err := Run(t.Context(), m, okTools, task(),
				Limits{MaxTurns: 5})
			if !errors.Is(err, tt.want) || !errors.Is(err, ErrBadReply) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if len(res.Messages) != 1 {
				t.Errorf("the bad reply was appended: %d messages",
					len(res.Messages))
			}
			if res.Usage.Cost != 0.25 {
				t.Errorf("usage = %v, want the bad reply counted", res.Usage)
			}
		})
	}
}

func TestCostBudget(t *testing.T) {
	// Each call costs 0.4: the budget of 1.0 is checked before each
	// turn, so a third call runs (0.8 < 1.0) and the fourth doesn't.
	m := &replies{callReply(0.4, "a"), callReply(0.4, "b"),
		callReply(0.4, "c"), callReply(0.4, "d")}
	res, err := Run(t.Context(), m, okTools, task(),
		Limits{MaxTurns: 10, MaxCost: 1.0})
	if !errors.Is(err, ErrBudgetExceeded) || !errors.Is(err, ErrBudget) {
		t.Errorf("err = %v, want ErrBudgetExceeded", err)
	}
	if got := res.Usage.Cost; got < 1.19 || got > 1.21 {
		t.Errorf("spent %v, want 1.2: over by at most one call", got)
	}

	// A final answer that crosses the budget still succeeds.
	m = &replies{callReply(0.9, "a"),
		{Message: Message{Role: "assistant", Content: "done"},
			Usage: Usage{Cost: 0.9}}}
	res, err = Run(t.Context(), m, okTools, task(),
		Limits{MaxTurns: 10, MaxCost: 1.0})
	if err != nil || res.Usage.Cost < 1.79 {
		t.Errorf("answer over budget: %v, spent %v", err, res.Usage.Cost)
	}
}

func TestMaxTurnsIsABudget(t *testing.T) {
	m := &replies{callReply(0, "a"), callReply(0, "b")}
	_, err := Run(t.Context(), m, okTools, task(), Limits{MaxTurns: 2})
	if !errors.Is(err, ErrMaxTurns) || !errors.Is(err, ErrBudget) {
		t.Errorf("err = %v", err)
	}
}

// tag::cancel-test[]

// TestCancelPartwayThroughAReply cancels the run while the first of
// three tool calls runs. The other two get "cancelled" results, so
// every call is answered, and Run reports both the kind and the cause.
func TestCancelPartwayThroughAReply(t *testing.T) {
	stop := errors.New("user pressed Ctrl-C")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	ran := 0
	tools := echoTool(func(context.Context) (string, error) {
		ran++
		cancel(stop)
		return "done", nil
	})
	m := &replies{callReply(0, "a", "b", "c")}
	res, err := Run(ctx, m, tools, task(), Limits{MaxTurns: 5})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, stop) {
		t.Errorf("err = %v, want context.Canceled and the cause", err)
	}
	if ran != 1 {
		t.Errorf("%d tools ran, want 1", ran)
	}
	msgs := res.Messages
	if len(msgs) != 5 { // task, the calls, three tool messages
		t.Fatalf("%d messages, want 5", len(msgs))
	}
	for i, id := range []string{"a", "b", "c"} {
		m := msgs[2+i]
		if m.Role != "tool" || m.ToolCallID != id {
			t.Errorf("message %d = %+v, want the answer to %s", 2+i, m, id)
		}
	}
	if msgs[2].IsError || !msgs[3].IsError || !msgs[4].IsError {
		t.Errorf("want the first call's result and two cancellations")
	}
}

// end::cancel-test[]

func TestToolErrorsAreTyped(t *testing.T) {
	_, byName := index(Tools(FS{"a.go": "x"}))
	_, err := lookupAndRun(t.Context(), byName,
		ToolCall{Name: "nope", Arguments: jsontext.Value(`{}`)})
	if !errors.Is(err, ErrUnknownTool) || !errors.Is(err, ErrTool) {
		t.Errorf("unknown tool: %v", err)
	}
	_, err = lookupAndRun(t.Context(), byName,
		ToolCall{Name: "read_file", Arguments: jsontext.Value(`{"path": 1}`)})
	if !errors.Is(err, ErrToolArgs) || !errors.Is(err, ErrTool) {
		t.Errorf("bad arguments: %v", err)
	}
}

// TestCancelOnLastTurn cancels during the tools of the last allowed
// turn: the run reports the cancellation, not the turn limit.
func TestCancelOnLastTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tools := echoTool(func(context.Context) (string, error) {
		cancel()
		return "done", nil
	})
	m := &replies{callReply(0, "a")}
	_, err := Run(ctx, m, tools, task(), Limits{MaxTurns: 1})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrMaxTurns) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestIDsUniqueAcrossTurns refuses a reply that reuses an ID from an
// earlier turn, since its result would answer two calls.
func TestIDsUniqueAcrossTurns(t *testing.T) {
	m := &replies{callReply(0, "a"), callReply(0, "a")}
	res, err := Run(t.Context(), m, okTools, task(), Limits{MaxTurns: 5})
	if !errors.Is(err, ErrBadToolCall) {
		t.Errorf("err = %v", err)
	}
	if last := res.Messages[len(res.Messages)-1]; last.Role != "tool" {
		t.Errorf("the conversation should end with turn 1's result: %+v", last)
	}
}

// failing is a Model whose call fails after the provider billed for it.
type failing struct{}

func (failing) Generate(context.Context, Request) (Reply, error) {
	return Reply{Usage: Usage{Cost: 0.3}}, ErrProviderFailed
}

func TestFailedCallStillCounted(t *testing.T) {
	res, err := Run(t.Context(), failing{}, okTools, task(),
		Limits{MaxTurns: 5})
	if !errors.Is(err, ErrProviderFailed) || res.Usage.Cost != 0.3 {
		t.Errorf("err = %v, spent %v", err, res.Usage.Cost)
	}
}
