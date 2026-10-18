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
	calls []ToolCall, j *journal,
	yield func(Message) bool) ([]Message, bool, error) {
	answers := make([]Message, len(calls))
	fatal := make([]error, len(calls))
	ran := make([]bool, len(calls))     // chapter 15
	stopped := make([]bool, len(calls)) // not run: an earlier call
	slots := NewSlots(maxParallel)
	var stop, ended string // why later calls don't run, if they don't
	for _, stage := range stages(byName, calls) {
		var wg sync.WaitGroup
		for _, i := range stage {
			why := skipReason(ctx, stop, ended)
			if t := byName[calls[i].Name]; why == "" &&
				t.Approval == nil { // a marker first (chapter 15)
				if err := j.mark(ctx, t, calls[i]); err != nil {
					fatal[i], why = err, "not run: "+err.Error()
				}
			}
			if why == "" && slots.Acquire(ctx) != nil { // <1>
				why = skipReason(ctx, stop, ended)
			}
			if why != "" {
				answers[i] = notRun(calls[i], why)
				stopped[i] = stop != "" && ctx.Err() == nil
				continue
			}
			wg.Go(func() { // <2>
				defer slots.Release()
				answers[i], fatal[i] = runTool(ctx, byName, calls[i], j)
				ran[i] = true
			})
		}
		wg.Wait()
		// end::run-stage[]
		// tag::answer-stage[]
		for _, i := range stage { // <1>
			if isPending(fatal[i]) { // chapter 14
				j.unmark(calls[i]) // it never ran
				return answers[:i], true, fatal[i]
			}
			if err := fatal[i]; err != nil && stop == "" {
				stop = err.Error()
			}
			if byName[calls[i].Name].Final && !answers[i].IsError {
				ended = calls[i].Name
			}
			if err := j.answered(ctx, answers[i], ran[i], stopped[i],
				fatal[i]); err != nil {
				return answers[:i+1], true, err
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
	call ToolCall, j *journal) (Message, error) {
	msg := Message{Role: "tool", ToolCallID: call.ID}
	ctx = context.WithValue(ctx, callKey{}, call) // for wrappers (ch. 12)
	if run := runFrom(ctx); run != "" {
		ctx = WithKey(ctx, Key(run, call.ID)) // chapter 15
	}
	if t := byName[call.Name]; t.Approval != nil { // marked after a yes
		mark := func() error { return j.mark(ctx, t, call) }
		ctx = withMarker(ctx, mark)
	}
	out, asked, err := j.settle(ctx, byName, call, lookupAndRun)
	switch {
	case err != nil && j.notStarted(call.ID): // no marker: it stops
		msg.IsError, msg.Content = true, "not run: "+err.Error()
		return msg, fmt.Errorf("%s: %w", call.Name, err)
	case errors.Is(err, ErrToolFatal): // it may have acted
		msg.IsError, msg.Content = true, "stopped: "+err.Error()
		return msg, fmt.Errorf("%s: %w", call.Name, err)
	case errors.Is(err, ErrOutcomeUnknown): // chapter 12
		msg.IsError, msg.Content = true, "outcome unknown: "+err.Error()
		return msg, fmt.Errorf("%s: %w", call.Name, err)
	case isPending(err) && (asked || byName[call.Name].Approval != nil):
		return Message{}, err // the call waits, unanswered (chapter 14)
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
