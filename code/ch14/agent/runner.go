package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// maxParallel is how many tool calls of one reply run at once.
const maxParallel = 8

// tag::stages[]

// stages splits a reply's calls, in the model's order, into the groups
// that run together: consecutive calls to read-only tools share a
// stage, and any other call, which may change something a later call
// reads, is a stage of its own, a barrier. So is a call that may wait
// for a person (chapter 14), so a pause leaves no call half-run.
func stages(byName map[string]Tool, calls []ToolCall) [][]int {
	shares := func(name string) bool {
		t := byName[name]
		return t.ReadOnly && t.Approval == nil
	}
	var out [][]int
	for i, c := range calls {
		n := len(out)
		if shares(c.Name) && n > 0 && shares(calls[out[n-1][0]].Name) {
			out[n-1] = append(out[n-1], i)
			continue
		}
		out = append(out, []int{i})
	}
	return out
}

// end::stages[]

// tag::run-stage[]

// runTools answers every call in a reply, stage by stage, running the
// calls of a stage at once and passing each answer to yield in the
// model's order. Every call gets an answer, even one that never ran. It
// reports false if yield did, and returns an error wrapping
// ErrToolFatal when a tool can't run at all.
func runTools(ctx context.Context, byName map[string]Tool,
	calls []ToolCall, yield func(Message) bool) ([]Message, bool, error) {
	answers := make([]Message, len(calls))
	fatal := make([]error, len(calls))
	slots := NewSlots(maxParallel)
	var stop, ended string // why later calls don't run, if they don't
	for _, stage := range stages(byName, calls) {
		var wg sync.WaitGroup
		for _, i := range stage {
			why := skipReason(ctx, stop, ended)
			if why == "" && slots.Acquire(ctx) != nil { // <1>
				why = skipReason(ctx, stop, ended)
			}
			if why != "" {
				answers[i] = notRun(calls[i], why)
				continue
			}
			wg.Go(func() { // <2>
				defer slots.Release()
				answers[i], fatal[i] = runTool(ctx, byName, calls[i])
			})
		}
		wg.Wait()
		// end::run-stage[]
		// tag::answer-stage[]
		for _, i := range stage { // <1>
			if isPending(fatal[i]) { // chapter 14
				return answers[:i], true, fatal[i]
			}
			if err := fatal[i]; err != nil && stop == "" {
				stop = err.Error()
			}
			if byName[calls[i].Name].Final && !answers[i].IsError {
				ended = calls[i].Name
			}
			if !yield(answers[i]) {
				return answers[:i+1], false, nil
			}
		}
	}
	return answers, true, errors.Join(fatal...)
}

// end::answer-stage[]

// tag::not-run[]

// skipReason says why a call can't start, or "" if it can.
func skipReason(ctx context.Context, stop, ended string) string {
	switch {
	case ctx.Err() != nil:
		return "cancelled before it ran: " + context.Cause(ctx).Error()
	case stop != "":
		return "not run: " + stop
	case ended != "":
		return "not run: the run ended when " + ended + " was accepted"
	}
	return ""
}

// notRun is the answer to a call that didn't run, so every call in the
// conversation still has one.
func notRun(call ToolCall, why string) Message {
	return Message{Role: "tool", ToolCallID: call.ID, IsError: true,
		Content: why}
}

// end::not-run[]

// tag::run-tool[]

// runTool runs one tool call and returns the tool message that answers
// it. Every problem becomes an error message the model can read; a tool
// that can't run at all is also returned as an error, which stops the
// run.
func runTool(ctx context.Context, byName map[string]Tool,
	call ToolCall) (Message, error) {
	msg := Message{Role: "tool", ToolCallID: call.ID}
	ctx = context.WithValue(ctx, callKey{}, call) // for wrappers (chapter 12)
	out, err := lookupAndRun(ctx, byName, call)
	switch {
	case errors.Is(err, ErrToolFatal):
		msg.IsError, msg.Content = true, "not run: "+err.Error()
		return msg, fmt.Errorf("%s: %w", call.Name, err)
	case errors.Is(err, ErrOutcomeUnknown): // chapter 12
		msg.IsError, msg.Content = true, "outcome unknown: "+err.Error()
		return msg, fmt.Errorf("%s: %w", call.Name, err)
	case isPending(err) && byName[call.Name].Approval != nil: // chapter 14
		return Message{}, err // the call waits, unanswered
	case isPending(err): // from inside a tool, such as a sub-agent's
		msg.IsError = true
		msg.Content = "not finished: a call inside it waits for a " +
			"person's decision: " + err.Error()
	case err != nil: // the model reads it and tries again
		msg.IsError, msg.Content = true, err.Error()
	default:
		msg.Content = out
	}
	return msg, nil
}

// end::run-tool[]
