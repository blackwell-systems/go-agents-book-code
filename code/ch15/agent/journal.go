package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// tag::journal[]

// A journal keeps a run's checkpoint up to date as the loop goes
// (chapter 15). With no Checkpointer it saves nothing, but it still
// settles calls that a saved run left with markers.
type journal struct {
	cp    Checkpointer
	st    State           // markers, run ID, restarts, as saved
	res   *Result         // the run so far
	extra []Message       // answers to this reply, not in res yet
	old   map[string]bool // calls with markers from an earlier process

	// unmarked holds calls whose marker couldn't be saved, so they
	// never started. Written on a call's goroutine, read after its
	// stage ends.
	unmarked sync.Map

	// unsettled holds calls whose answers in the Result don't settle
	// them, such as "cancelled before it ran": saves leave those
	// answers out.
	unsettled map[string]bool
}

// end::journal[]

// newJournal starts a journal from a run's state. Markers already in
// the state were left by an earlier process: their calls' outcomes are
// unknown.
func newJournal(cp Checkpointer, st State) *journal {
	j := &journal{cp: cp, st: st, old: map[string]bool{},
		unsettled: map[string]bool{}}
	for _, a := range st.Attempts {
		j.old[a.CallID] = true
	}
	return j
}

// start returns the Result the loop builds on: the saved run's.
func (j *journal) start() *Result {
	res := j.st.Result()
	res.Pending = nil // set again if the run pauses again
	j.res = &res
	return j.res
}

// phaseFor is the phase of a run whose last reply left these calls
// unanswered.
func phaseFor(calls []ToolCall) Phase {
	if len(calls) > 0 {
		return PhaseTools
	}
	return PhaseModel
}

// tag::save[]

// save writes the run as it is now. A tool message whose call still
// has a marker is left out: the call's outcome is unknown, so on
// resume it goes to the resolution ladder instead of counting as
// answered. So is one that doesn't settle its call. The save ignores
// cancellation, since the save that keeps a stopping run's work must
// finish.
func (j *journal) save(ctx context.Context, phase Phase) error {
	if j.cp == nil {
		return nil
	}
	st := j.st
	st.Version, st.Phase = StateVersion, phase
	st.Messages = j.settledMessages() // <1>
	st.Usage, st.Turns = j.res.Usage, j.res.Turns
	st.Context, st.Final = j.res.Context, j.res.Final
	st.Pending = j.res.Pending
	err := j.cp.Save(context.WithoutCancel(ctx), st) // <2>
	if err != nil {
		return fmt.Errorf("save run: %w", err)
	}
	return nil
}

// end::save[]

// settledMessages is the conversation as a save writes it: without the
// answers that don't settle their calls.
func (j *journal) settledMessages() []Message {
	msgs := append(slices.Clone(j.res.Messages), j.extra...)
	return slices.DeleteFunc(msgs, func(m Message) bool {
		_, marked := j.st.Attempt(m.ToolCallID)
		return m.Role == "tool" && (marked || j.unsettled[m.ToolCallID])
	})
}

// tag::mark[]

// mark saves an attempt marker before a call that may change something
// outside the run. A call that already has one, from an earlier
// process, keeps it.
func (j *journal) mark(ctx context.Context, t Tool,
	call ToolCall) error {
	if j.cp == nil || t.Run == nil || t.ReadOnly || j.old[call.ID] {
		return nil
	}
	a := Attempt{CallID: call.ID, Tool: call.Name,
		Started: time.Now().UTC()}
	if t.Keyed && j.st.RunID != "" { // a key only if one is sent
		a.Key = Key(j.st.RunID, call.ID)
	}
	j.st.Attempts = append(j.st.Attempts, a)
	if err := j.save(ctx, PhaseTools); err != nil {
		j.dropMarker(call.ID) // the call doesn't go out
		j.unmarked.Store(call.ID, true)
		return err
	}
	return nil
}

// end::mark[]

// tag::mark-start[]

type markerKey struct{}

func withMarker(ctx context.Context,
	mark func() error) context.Context {
	return context.WithValue(ctx, markerKey{}, mark)
}

// MarkStart saves the attempt marker for the call ctx runs, if the run
// keeps a checkpoint. A call whose tool needs a person's approval is
// marked here rather than before it starts: the approval gate calls
// MarkStart after a yes, just before the call goes out, so a run
// stopped while a person decides leaves no marker for a call that
// never ran (chapter 15).
func MarkStart(ctx context.Context) error {
	if mark, ok := ctx.Value(markerKey{}).(func() error); ok {
		return mark()
	}
	return nil
}

// end::mark-start[]

// unmark drops the marker of a call that stopped to wait for a person
// before it ran. A marker from an earlier process stays: that call's
// outcome is still unknown.
func (j *journal) unmark(call ToolCall) {
	if j.old[call.ID] {
		return
	}
	j.dropMarker(call.ID)
}

func (j *journal) dropMarker(id string) {
	j.st.Attempts = slices.DeleteFunc(j.st.Attempts, func(a Attempt) bool {
		return a.CallID == id
	})
}

// tag::answered[]

// answered saves a call's answer, unless the answer doesn't settle the
// call. ran says whether the call started; stopped, that it didn't
// because an earlier call stopped the run.
func (j *journal) answered(ctx context.Context, m Message, ran,
	stopped bool, err error) error {
	j.extra = append(j.extra, m)
	if !ran && (stopped || ctx.Err() != nil) { // <1>
		j.unmark(ToolCall{ID: m.ToolCallID})
	}
	if !j.settles(ctx, m, ran, stopped, err) { // <2>
		j.unsettled[m.ToolCallID] = true
		return nil
	}
	j.dropMarker(m.ToolCallID)
	delete(j.old, m.ToolCallID)
	return j.save(ctx, PhaseTools)
}

// end::answered[]

// tag::settles[]

// settles reports whether an answer settles its call. A call that
// never started because the run stopped runs after a restart. A call
// that started and ended in an error the run's stop may have caused,
// or that leaves its effect unknown, keeps its marker for the ladder:
// a failure after the request went out doesn't mean nothing happened.
func (j *journal) settles(ctx context.Context, m Message, ran,
	stopped bool, err error) bool {
	switch {
	case j.notStarted(m.ToolCallID): // its marker couldn't be saved
		return false
	case !ran:
		return !stopped && ctx.Err() == nil // ended: the run is done
	case errors.Is(err, ErrOutcomeUnknown), errors.Is(err, ErrToolFatal):
		return false
	case m.IsError && ctx.Err() != nil:
		return false
	}
	return true
}

// end::settles[]

// notStarted reports whether a call never started because its marker
// couldn't be saved.
func (j *journal) notStarted(id string) bool {
	_, ok := j.unmarked.Load(id)
	return ok
}

// settled is called once a reply's answers are in the Result.
func (j *journal) settled() { j.extra = nil }

// tag::settle[]

// settle runs a call, unless an earlier process started it and never
// saved its answer: then the resolution ladder decides whether to run
// it again or answer it with what the record or a person says. It
// reports whether the call waits for a person's answer.
func (j *journal) settle(ctx context.Context, byName map[string]Tool,
	call ToolCall, run func(context.Context, map[string]Tool,
		ToolCall) (string, error)) (string, bool, error) {
	a, ok := j.inherited(call.ID)
	if !ok {
		out, err := run(ctx, byName, call)
		return out, false, err
	}
	r, err := Resolve(ctx, byName[call.Name], j.st.RunID, call, a,
		ApproverFrom(ctx))
	switch {
	case isPending(err):
		return "", true, err
	case err != nil: // still unknown: stop, keeping the marker
		//nolint:errorlint // the cause stays out of the chain
		return "", false, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	case !r.Run:
		return r.Answer.Content, false, nil
	}
	out, err := run(ctx, byName, call)
	if errors.Is(err, ErrDenied) { // <1>
		return j.askInstead(ctx, call, a, err)
	}
	return out, false, err
}

// end::settle[]

// askInstead puts the outcome question to a person when the approval
// gate refused to run a call again: the call may already have taken
// effect, so a refusal can't settle it as not done.
func (j *journal) askInstead(ctx context.Context, call ToolCall,
	a Attempt, denied error) (string, bool, error) {
	r, err := ask(ctx, j.st.RunID, call, a, "; running it again was "+
		"refused: "+denied.Error(), ApproverFrom(ctx))
	switch {
	case isPending(err):
		return "", true, err
	case err != nil:
		//nolint:errorlint // the cause stays out of the chain
		return "", false, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	case r.Run:
		return "", false, fmt.Errorf("%w: rerun approved, but the "+
			"gate refused it: %v", ErrOutcomeUnknown, denied)
	}
	return r.Answer.Content, false, nil
}

// inherited returns the marker an earlier process left for a call.
// Only the loop's goroutine changes markers, between stages, so reading
// them while a stage runs is safe.
func (j *journal) inherited(id string) (Attempt, bool) {
	if !j.old[id] {
		return Attempt{}, false
	}
	return j.st.Attempt(id)
}

// tag::finish[]

// finish saves the run as it stopped: done with an answer, paused for a
// person, or in the phase it was in when an error stopped it, so that
// Resume can continue it once the cause is gone.
func (j *journal) finish(ctx context.Context, res Result,
	err error) error {
	if j.res == nil {
		return err
	}
	*j.res = res
	j.extra = nil
	phase := PhaseModel
	calls, _ := unanswered(j.settledMessages())
	switch {
	case err == nil:
		phase = PhaseDone
	case isPending(err):
		phase = PhasePaused
	case len(calls) > 0 || len(j.st.Attempts) > 0:
		phase = PhaseTools
	}
	if serr := j.save(ctx, phase); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

// end::finish[]
