package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"iter"
	"log/slog"
)

// callKey carries the tool call a tool is running for, so a wrapper can
// name it; Tool.Run sees only the arguments.
type callKey struct{}

// WithCall returns a context for running a tool for call outside the
// loop, as the loop's runTool does, so wrappers such as the approval
// gate can name it (chapter 17).
func WithCall(ctx context.Context, call ToolCall) context.Context {
	return context.WithValue(ctx, callKey{}, call)
}

// CallFrom returns the tool call the context's tool is running for.
func CallFrom(ctx context.Context) (ToolCall, bool) {
	c, ok := ctx.Value(callKey{}).(ToolCall)
	return c, ok
}

// tag::traced[]

// Traced returns a ModelWare that records each model call as a "chat"
// span under the span in the call's context, with the GenAI attributes.
// It goes outside the retries, so one span covers every attempt.
func Traced(rec *Recorder, provider, model string) ModelWare {
	return func(m StreamModel) StreamModel {
		return StreamFunc(func(ctx context.Context,
			req Request) iter.Seq2[Delta, error] {
			return func(yield func(Delta, error) bool) {
				ctx, span := rec.Start(ctx, opChat+" "+model,
					slog.String(attrOperation, opChat),
					slog.String(attrProvider, provider),
					slog.String(attrRequestModel, model))
				var err error
				defer func() { span.Finish(err) }()
				for d, e := range m.Stream(ctx, req) {
					spanDelta(span, d, e)
					if err = e; !yield(d, e) {
						return
					}
				}
			}
		})
	}
}

// end::traced[]

// tag::traced-attempts[]

// TracedAttempts returns a ModelWare that records each attempt, inside
// the retries, as an "attempt" span with its number and, if it failed,
// error.type. It sets no GenAI attributes: the chat span around it is
// the call, and a second set would count the call's tokens twice.
func TracedAttempts(rec *Recorder) ModelWare {
	return func(m StreamModel) StreamModel {
		return StreamFunc(func(ctx context.Context,
			req Request) iter.Seq2[Delta, error] {
			return func(yield func(Delta, error) bool) {
				ctx, span := rec.Start(ctx, "attempt",
					slog.Int("attempt", AttemptFrom(ctx)))
				var err error
				defer func() { span.Finish(err) }()
				for d, e := range m.Stream(ctx, req) {
					if e != nil {
						span.Set(slog.String(attrErrorType,
							errorType(e)))
					}
					if err = e; !yield(d, e) {
						return
					}
				}
			}
		})
	}
}

// end::traced-attempts[]

// tag::span-delta[]

// spanDelta adds what one Delta says about the call to its span.
func spanDelta(span *Handle, d Delta, err error) {
	if d.ID != "" {
		span.Set(slog.String(attrResponseID, d.ID))
	}
	if u := d.Usage; u != nil {
		span.Set(slog.Int(attrInputTokens, u.PromptTokens),
			slog.Int(attrOutputTokens, u.CompletionTokens),
			slog.Int(attrCachedTokens, u.CachedTokens))
	}
	if err != nil {
		span.Set(slog.String(attrErrorType, errorType(err)))
	}
}

// end::span-delta[]

// tag::traced-tools[]

// TracedTools returns a ToolWare that records each tool call as an
// execute_tool span.
func TracedTools(rec *Recorder) ToolWare {
	return func(t Tool) Tool {
		run := t.Run
		t.Run = func(ctx context.Context,
			args jsontext.Value) (string, error) {
			call, _ := CallFrom(ctx)
			ctx, span := rec.Start(ctx, opExecuteTool+" "+t.Def.Name,
				slog.String(attrOperation, opExecuteTool),
				slog.String(attrToolName, t.Def.Name),
				slog.String(attrToolCallID, call.ID))
			defer func() {
				if p := recover(); p != nil { // <1>
					span.Set(slog.String(attrErrorType, "panic"))
					span.Finish(fmt.Errorf("tool panicked: %v", p))
					panic(p)
				}
			}()
			out, err := run(ctx, args)
			if err != nil {
				span.Set(slog.String(attrErrorType, errorType(err)))
			}
			span.Finish(err)
			return out, err
		}
		return t
	}
}

// end::traced-tools[]

// StartRun opens the run's span, invoke_agent, for an agent named name.
func StartRun(ctx context.Context, rec *Recorder, name,
	provider string) (context.Context, *Handle) {
	return rec.Start(ctx, opInvokeAgent+" "+name,
		slog.String(attrOperation, opInvokeAgent),
		slog.String(attrProvider, provider),
		slog.String(attrAgentName, name))
}

// errorType names an error for error.type: the API's status if it
// has one, otherwise a word for its kind.
func errorType(err error) string {
	type status interface {
		error
		StatusCode() int
	}
	if s, ok := errors.AsType[status](err); ok {
		return fmt.Sprint(s.StatusCode())
	}
	switch {
	case timedOut(err):
		return "timeout"
	case transient(err):
		return "connection"
	}
	return "_OTHER"
}
