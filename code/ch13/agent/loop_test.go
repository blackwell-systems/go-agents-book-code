package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

// echo is a tool that returns its "text" argument, or fails when told to.
var echo = Tool{
	Def: ToolDef{Name: "echo", Parameters: jsontext.Value(`{"type": "object"}`)},
	Run: func(_ context.Context, args jsontext.Value) (string, error) {
		if strings.Contains(string(args), "fail") {
			return "", errors.New("echo failed")
		}
		return string(args), nil
	},
}

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		script   []Message
		maxTurns int
		wantErr  error
		wantMsgs int      // messages in the result, including the task
		check    []string // substrings of tool messages, in order
	}{
		{
			name:     "answer on the first turn",
			script:   []Message{Says("done")},
			maxTurns: 5,
			wantMsgs: 2,
		},
		{
			name: "one tool call, then an answer",
			script: []Message{
				Calls(Call("1", "echo", `{"text":"hi"}`)),
				Says("done"),
			},
			maxTurns: 5,
			wantMsgs: 4,
			check:    []string{`"hi"`},
		},
		{
			name: "several tool calls in one reply",
			script: []Message{
				Calls(Call("1", "echo", `{"n":1}`), Call("2", "echo", `{"n":2}`)),
				Says("done"),
			},
			maxTurns: 5,
			wantMsgs: 5,
			check:    []string{`"n":1`, `"n":2`},
		},
		{
			name: "a tool error goes back to the model",
			script: []Message{
				Calls(Call("1", "echo", `{"text":"fail"}`)),
				Says("done"),
			},
			maxTurns: 5,
			wantMsgs: 4,
			check:    []string{"echo failed"},
		},
		{
			name: "an unknown tool lists the real ones",
			script: []Message{
				Calls(Call("1", "eko", `{}`)),
				Says("done"),
			},
			maxTurns: 5,
			wantMsgs: 4,
			check:    []string{`no tool named "eko"; the tools are echo`},
		},
		{
			name: "the turn limit stops a model that never answers",
			script: []Message{
				Calls(Call("1", "echo", `{}`)),
				Calls(Call("2", "echo", `{}`)),
				Calls(Call("3", "echo", `{}`)),
			},
			maxTurns: 2,
			wantErr:  ErrMaxTurns,
			wantMsgs: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []Message{{Role: "user", Content: "task"}}
			res, err := Run(t.Context(), &Fake{Script: tt.script},
				[]Tool{echo}, msgs, Limits{MaxTurns: tt.maxTurns})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, tt.wantErr)
			}
			if len(res.Messages) != tt.wantMsgs {
				t.Fatalf("got %d messages, want %d: %+v",
					len(res.Messages), tt.wantMsgs, res.Messages)
			}
			var tools []Message
			for _, m := range res.Messages {
				if m.Role == "tool" {
					tools = append(tools, m)
				}
			}
			for i, want := range tt.check {
				if i >= len(tools) || !strings.Contains(tools[i].Content, want) {
					t.Errorf("tool message %d doesn't contain %q: %+v", i, want, tools)
				}
			}
		})
	}
}

// TestRunAnswersEveryCall checks that each tool message answers the
// call before it, by ID, and that failures are marked as errors.
func TestRunAnswersEveryCall(t *testing.T) {
	script := []Message{
		Calls(Call("a", "echo", `{}`), Call("b", "nope", `{}`),
			Call("c", "echo", `{"text":"fail"}`)),
		Says("done"),
	}
	res, err := Run(t.Context(), &Fake{Script: script}, []Tool{echo},
		[]Message{{Role: "user", Content: "task"}}, Limits{MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id      string
		isError bool
	}{{"a", false}, {"b", true}, {"c", true}}
	if len(res.Messages) != 6 {
		t.Fatalf("got %d messages, want 6: %+v", len(res.Messages), res.Messages)
	}
	tools := res.Messages[2:5]
	for i, w := range want {
		if tools[i].ToolCallID != w.id || tools[i].IsError != w.isError {
			t.Errorf("tool message %d = %+v, want ID %s, IsError %v",
				i, tools[i], w.id, w.isError)
		}
	}
}

// TestRunModelError checks that Run returns the model's error together
// with the conversation so far.
func TestRunModelError(t *testing.T) {
	res, err := Run(t.Context(), &Fake{}, nil,
		[]Message{{Role: "user", Content: "task"}}, Limits{MaxTurns: 3})
	if !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("Run error = %v, want ErrScriptExhausted", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Content != "task" {
		t.Errorf("Result.Messages = %+v, want the task", res.Messages)
	}
}

// TestRunStopsWhenCancelled checks that a cancelled context stops the
// run before the next turn, with the cancellation in the error.
func TestRunStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := &recorder{}
	cancel()
	res, err := Run(ctx, m, nil, []Message{{Role: "user", Content: "task"}}, Limits{MaxTurns: 3})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run error = %v, want context.Canceled", err)
	}
	if len(m.reqs) != 0 || len(res.Messages) != 1 {
		t.Errorf("model called %d times, %d messages; want 0 and the task", len(m.reqs), len(res.Messages))
	}
}

// recorder is a Model that remembers each request and answers at once.
type recorder struct{ reqs []Request }

func (r *recorder) Generate(_ context.Context, req Request) (Reply, error) {
	r.reqs = append(r.reqs, req)
	return Reply{Message: Says("done")}, nil
}

// TestRunSendsToolsAndConversation checks what the model receives: the
// tool definitions, and the conversation, without the caller's slice
// being changed.
func TestRunSendsToolsAndConversation(t *testing.T) {
	m := &recorder{}
	msgs := make([]Message, 1, 10) // spare capacity Run must not write into
	msgs[0] = Message{Role: "user", Content: "task"}
	res, err := Run(t.Context(), m, []Tool{echo}, msgs, Limits{MaxTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.reqs) != 1 || len(m.reqs[0].Tools) != 1 || m.reqs[0].Tools[0].Name != "echo" {
		t.Errorf("model got %+v, want one request listing the echo tool", m.reqs)
	}
	if len(res.Messages) != 2 || msgs[:2][1].Role != "" {
		t.Errorf("Run wrote into the caller's slice: %+v", msgs[:2])
	}
}

func TestRunPanicsOnDuplicateTools(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Run with two tools of one name didn't panic")
		}
	}()
	_, _ = Run(t.Context(), &Fake{Script: []Message{Says("x")}}, []Tool{echo, echo}, nil, Limits{MaxTurns: 1})
}
