// Package otel sends the spans of chapter 12's Recorder to
// OpenTelemetry. It is a module of its own so that the rest of the
// book's code doesn't depend on OpenTelemetry: the agent records spans
// with the standard library, and this file is the one place that knows
// OpenTelemetry's API.
package otel

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"log/slog"
	"math/rand/v2"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// tag::export[]

// Export sends one finished run's spans to tp, each with its parent,
// its times, its attributes, and its error. The spans are in the order
// they started, so a parent is always sent before its children.
func Export(ctx context.Context, tp trace.TracerProvider,
	traceID string, spans []agent.Span) {
	tracer := tp.Tracer("go-agents-book/ch12")
	ctx = context.WithValue(ctx, traceKey{}, traceID)
	ctxs := make([]context.Context, len(spans))
	for i, s := range spans {
		parent := ctx
		if s.Parent >= 0 {
			parent = ctxs[s.Parent]
		}
		var span trace.Span
		ctxs[i], span = tracer.Start(parent, s.Name,
			trace.WithTimestamp(s.Start),
			trace.WithSpanKind(kind(s.Attrs)),
			trace.WithAttributes(attributes(s.Attrs)...))
		end(span, s)
	}
}

// end::export[]

// end ends span as s ended, with its error. A span the recorder never
// saw finish, which is a bug in whatever started it, ends now, as an
// error, rather than at the zero time.
func end(span trace.Span, s agent.Span) {
	if s.End.IsZero() {
		span.SetStatus(codes.Error, "the span never finished")
		span.End()
		return
	}
	if s.Err != nil {
		span.RecordError(s.Err)
		span.SetStatus(codes.Error, s.Err.Error())
	}
	span.End(trace.WithTimestamp(s.End))
}

// tag::keep[]

// Keep reports whether to export a run: always when the run itself
// failed, and otherwise for a fraction of runs, chosen at random. A
// tool error the model read and recovered from doesn't count; the run's
// own span, the root, says how it ended. The Recorder holds the whole
// run, so the choice can wait until the run is over.
func Keep(spans []agent.Span, fraction float64) bool {
	for _, s := range spans {
		if s.Parent < 0 && s.Err != nil {
			return true
		}
	}
	return rand.Float64() < fraction
}

// end::keep[]

// kind is the span kind the GenAI conventions ask for: CLIENT for a
// call to a model, INTERNAL for a tool and for an agent that runs in
// this process.
func kind(attrs []slog.Attr) trace.SpanKind {
	for _, a := range attrs {
		if a.Key == "gen_ai.operation.name" && a.Value.String() == "chat" {
			return trace.SpanKindClient
		}
	}
	return trace.SpanKindInternal
}

// attributes converts slog attributes to OpenTelemetry's.
func attributes(attrs []slog.Attr) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		v := a.Value.Resolve()
		switch v.Kind() {
		case slog.KindInt64:
			out = append(out, attribute.Int64(a.Key, v.Int64()))
		case slog.KindFloat64:
			out = append(out, attribute.Float64(a.Key, v.Float64()))
		case slog.KindBool:
			out = append(out, attribute.Bool(a.Key, v.Bool()))
		default:
			out = append(out, attribute.String(a.Key, v.String()))
		}
	}
	return out
}

// Provider returns a TracerProvider that sends spans to exp in
// batches, and that gives a run's spans the trace ID its Recorder
// chose, so the trace field of the run's log lines finds its trace.
func Provider(exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp),
		sdktrace.WithIDGenerator(recorderIDs{}))
}

type traceKey struct{}

// recorderIDs is an sdktrace.IDGenerator that uses the trace ID Export
// put in the context, when there is one, and random IDs otherwise.
type recorderIDs struct{}

func (recorderIDs) NewIDs(ctx context.Context) (trace.TraceID,
	trace.SpanID) {
	var tid trace.TraceID
	s, _ := ctx.Value(traceKey{}).(string)
	if b, err := hex.DecodeString(s); err == nil && len(b) == len(tid) {
		copy(tid[:], b)
	} else {
		_, _ = crand.Read(tid[:]) // crypto/rand.Read never fails
	}
	return tid, recorderIDs{}.NewSpanID(ctx, tid)
}

func (recorderIDs) NewSpanID(context.Context,
	trace.TraceID) trace.SpanID {
	var sid trace.SpanID
	_, _ = crand.Read(sid[:]) // crypto/rand.Read never fails
	return sid
}
