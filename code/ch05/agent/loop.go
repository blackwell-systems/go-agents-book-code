package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// tag::run[]

// Run runs the agent loop: it sends the conversation to the model, runs
// any tools the model asks for, and repeats until the model answers
// without calling a tool or a limit stops it. It returns the run so far
// even when it returns an error.
func Run(ctx context.Context, m Model, tools []Tool,
	msgs []Message, lim Limits) (Result, error) {
	defs, byName := index(tools)
	res := Result{Messages: slices.Clone(msgs)}
	for range lim.MaxTurns {
		if err := beforeTurn(ctx, res, lim); err != nil { // <1>
			return res, err
		}
		req := Request{Messages: res.Messages, Tools: defs}
		reply, err := m.Generate(ctx, req)
		res.Usage = res.Usage.Add(reply.Usage) // <2>
		if err != nil {
			return res, fmt.Errorf("generate: %w", err)
		}
		if err := check(reply, res.Messages); err != nil { // <3>
			return res, err
		}
		res.Messages = append(res.Messages, reply.Message)
		// end::run[]
		// tag::run-calls[]
		calls := reply.Message.ToolCalls
		if len(calls) == 0 {
			return res, nil
		}
		res.Messages = append(res.Messages,
			runTools(ctx, byName, calls)...) // <1>
	}
	if err := stopped(ctx); err != nil { // <2>
		return res, err
	}
	return res, ErrMaxTurns
}

// end::run-calls[]

// tag::before-turn[]

// beforeTurn checks what can stop a run before it spends another call:
// cancellation, and the cost budget.
func beforeTurn(ctx context.Context, res Result, lim Limits) error {
	if err := stopped(ctx); err != nil {
		return err
	}
	if lim.MaxCost > 0 && res.Usage.Cost >= lim.MaxCost { // <1>
		return fmt.Errorf("%w: spent $%.6f of $%.6f",
			ErrBudgetExceeded, res.Usage.Cost, lim.MaxCost)
	}
	return nil
}

// stopped returns the error for a run whose ctx is done, carrying the
// kind of cancellation and, when it says more, the cause. It returns
// nil while ctx isn't done.
func stopped(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	err := fmt.Errorf("stopped before turn: %w", ctx.Err())
	if cause := context.Cause(ctx); !errors.Is(err, cause) { // <2>
		err = fmt.Errorf("%w: %w", err, cause)
	}
	return err
}

// end::before-turn[]

// tag::check[]

// check decides whether a reply can join the conversation so far. A
// reply that fails is never appended, so the conversation stays one the
// API will accept.
func check(reply Reply, msgs []Message) error {
	switch reply.FinishReason { // <1>
	case "length":
		return ErrOutputTruncated
	case "content_filter":
		return ErrOutputFiltered
	case "error":
		return ErrProviderFailed
	}
	// end::check[]
	// tag::check-ids[]
	seen := map[string]bool{} // IDs must be unique in the conversation
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			seen[c.ID] = true
		}
	}
	for _, c := range reply.Message.ToolCalls { // <1>
		if c.ID == "" {
			return fmt.Errorf("%w: a call to %s has no ID",
				ErrBadToolCall, c.Name)
		}
		if seen[c.ID] {
			return fmt.Errorf("%w: the ID %q is already in use",
				ErrBadToolCall, c.ID)
		}
		seen[c.ID] = true
	}
	return nil
}

// end::check-ids[]

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

// tag::run-tools[]

// runTools answers every call in a reply, in order. Once ctx is done,
// the calls that haven't run get a "cancelled" result instead, so every
// tool call in the conversation still has its answer.
func runTools(ctx context.Context, byName map[string]Tool,
	calls []ToolCall) []Message {
	var out []Message
	for _, call := range calls {
		if ctx.Err() != nil { // <1>
			out = append(out, Message{Role: "tool", ToolCallID: call.ID,
				IsError: true, Content: "cancelled before it ran: " +
					context.Cause(ctx).Error()})
			continue
		}
		out = append(out, runTool(ctx, byName, call))
	}
	return out
}

// end::run-tools[]

// tag::run-tool[]

// runTool runs one tool call and returns the tool message that answers
// it. Every problem becomes an error message the model can read.
func runTool(ctx context.Context, byName map[string]Tool,
	call ToolCall) Message {
	msg := Message{Role: "tool", ToolCallID: call.ID}
	out, err := lookupAndRun(ctx, byName, call)
	if err != nil {
		msg.IsError = true
		msg.Content = err.Error()
		return msg
	}
	msg.Content = out
	return msg
}

// lookupAndRun finds the tool a call names and runs it.
func lookupAndRun(ctx context.Context, byName map[string]Tool,
	call ToolCall) (string, error) {
	t, ok := byName[call.Name]
	if !ok {
		names := slices.Sorted(maps.Keys(byName))
		return "", &toolError{ErrUnknownTool, fmt.Sprintf(
			"no tool named %q; the tools are %s",
			call.Name, strings.Join(names, ", "))}
	}
	return t.Run(ctx, call.Arguments)
}

// end::run-tool[]
