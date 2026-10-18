package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
)

// ErrScriptExhausted means a Fake was asked for more replies than its
// script holds.
var ErrScriptExhausted = errors.New("fake model: script exhausted")

// tag::fake[]

// Fake is a Model that replays a script of replies. It picks the next
// reply by counting the assistant messages already in the conversation,
// so it stays in step with the loop.
type Fake struct {
	Script []Message
}

// Generate returns the next scripted reply.
func (f *Fake) Generate(_ context.Context, req Request) (Reply, error) {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	if n >= len(f.Script) {
		return Reply{}, fmt.Errorf("reply %d: %w", n+1, ErrScriptExhausted)
	}
	return Reply{Message: f.Script[n]}, nil
}

// end::fake[]

// tag::helpers[]

// Says is a scripted reply with text and no tool calls: an answer.
func Says(text string) Message {
	return Message{Role: "assistant", Content: text}
}

// Calls is a scripted reply that calls tools.
func Calls(calls ...ToolCall) Message {
	return Message{Role: "assistant", ToolCalls: calls}
}

// Call is one scripted tool call; args is JSON.
func Call(id, name, args string) ToolCall {
	return ToolCall{ID: id, Name: name, Arguments: jsontext.Value(args)}
}

// end::helpers[]
