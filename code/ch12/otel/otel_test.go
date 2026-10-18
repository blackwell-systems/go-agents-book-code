package otel

import (
	"errors"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// TestExport records a run with a model call, an attempt inside it,
// and a tool call that fails, and checks what OpenTelemetry receives:
// the same tree, the Recorder's trace ID, kinds, attributes, and the
// error.
func TestExport(t *testing.T) {
	rec := agent.NewRecorder()
	ctx, run := rec.Start(t.Context(), "invoke_agent fp_1",
		slog.String("gen_ai.operation.name", "invoke_agent"))
	cctx, chat := rec.Start(ctx, "chat m",
		slog.String("gen_ai.operation.name", "chat"))
	_, attempt := rec.Start(cctx, "attempt", slog.Int("attempt", 1))
	attempt.Finish(nil)
	chat.Set(slog.Int("gen_ai.usage.input_tokens", 120))
	chat.Finish(nil)
	_, tool := rec.Start(ctx, "execute_tool read_file",
		slog.String("gen_ai.operation.name", "execute_tool"))
	tool.Finish(errors.New("no such file"))
	run.Finish(nil)

	exp := tracetest.NewInMemoryExporter()
	tp := Provider(exp)
	Export(t.Context(), tp, rec.Trace, rec.Spans())
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := map[string]tracetest.SpanStub{}
	for _, s := range exp.GetSpans() {
		got[s.Name] = s
	}
	if len(got) != 4 {
		t.Fatalf("exported %d spans, want 4", len(got))
	}
	root := got["invoke_agent fp_1"]
	if root.SpanContext.TraceID().String() != rec.Trace {
		t.Errorf("trace ID %s, want the recorder's %s",
			root.SpanContext.TraceID(), rec.Trace)
	}
	for child, parent := range map[string]string{
		"chat m": "invoke_agent fp_1", "attempt": "chat m",
		"execute_tool read_file": "invoke_agent fp_1"} {
		if got[child].Parent.SpanID() != got[parent].SpanContext.SpanID() {
			t.Errorf("%s isn't a child of %s", child, parent)
		}
	}
	if got["chat m"].SpanKind != trace.SpanKindClient ||
		got["attempt"].SpanKind != trace.SpanKindInternal ||
		got["execute_tool read_file"].SpanKind != trace.SpanKindInternal {
		t.Error("span kinds")
	}
	a := got["chat m"].Attributes
	if len(a) != 2 || a[1].Key != "gen_ai.usage.input_tokens" ||
		a[1].Value.AsInt64() != 120 {
		t.Errorf("chat attributes %v", a)
	}
	if s := got["execute_tool read_file"].Status; s.Code != codes.Error ||
		s.Description != "no such file" {
		t.Errorf("tool status %+v", s)
	}
	if !got["attempt"].EndTime.Equal(rec.Spans()[2].End) {
		t.Error("the attempt's end time changed on the way")
	}
}

// TestKeep checks that a run that failed is always kept, and one that
// succeeded, even after a tool error the model recovered from, only at
// the given fraction.
func TestKeep(t *testing.T) {
	ok := []agent.Span{{Name: "run", Parent: -1}}
	recovered := []agent.Span{{Name: "run", Parent: -1},
		{Name: "execute_tool", Parent: 0, Err: errors.New("no such file")}}
	failed := []agent.Span{{Name: "run", Parent: -1,
		Err: errors.New("max turns")}}
	if !Keep(failed, 0) || Keep(ok, 0) || Keep(recovered, 0) ||
		!Keep(ok, 1) {
		t.Error("Keep")
	}
}

// TestExportOpenSpan checks that a span that never finished is sent
// ending now, as an error, not at the zero time.
func TestExportOpenSpan(t *testing.T) {
	rec := agent.NewRecorder()
	_, _ = rec.Start(t.Context(), "execute_tool boom")
	exp := tracetest.NewInMemoryExporter()
	tp := Provider(exp)
	Export(t.Context(), tp, rec.Trace, rec.Spans())
	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := exp.GetSpans()
	if len(s) != 1 || s[0].EndTime.Before(s[0].StartTime) ||
		s[0].Status.Code != codes.Error {
		t.Errorf("got %+v", s)
	}
}
