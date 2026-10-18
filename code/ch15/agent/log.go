package agent

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"iter"
	"log/slog"
	"runtime/debug"
	"time"
)

type agentKey struct{}

// WithName returns a context that names the agent a run is for, such
// as a fingerprint group's fingerprint; a sub-agent's name is its parent's
// with its own added, so its costs roll up into the parent's.
func WithName(ctx context.Context, name string) context.Context {
	if parent := NameFrom(ctx); parent != "" {
		name = parent + "/" + name
	}
	return context.WithValue(ctx, agentKey{}, name)
}

// NameFrom returns the agent's name from the context, or "".
func NameFrom(ctx context.Context) string {
	name, _ := ctx.Value(agentKey{}).(string)
	return name
}

// tag::log-options[]

// A LogOptions says what Logged writes: metadata always, and the
// content of requests and results only when Content is set, cut to
// Limit bytes and passed through Redact first.
type LogOptions struct {
	Content bool
	Limit   int                 // bytes per field; 0 means 500
	Redact  func(string) string // nil means none
}

// end::log-options[]

// tag::logged-tools[]

// LoggedTools returns a ToolWare that writes a line for every tool
// call, with the agent, the tool, the call, how long it took, and any
// error, and the stack of a panic before it reaches the loop's
// recovery, which keeps the stack from the model.
func LoggedTools(log *slog.Logger, o LogOptions) ToolWare {
	return func(t Tool) Tool {
		run := t.Run
		t.Run = func(ctx context.Context,
			args jsontext.Value) (out string, err error) {
			call, _ := CallFrom(ctx)
			attrs := []any{"agent", NameFrom(ctx), "tool", t.Def.Name,
				"call", call.ID}
			start := time.Now()
			defer func() {
				if p := recover(); p != nil { // <1>
					log.ErrorContext(ctx, "tool panicked", append(attrs,
						"panic", fmt.Sprint(p),
						"stack", string(debug.Stack()))...)
					panic(p)
				}
				attrs = append(attrs, "took", time.Since(start))
				o.logTool(ctx, log, attrs, string(args), out, err)
			}()
			return run(ctx, args)
		}
		return t
	}
}

// end::logged-tools[]

// logTool writes the line for a tool call that returned.
func (o LogOptions) logTool(ctx context.Context, log *slog.Logger,
	attrs []any, args, out string, err error) {
	if o.Content {
		attrs = append(attrs, "args", o.text(args), "result", o.text(out))
	}
	if err != nil {
		log.WarnContext(ctx, "tool failed",
			append(attrs, "err", o.text(err.Error()))...)
		return
	}
	log.InfoContext(ctx, "tool", attrs...)
}

// tag::logged[]

// Logged returns a ModelWare that writes a line for every model call:
// the agent, the model, the tokens and cost, and any error.
func Logged(log *slog.Logger, model string) ModelWare {
	return func(m StreamModel) StreamModel {
		return StreamFunc(func(ctx context.Context,
			req Request) iter.Seq2[Delta, error] {
			return func(yield func(Delta, error) bool) {
				start := time.Now()
				var u Usage
				var err error
				defer func() {
					log.InfoContext(ctx, "model call", "agent",
						NameFrom(ctx), "model", model,
						"took", time.Since(start),
						"in", u.PromptTokens, "cached", u.CachedTokens,
						"out", u.CompletionTokens, "cost", u.Cost,
						"err", err)
				}()
				for d, e := range m.Stream(ctx, req) {
					if d.Usage != nil {
						u = *d.Usage
					}
					if err = e; !yield(d, e) {
						return
					}
				}
			}
		})
	}
}

// end::logged[]

// text prepares content for a log line: redacted, then cut.
func (o LogOptions) text(s string) string {
	if o.Redact != nil {
		s = o.Redact(s)
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 500
	}
	if len(s) > limit {
		s = s[:limit] + fmt.Sprintf("... (%d more bytes)", len(s)-limit)
	}
	return s
}
