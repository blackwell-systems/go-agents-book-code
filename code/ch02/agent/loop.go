package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrMaxTurns means the run stopped because it reached Limits.MaxTurns
// without an answer.
var ErrMaxTurns = errors.New("agent: max turns reached")

// tag::run[]

// Run runs the agent loop: it sends the conversation to the model, runs
// any tools the model asks for, and repeats until the model answers
// without calling a tool, the turn limit is reached, or ctx is done. It
// returns the conversation so far even when it returns an error.
func Run(ctx context.Context, m Model, tools []Tool,
	msgs []Message, lim Limits) (Result, error) {
	defs, byName := index(tools) // <1>
	msgs = slices.Clone(msgs)    // <2>
	for range lim.MaxTurns {     // <3>
		if err := ctx.Err(); err != nil { // <4>
			return Result{Messages: msgs}, fmt.Errorf("run: %w", err)
		}
		req := Request{Messages: msgs, Tools: defs}
		reply, err := m.Generate(ctx, req) // <5>
		if err != nil {
			err = fmt.Errorf("generate: %w", err)
			return Result{Messages: msgs}, err
		}
		msgs = append(msgs, reply.Message) // <6>
		if len(reply.Message.ToolCalls) == 0 {
			return Result{Messages: msgs}, nil // <7>
		}
		for _, call := range reply.Message.ToolCalls {
			msgs = append(msgs, runTool(ctx, byName, call)) // <8>
		}
	}
	return Result{Messages: msgs}, ErrMaxTurns
}

// end::run[]

// index splits tools into the definitions the model sees and the tools
// the harness looks up by name. Two tools with one name is a mistake in
// the program, not something to recover from, so it panics.
func index(tools []Tool) ([]ToolDef, map[string]Tool) {
	defs := make([]ToolDef, len(tools))
	byName := make(map[string]Tool, len(tools))
	for i, t := range tools {
		if _, dup := byName[t.Def.Name]; dup {
			panic("agent: two tools named " + t.Def.Name)
		}
		defs[i] = t.Def
		byName[t.Def.Name] = t
	}
	return defs, byName
}

// tag::run-tool[]

// runTool runs one tool call and returns the tool message that answers
// it. Every problem becomes an error message the model can read.
func runTool(ctx context.Context, byName map[string]Tool,
	call ToolCall) Message {
	msg := Message{Role: "tool", ToolCallID: call.ID}
	t, ok := byName[call.Name]
	if !ok {
		names := slices.Sorted(maps.Keys(byName))
		msg.IsError = true
		msg.Content = fmt.Sprintf("no tool named %q; the tools are %s",
			call.Name, strings.Join(names, ", "))
		return msg
	}
	out, err := t.Run(ctx, call.Arguments)
	if err != nil {
		msg.IsError = true
		msg.Content = err.Error()
		return msg
	}
	msg.Content = out
	return msg
}

// end::run-tool[]
