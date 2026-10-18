package agent

import (
	"context"
	"errors"
	"iter"
)

// ErrBeforeAndState means Resume was given Limits.Before as well as a
// saved State. Both carry the earlier turns and usage, and counting
// them from both would count them twice.
var ErrBeforeAndState = errors.New("agent: Limits.Before set for " +
	"a resumed run; the State already holds its counts")

// ErrNoRunID means Limits.Checkpoint was set for Run, RunStream, or
// Events, which have no run ID to save under or to make keys from; a
// run that saves its state starts with Resume and NewState.
var ErrNoRunID = errors.New("agent: Limits.Checkpoint needs a run ID; " +
	"use Resume with NewState")

// tag::resume[]

// Resume continues a saved run with the same tools and limits. A done
// run returns its saved Result without calling the model. Otherwise the
// run goes on from its phase: calls of the last reply without answers
// run, those an earlier process may have run go to the resolution
// ladder, and decisions arrive through lim.Approver. The saved turns
// and usage count against lim's limits.
func Resume(ctx context.Context, m StreamModel, tools []Tool, st State,
	lim Limits) (Result, error) {
	for ev := range ResumeEvents(ctx, m, tools, st, lim) {
		if ev.Kind == StoppedEvent {
			return ev.Result, ev.Err
		}
	}
	return Result{}, errGone
}

// ResumeEvents is Resume, reporting what happens as Events does. A run
// whose saved workspace no longer holds what it wrote stops with
// ErrWorkspaceChanged before any model call (chapter 17).
func ResumeEvents(ctx context.Context, m StreamModel, tools []Tool,
	st State, lim Limits) iter.Seq[Event] {
	switch {
	case lim.Before != nil: // <1>
		return stoppedAt(st.Result(), ErrBeforeAndState)
	case st.Phase == PhaseDone: // <2>
		return stoppedAt(st.Result(), nil)
	case lim.Checkpoint != nil && st.RunID == "":
		return stoppedAt(st.Result(), ErrNoRunID)
	}
	if err := st.checkSaved(); err != nil { // <3>
		return stoppedAt(st.Result(), err)
	}
	return events(ctx, m, tools, st, lim)
}

// end::resume[]

// stoppedAt is a run that ends at once, with res and err.
func stoppedAt(res Result, err error) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		yield(Event{Kind: StoppedEvent, Result: res, Err: err})
	}
}
