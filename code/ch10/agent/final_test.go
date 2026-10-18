package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

// submitTools is an echo tool plus a final tool that refuses the first
// answer it's given, so a test can show a failed final call going back.
func submitTools(refuse int) []Tool {
	type answer struct {
		Text string `json:"text"`
	}
	submit := NewTool("submit", "Submit the answer.",
		func(context.Context, answer) (string, error) {
			if refuse > 0 {
				refuse--
				return "", errors.New("the reproducer passes on the original code")
			}
			return "submitted", nil
		})
	submit.Final = true
	return append(okTools, submit)
}

func submitReply(id, text string) Reply {
	return Reply{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
		ID: id, Name: "submit",
		Arguments: jsontext.Value(`{"text": "` + text + `"}`)}}},
		FinishReason: "tool_calls"}
}

// TestFinalTool checks that a successful call to the final tool ends
// the run with its arguments as Result.Final, and a failed one goes
// back to the model.
func TestFinalTool(t *testing.T) {
	m := &replies{callReply(0, "c1"), submitReply("c2", "first"),
		submitReply("c3", "second")}
	res, err := Run(t.Context(), m, submitTools(1), task(), Limits{MaxTurns: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Final); got != `{"text": "second"}` {
		t.Errorf("Final = %s", got)
	}
	refused := res.Messages[4]
	if !refused.IsError || !strings.Contains(refused.Content, "reproducer") {
		t.Errorf("refused call's answer = %+v", refused)
	}
}

// TestNudge checks that an answer in prose gets a nudge, and that a
// model that keeps answering in prose stops the run.
func TestNudge(t *testing.T) {
	prose := Reply{Message: Says("I think it's fixed."), FinishReason: "stop"}
	m := &replies{prose, submitReply("c1", "done")}
	res, err := Run(t.Context(), m, submitTools(0), task(), Limits{MaxTurns: 10})
	if err != nil {
		t.Fatal(err)
	}
	if n := res.Messages[2]; n.Role != "user" || !strings.Contains(n.Content, "call submit") {
		t.Errorf("nudge = %+v", n)
	}
	if res.Final == nil {
		t.Error("no final answer after the nudge")
	}

	m = &replies{prose, prose, prose}
	res, err = Run(t.Context(), m, submitTools(0), task(), Limits{MaxTurns: 10})
	if !errors.Is(err, ErrNoFinalAnswer) || !errors.Is(err, ErrBadReply) {
		t.Errorf("err = %v, want ErrNoFinalAnswer", err)
	}
	if len(res.Messages) != 6 { // task, then three answers and two nudges
		t.Errorf("%d messages, want 6", len(res.Messages))
	}

	// Without a final tool, prose is the answer, as in chapter 1.
	m = &replies{prose}
	if _, err := Run(t.Context(), m, okTools, task(), Limits{MaxTurns: 10}); err != nil {
		t.Errorf("err = %v, want an answer", err)
	}
}

// TestToolPanic checks that a tool's panic becomes an error the model
// reads, and the run goes on.
func TestToolPanic(t *testing.T) {
	tools := echoTool(func(context.Context) (string, error) {
		panic("boom")
	})
	m := &replies{callReply(0, "c1"), {Message: Says("done")}}
	res, err := Run(t.Context(), m, tools, task(), Limits{MaxTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Messages[2]
	if !got.IsError || !strings.Contains(got.Content, "echo failed with an internal error") {
		t.Errorf("tool message = %+v", got)
	}
}

// TestFinalEndsReply checks that calls after a successful final call,
// in the same reply, don't run: the answer describes the code as it
// was when the final tool accepted it.
func TestFinalEndsReply(t *testing.T) {
	ran := false
	tools := submitTools(0)
	tools[0].Run = func(context.Context, jsontext.Value) (string, error) {
		ran = true
		return "ok", nil
	}
	reply := submitReply("c1", "done")
	reply.Message.ToolCalls = append(reply.Message.ToolCalls,
		ToolCall{ID: "c2", Name: "echo", Arguments: jsontext.Value(`{}`)})
	res, err := Run(t.Context(), &replies{reply}, tools, task(), Limits{MaxTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("a call after the final tool ran")
	}
	last := res.Messages[len(res.Messages)-1]
	if !last.IsError || !strings.Contains(last.Content, "not run: the run ended") {
		t.Errorf("last message = %+v", last)
	}
	if res.Final == nil {
		t.Error("no final answer")
	}
}
