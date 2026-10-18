package agent

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"iter"
	"strings"
)

// tag::delta[]

// A Delta is one piece of a streamed reply. Most carry a little text or
// a piece of one tool call; one near the end carries the finish reason,
// and the last carries the usage.
type Delta struct {
	Text         string     // a piece of the reply's text
	Call         *CallDelta // a piece of one tool call
	FinishReason string     // why the reply ended, when it has
	Usage        *Usage     // what the call consumed, at the end

	// ReasoningDetails holds reasoning records, a JSON array, which the
	// assembler joins into the reply's ReasoningDetails.
	ReasoningDetails jsontext.Value

	// ReasoningFrom names the client the records came from (chapter
	// 11); the assembler copies it into the reply.
	ReasoningFrom string

	// ID is the generation's ID, which the assembler copies into
	// Reply.ID (chapter 12).
	ID string

	// Reset says that every Delta before this one belonged to an
	// attempt that failed and is being retried: discard them (chapter
	// 12).
	Reset bool
}

// A CallDelta is a piece of one tool call. The first piece for an Index
// carries the call's ID and name; later pieces carry more of its
// arguments.
type CallDelta struct {
	Index     int    // which tool call in the reply this belongs to
	ID        string // set on the first piece only
	Name      string // set on the first piece only
	Arguments string // the next fragment of the arguments
}

// end::delta[]

// tag::stream-model[]

// A StreamModel produces the next reply as a stream of pieces. The
// stream ends after the last Delta, or with an error.
type StreamModel interface {
	Stream(ctx context.Context, req Request) iter.Seq2[Delta, error]
}

// end::stream-model[]

// tag::streamed[]

// Streamed turns a Model into a StreamModel whose stream holds the
// whole reply at once, so scripted models and saved replies work with
// the streaming loop.
func Streamed(m Model) StreamModel { return streamed{m} }

type streamed struct{ m Model }

func (s streamed) Stream(ctx context.Context,
	req Request) iter.Seq2[Delta, error] {
	return func(yield func(Delta, error) bool) {
		reply, err := s.m.Generate(ctx, req)
		if err != nil {
			yield(Delta{Usage: &reply.Usage}, err) // <1>
			return
		}
		msg := reply.Message
		ds := []Delta{{Text: msg.Content,
			ReasoningDetails: msg.ReasoningDetails,
			ReasoningFrom:    msg.ReasoningFrom}}
		for i, c := range msg.ToolCalls {
			ds = append(ds, Delta{Call: &CallDelta{Index: i, ID: c.ID,
				Name: c.Name, Arguments: string(c.Arguments)}})
		}
		ds = append(ds, Delta{FinishReason: reply.FinishReason,
			Usage: &reply.Usage})
		for _, d := range ds {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// end::streamed[]

// tag::assembler[]

// maxCalls bounds the tool calls in one reply, so a broken stream can't
// make the assembler allocate without limit.
const maxCalls = 128

// An assembler builds a Reply from the pieces of a stream.
type assembler struct {
	text      strings.Builder
	calls     []ToolCall
	args      [][]byte         // each call's arguments so far
	reasoning []jsontext.Value // reasoning records, in order
	reply     Reply
}

// add takes the next piece of the stream.
func (a *assembler) add(d Delta) error {
	a.text.WriteString(d.Text)
	if d.ReasoningFrom != "" {
		a.reply.Message.ReasoningFrom = d.ReasoningFrom
	}
	if d.ReasoningDetails != nil {
		var recs []jsontext.Value
		if err := json.Unmarshal(d.ReasoningDetails, &recs); err != nil {
			return fmt.Errorf("%w: reasoning details: %w", ErrBadReply, err)
		}
		a.reasoning = append(a.reasoning, recs...)
	}
	if d.FinishReason != "" {
		a.reply.FinishReason = d.FinishReason
	}
	if d.ID != "" {
		a.reply.ID = d.ID
	}
	if d.Usage != nil {
		a.reply.Usage = *d.Usage
	}
	if d.Call != nil {
		return a.addCall(*d.Call)
	}
	return nil
}

// end::assembler[]

// tag::add-call[]

// addCall takes the next piece of one tool call.
func (a *assembler) addCall(c CallDelta) error {
	if c.Index < 0 || c.Index >= maxCalls {
		return fmt.Errorf("%w: call index %d", ErrBadToolCall, c.Index)
	}
	for c.Index >= len(a.calls) { // <1>
		a.calls = append(a.calls, ToolCall{})
		a.args = append(a.args, nil)
	}
	call := &a.calls[c.Index]
	if c.ID != "" && call.ID != "" && c.ID != call.ID { // <2>
		return fmt.Errorf("%w: call %d changed its ID from %q to %q",
			ErrBadToolCall, c.Index, call.ID, c.ID)
	}
	if c.ID != "" {
		call.ID = c.ID
	}
	if c.Name != "" {
		call.Name = c.Name
	}
	a.args[c.Index] = append(a.args[c.Index], c.Arguments...) // <3>
	return nil
}

// end::add-call[]

// tag::result[]

// result returns the assembled Reply, once the stream has ended.
func (a *assembler) result() Reply {
	r := a.reply
	r.Message.Role = "assistant"
	r.Message.Content = a.text.String()
	if len(a.reasoning) > 0 {
		r.Message.ReasoningDetails, _ = json.Marshal(a.reasoning)
	}
	for i, c := range a.calls {
		c.Arguments = jsontext.Value(a.args[i])
		if len(c.Arguments) == 0 {
			c.Arguments = jsontext.Value("{}")
		}
		r.Message.ToolCalls = append(r.Message.ToolCalls, c)
	}
	return r
}

// end::result[]

// tag::collect[]

// Collect assembles a whole reply from a stream, as the loop does, for
// code that wants the reply rather than its pieces, such as a test of
// an adapter's stream (chapter 11).
func Collect(stream iter.Seq2[Delta, error]) (Reply, error) {
	var a assembler
	for d, err := range stream {
		if err != nil { // the error's Delta may carry usage and ID
			if d.Usage != nil {
				a.reply.Usage = *d.Usage
			}
			id := cmp.Or(d.ID, a.reply.ID)
			return Reply{Usage: a.reply.Usage, ID: id}, err
		}
		if d.Reset { // a retry started over (chapter 12)
			a = assembler{}
			continue
		}
		if err := a.add(d); err != nil {
			return Reply{Usage: a.reply.Usage}, err
		}
	}
	return a.result(), nil
}

// end::collect[]
