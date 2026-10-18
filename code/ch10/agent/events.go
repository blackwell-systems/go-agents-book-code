package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
)

// tag::event[]

// An EventKind says what an Event reports.
type EventKind int

// The kinds of Event, in the order a turn produces them.
const (
	TextEvent       EventKind = iota + 1 // a piece of the model's text
	CallEvent                            // a complete tool call
	ToolResultEvent                      // a tool's result
	TurnEvent                            // a turn with tool calls ended
	StoppedEvent                         // the run ended; always last
)

// An Event is something that happened during a run.
type Event struct {
	Kind   EventKind
	Text   string   // TextEvent: the new text
	Call   ToolCall // CallEvent: the call
	Tool   Message  // ToolResultEvent: the tool message
	Turn   int      // TurnEvent: how many turns have ended
	Result Result   // StoppedEvent: the run, as far as it went
	Err    error    // StoppedEvent: why it stopped; nil for an answer

	// Agent names the run an event came from when several run at once;
	// Merge sets it (chapter 9).
	Agent string
}

// end::event[]

// tag::events[]

// errGone means the caller stopped reading events: yield returned
// false, and calling it again would panic, so the loop just returns.
var errGone = errors.New("agent: the caller stopped reading events")

// Events runs the agent loop and reports what happens as it happens.
// The last event is always a StoppedEvent with the Result and the
// error, unless the caller stops reading first: breaking out of the
// range loop stops the run, cancelling the model call in progress.
func Events(ctx context.Context, m StreamModel, tools []Tool,
	msgs []Message, lim Limits) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		ctx, cancel := context.WithCancel(ctx) // <1>
		defer cancel()
		res, err := loop(ctx, m, tools, msgs, lim, yield)
		if errors.Is(err, errGone) { // <2>
			return
		}
		yield(Event{Kind: StoppedEvent, Result: res, Err: err})
	}
}

// end::events[]

// tag::loop[]

// loop is the hardened loop from chapter 5, reporting to yield.
func loop(ctx context.Context, m StreamModel, tools []Tool,
	msgs []Message, lim Limits, yield func(Event) bool) (Result, error) {
	defs, byName := index(tools)
	res := Result{Messages: slices.Clone(msgs)}
	nudges := 0
	for turn := range lim.MaxTurns {
		if err := beforeTurn(ctx, res, lim); err != nil {
			return res, err
		}
		spent := res.Usage.Cost
		sent, err := build(ctx, lim, &res)
		if err != nil {
			return res, err
		}
		if res.Usage.Cost > spent { // a summary cost money: check again
			if err := beforeTurn(ctx, res, lim); err != nil {
				return res, err
			}
		}
		req := Request{Messages: sent, Tools: defs}
		reply, err := receive(ctx, m, req, yield) // <1>
		res.Usage = res.Usage.Add(reply.Usage)
		lim.Spend.Add(reply.Usage.Cost)
		if err != nil {
			return res, err
		}
		if err := check(reply, res.Messages); err != nil {
			return res, err
		}
		res.Messages = append(res.Messages, reply.Message)
		for _, c := range reply.Message.ToolCalls {
			if !yield(Event{Kind: CallEvent, Call: c}) {
				return res, errGone
			}
		}
		// tag::nudge[]
		if len(reply.Message.ToolCalls) == 0 {
			final, ok := finalTool(tools)
			if !ok {
				return res, nil // an answer
			}
			if nudges == maxNudges { // <1>
				return res, ErrNoFinalAnswer
			}
			nudges++
			res.Messages = append(res.Messages, nudge(final)) // <2>
			continue
		}
		// end::nudge[]
		// end::loop[]
		// tag::loop-tools[]
		answers, ok, fatal := runTools(ctx, byName,
			reply.Message.ToolCalls, func(m Message) bool {
				return yield(Event{Kind: ToolResultEvent, Tool: m})
			})
		res.Messages = append(res.Messages, answers...)
		switch {
		case !ok:
			return res, errGone
		case fatal != nil: // <1>
			return res, fmt.Errorf("run tools: %w", fatal)
		}
		if args, done := finished(byName, reply.Message.ToolCalls, answers); done {
			res.Final = args // <2>
			return res, nil
		}
		if !yield(Event{Kind: TurnEvent, Turn: turn + 1}) {
			return res, errGone
		}
	}
	if err := stopped(ctx); err != nil {
		return res, err
	}
	return res, ErrMaxTurns
}

// end::loop-tools[]

// tag::receive[]

// receive streams one reply, passing its text on as it arrives, and
// returns the assembled reply. If the stream fails, the partial reply
// is dropped; only its usage, if any arrived, comes back.
func receive(ctx context.Context, m StreamModel, req Request,
	yield func(Event) bool) (Reply, error) {
	var a assembler
	for d, err := range m.Stream(ctx, req) {
		if err != nil {
			if d.Usage != nil {
				a.reply.Usage = *d.Usage
			}
			return Reply{Usage: a.reply.Usage},
				fmt.Errorf("generate: %w", err) // <1>
		}
		if err := a.add(d); err != nil {
			return Reply{Usage: a.reply.Usage}, err
		}
		if d.Text != "" && !yield(Event{Kind: TextEvent, Text: d.Text}) {
			return Reply{Usage: a.reply.Usage}, errGone
		}
	}
	return a.result(), nil
}

// end::receive[]

// tag::build[]

// build returns the messages this turn's request sends: the whole
// conversation, or what the context policy builds from it. The policy's
// state and cost are kept in the run's Result, and the cost goes to
// the spending pool, if there is one.
func build(ctx context.Context, lim Limits, res *Result) (
	[]Message, error) {
	if lim.Context == nil {
		return res.Messages, nil
	}
	w, err := lim.Context.Build(ctx, res.Messages, res.Context)
	res.Usage = res.Usage.Add(w.Usage)
	lim.Spend.Add(w.Usage.Cost)
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}
	res.Context = w.State
	return w.Messages, nil
}

// end::build[]
