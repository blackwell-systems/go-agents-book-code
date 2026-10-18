package agent

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// tag::trim[]

// Trim leaves the oldest turns out of the request once it is estimated
// to be over Limit tokens, keeping the head and the latest turn. It cuts
// Chunk turns at a time, and the cut only moves forward, so between
// cuts every request starts with the same messages and the provider's
// cache keeps working. A turn's reasoning details go with it.
type Trim struct {
	Limit int // tokens
	Chunk int // turns cut at a time; 0 means 4
}

// Build returns the request's messages, moving the cut forward until
// the request fits.
func (t Trim) Build(_ context.Context, msgs []Message,
	st ContextState) (Window, error) {
	h := head(msgs)
	cut := max(st.Cut, h)
	starts := turnStarts(msgs, cut)
	for Estimate(trimmed(msgs, h, cut)) > t.Limit && len(starts) > 1 {
		k := min(cmp.Or(t.Chunk, 4), len(starts)-1) // always keep the last turn
		cut, starts = starts[k], starts[k:]
	}
	st.Cut = cut
	return Window{Messages: trimmed(msgs, h, cut), State: st}, nil
}

// end::trim[]

// tag::trimmed[]

// trimmed is the head, a note saying what was left out, and the
// conversation from cut on.
func trimmed(msgs []Message, h, cut int) []Message {
	out := slices.Clone(msgs[:h])
	if cut > h {
		out = append(out, note("%d earlier messages are left out of "+
			"this request to save space; read files or reports again "+
			"if you need them", cut-h))
	}
	return append(out, msgs[cut:]...)
}

// end::trimmed[]

// tag::clear[]

// ClearResults replaces the content of old, long tool results with a
// note saying how to get them again, keeping every message, so no call
// loses its answer. It waits until the request is estimated past Limit
// tokens: cleared results that the model still needs cost it a call to
// read them again. Results in the last Keep turns stay whole, and the
// boundary moves in chunks, as Trim's cut does, so the cache survives
// most turns.
type ClearResults struct {
	Limit int // tokens; clear only past this, 0 means at once
	Keep  int // recent turns whose results stay; 0 means 3
	Min   int // results of this many bytes or fewer stay; 0 means 500
	Chunk int // turns cleared at a time; 0 means 4
}

// Build returns a copy of the conversation with old results cleared.
func (c ClearResults) Build(_ context.Context, msgs []Message,
	st ContextState) (Window, error) {
	keep, chunk := cmp.Or(c.Keep, 3), cmp.Or(c.Chunk, 4)
	out := c.cleared(msgs, st.Cleared)
	starts := turnStarts(msgs, max(st.Cleared, head(msgs)))
	if len(starts) >= keep+chunk && Estimate(out) > c.Limit { // <1>
		st.Cleared = starts[len(starts)-keep]
		out = c.cleared(msgs, st.Cleared)
	}
	return Window{Messages: out, State: st}, nil
}

// end::clear[]

// cleared returns a copy of msgs with the long tool results before
// index n replaced by a note.
func (c ClearResults) cleared(msgs []Message, n int) []Message {
	out := slices.Clone(msgs)
	calls := map[string]ToolCall{}
	for i := range out[:n] {
		for _, tc := range out[i].ToolCalls {
			calls[tc.ID] = tc
		}
		m := &out[i]
		if m.Role == "tool" && len(m.Content) > cmp.Or(c.Min, 500) {
			tc := calls[m.ToolCallID]
			m.Content = fmt.Sprintf("[cleared to save space: the "+
				"result of %s %s, %d bytes; read the file or report "+
				"again if you need it]", tc.Name,
				clip(string(tc.Arguments), 80), len(m.Content))
		}
	}
	return out
}

// tag::cache-marks[]

// CacheMarks marks where a provider that needs explicit markers may
// cache the request: the end of the head, which never changes, and the
// last message, so the next request, which starts with all of this one,
// can read it back from the cache. It changes no content.
type CacheMarks struct{}

// Build returns a copy of the conversation with its cache points set.
func (CacheMarks) Build(_ context.Context, msgs []Message,
	st ContextState) (Window, error) {
	out := slices.Clone(msgs)
	if h := head(out); h > 0 {
		out[h-1].CachePoint = true
	}
	if len(out) > 0 {
		out[len(out)-1].CachePoint = true
	}
	return Window{Messages: out, State: st}, nil
}

// end::cache-marks[]
