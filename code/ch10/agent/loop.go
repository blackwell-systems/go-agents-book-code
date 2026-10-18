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

// Run runs the agent loop on a Model that doesn't stream: it is
// RunStream over Streamed(m), so there is one loop for both.
func Run(ctx context.Context, m Model, tools []Tool,
	msgs []Message, lim Limits) (Result, error) {
	return RunStream(ctx, Streamed(m), tools, msgs, lim)
}

// RunStream runs the loop and returns when it ends, for callers that
// don't need to see the run as it happens.
func RunStream(ctx context.Context, m StreamModel, tools []Tool,
	msgs []Message, lim Limits) (Result, error) {
	for ev := range Events(ctx, m, tools, msgs, lim) {
		if ev.Kind == StoppedEvent {
			return ev.Result, ev.Err
		}
	}
	return Result{}, errNoStop // Events always ends with StoppedEvent
}

// end::run[]

var errNoStop = errors.New("agent: the run ended without a stop event")

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
	return lim.Spend.check() // a pool shared with other runs (chapter 9)
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
	seen := map[string]bool{} // IDs must be unique in the conversation
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			seen[c.ID] = true
		}
	}
	for _, c := range reply.Message.ToolCalls { // <2>
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

// end::check[]

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

// tag::lookup[]

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
	return runSafely(ctx, t, call)
}

// tag::run-safely[]

// runSafely runs a tool and turns a panic inside it into an error the
// model reads, so one broken tool can't crash the whole agent. The
// panic is a bug in the tool, so the message says to try another way.
func runSafely(ctx context.Context, t Tool, call ToolCall) (
	out string, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = "", &toolError{ErrToolPanic, fmt.Sprintf(
				"%s failed with an internal error (%v); "+
					"try another way, or report it", call.Name, p)}
		}
	}()
	return t.Run(ctx, call.Arguments)
}

// end::run-safely[]

// end::lookup[]
