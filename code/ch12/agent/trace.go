package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// tag::span[]

// A Span is one timed piece of a run: the run itself, a turn, a model
// call, an attempt, a tool call. Spans nest by the context they were
// started from.
type Span struct {
	ID     int // its index in the Recorder
	Parent int // the parent's ID, or -1 for a root
	Name   string
	Attrs  []slog.Attr
	Start  time.Time
	End    time.Time // zero while the span is open
	Err    error
}

// A Recorder keeps every span a run starts, in memory, and prints them
// as a tree. It is the whole tracing system for a test or a terminal,
// and the same spans can be sent on to OpenTelemetry.
type Recorder struct {
	Trace string // the trace's ID, random, set by NewRecorder

	mu    sync.Mutex
	spans []*Span
}

// end::span[]

// NewRecorder returns a Recorder with a random trace ID, which logs
// carry so a line can be found in its trace.
func NewRecorder() *Recorder {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return &Recorder{Trace: hex.EncodeToString(b)}
}

// open is a span in progress, carried in a context.
type open struct {
	rec *Recorder
	id  int
}

type spanKey struct{}

// tag::start[]

// Start opens a span named name under the span in ctx, if it belongs
// to r, and returns a context carrying the new span. A nil Recorder
// records nothing, so code can trace unconditionally.
func (r *Recorder) Start(ctx context.Context, name string,
	attrs ...slog.Attr) (context.Context, *Handle) {
	if r == nil {
		return ctx, nil
	}
	parent := -1
	if o, ok := ctx.Value(spanKey{}).(open); ok && o.rec == r {
		parent = o.id
	}
	r.mu.Lock()
	id := len(r.spans)
	r.spans = append(r.spans, &Span{ID: id, Parent: parent,
		Name: name, Attrs: attrs, Start: time.Now()})
	r.mu.Unlock()
	return context.WithValue(ctx, spanKey{}, open{r, id}),
		&Handle{r, id}
}

// end::start[]

// tag::finish[]

// A Handle is how the code that started a span adds to it and ends it.
type Handle struct {
	rec *Recorder
	id  int
}

// Set adds attributes to the span, such as token counts known only
// when a call ends.
func (h *Handle) Set(attrs ...slog.Attr) {
	if h == nil {
		return
	}
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	s := h.rec.spans[h.id]
	s.Attrs = append(s.Attrs, attrs...)
}

// Finish closes the span, with the error the work ended in, if any.
func (h *Handle) Finish(err error) {
	if h == nil {
		return
	}
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	s := h.rec.spans[h.id]
	s.End, s.Err = time.Now(), err
}

// end::finish[]

// tag::tree[]

// String prints the spans as a tree: each span under its parent, with
// its duration (to the millisecond), its attributes, and its error.
func (r *Recorder) String() string {
	var b strings.Builder
	spans := r.Spans()
	var walk func(parent, depth int)
	walk = func(parent, depth int) {
		for _, s := range spans {
			if s.Parent != parent {
				continue
			}
			fmt.Fprintf(&b, "%s%s %s", strings.Repeat("  ", depth),
				s.Name, s.duration())
			for _, a := range s.Attrs {
				fmt.Fprintf(&b, " %s", a)
			}
			if s.Err != nil {
				fmt.Fprintf(&b, " error=%q", s.Err)
			}
			b.WriteByte('\n')
			walk(s.ID, depth+1)
		}
	}
	walk(-1, 0)
	return b.String()
}

// end::tree[]

// duration is how long s ran, rounded to the millisecond (or to the
// microsecond under one), or "open" if it hasn't finished.
func (s Span) duration() string {
	if s.End.IsZero() {
		return "open"
	}
	d := s.End.Sub(s.Start)
	if d >= time.Millisecond {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Microsecond).String()
}

// Spans returns a copy of the spans recorded so far, in the order they
// started, for tests and exporters.
func (r *Recorder) Spans() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Span, len(r.spans))
	for i, s := range r.spans {
		out[i] = *s
		out[i].Attrs = append([]slog.Attr(nil), s.Attrs...)
	}
	return out
}
