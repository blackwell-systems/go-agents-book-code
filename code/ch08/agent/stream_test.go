package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
)

// streamOf is a StreamModel that yields the same deltas on every call.
type streamOf []Delta

func (s streamOf) Stream(context.Context, Request) iter.Seq2[Delta, error] {
	return func(yield func(Delta, error) bool) {
		for _, d := range s {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// pieces splits a tool call into one delta per byte of its arguments,
// the most awkward split a stream can make.
func pieces(index int, id, name, args string) []Delta {
	ds := []Delta{{Call: &CallDelta{Index: index, ID: id, Name: name}}}
	for i := range len(args) {
		ds = append(ds, Delta{Call: &CallDelta{Index: index,
			Arguments: args[i : i+1]}})
	}
	return ds
}

func TestAssembleAwkwardSplits(t *testing.T) {
	var ds []Delta
	for _, w := range strings.SplitAfter("Reading both files now.", "") {
		ds = append(ds, Delta{Text: w}) // one byte of text at a time
	}
	// Two calls whose pieces arrive interleaved.
	a := pieces(0, "c1", "read_file", `{"path": "duration.go"}`)
	b := pieces(1, "c2", "read_file", `{"path": "duration_test.go"}`)
	for i := range max(len(a), len(b)) {
		if i < len(a) {
			ds = append(ds, a[i])
		}
		if i < len(b) {
			ds = append(ds, b[i])
		}
	}
	ds = append(ds, Delta{FinishReason: "tool_calls"},
		Delta{Usage: &Usage{Cost: 0.5}})

	var asm assembler
	for _, d := range ds {
		if err := asm.add(d); err != nil {
			t.Fatal(err)
		}
	}
	r := asm.result()
	if r.Message.Content != "Reading both files now." ||
		len(r.Message.ToolCalls) != 2 ||
		string(r.Message.ToolCalls[1].Arguments) != `{"path": "duration_test.go"}` ||
		r.Message.ToolCalls[1].ID != "c2" || r.FinishReason != "tool_calls" ||
		r.Usage.Cost != 0.5 {
		t.Errorf("assembled %+v", r)
	}
}

func TestAssembleChangedID(t *testing.T) {
	var asm assembler
	_ = asm.add(Delta{Call: &CallDelta{Index: 0, ID: "a", Name: "x"}})
	err := asm.add(Delta{Call: &CallDelta{Index: 0, ID: "b"}})
	if !errors.Is(err, ErrBadToolCall) {
		t.Errorf("err = %v, want ErrBadToolCall", err)
	}
}

// TestEventsOrder checks the events of a run with one tool call and an
// answer, in order.
func TestEventsOrder(t *testing.T) {
	m := Streamed(&replies{callReply(0, "a"),
		{Message: Message{Role: "assistant", Content: "done"}}})
	var kinds []EventKind
	for ev := range Events(t.Context(), m, okTools, task(), Limits{MaxTurns: 5}) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == StoppedEvent && ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	want := []EventKind{CallEvent, ToolResultEvent, TurnEvent, TextEvent,
		StoppedEvent}
	if !equalKinds(kinds, want) {
		t.Errorf("events %v, want %v", kinds, want)
	}
}

func equalKinds(a, b []EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tag::break-test[]

// TestBreakOutOfEvents stops reading after the first piece of text. The
// model's stream must stop too: its iterator returns, and the context
// it was given is cancelled, which would end an HTTP request.
func TestBreakOutOfEvents(t *testing.T) {
	w := &watchedStream{}
	lim := Limits{MaxTurns: 5}
	for ev := range Events(t.Context(), w, nil, task(), lim) {
		if ev.Kind == TextEvent {
			break
		}
	}
	if !w.returned {
		t.Error("the model's stream is still running")
	}
	if w.ctx.Err() == nil {
		t.Error("the model's context wasn't cancelled")
	}
}

// watchedStream yields text until its consumer stops, and remembers
// that it returned and the context it was given.
type watchedStream struct {
	ctx      context.Context
	returned bool
}

func (w *watchedStream) Stream(ctx context.Context,
	_ Request) iter.Seq2[Delta, error] {
	w.ctx = ctx
	return func(yield func(Delta, error) bool) {
		for yield(Delta{Text: "more "}, nil) {
		}
		w.returned = true
	}
}

// end::break-test[]

// TestStreamErrorDropsPartialReply checks that text streamed before an
// error never joins the conversation.
func TestStreamErrorDropsPartialReply(t *testing.T) {
	failing := errors.New("provider disconnected")
	m := failAfter{text: "half a reply", err: failing}
	res, err := RunStream(t.Context(), m, okTools, task(), Limits{MaxTurns: 5})
	if !errors.Is(err, failing) || len(res.Messages) != 1 {
		t.Errorf("err = %v, %d messages", err, len(res.Messages))
	}
}

// failAfter streams some text, then fails.
type failAfter struct {
	text string
	err  error
}

func (f failAfter) Stream(context.Context, Request) iter.Seq2[Delta, error] {
	return func(yield func(Delta, error) bool) {
		if yield(Delta{Text: f.text}, nil) {
			yield(Delta{}, f.err)
		}
	}
}

var _ StreamModel = streamOf(nil)

func TestAssembleBadIndex(t *testing.T) {
	for _, i := range []int{-1, maxCalls, 1_000_000_000} {
		var asm assembler
		err := asm.add(Delta{Call: &CallDelta{Index: i, ID: "a"}})
		if !errors.Is(err, ErrBadToolCall) {
			t.Errorf("index %d: err = %v", i, err)
		}
	}
}
