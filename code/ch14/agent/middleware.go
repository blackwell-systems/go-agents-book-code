package agent

import (
	"context"
	"errors"
	"iter"
	"slices"
)

// tag::middleware[]

// A ModelWare wraps a model: it takes the model a call goes to and
// returns one that does something before and after, such as a retry or
// a span. It takes and returns StreamModel, the interface the loop
// calls, so no wrapper can hide the stream (chapter 6's rule).
type ModelWare func(StreamModel) StreamModel

// A ToolWare wraps a tool. It sees the whole Tool, its definition and
// its safety flags as well as its Run, which is what a retry needs to
// know whether the tool is safe to run twice.
type ToolWare func(Tool) Tool

// WrapModel wraps m in ws, the first outermost: a call goes through
// ws[0], then ws[1], and so on, to m.
func WrapModel(m StreamModel, ws ...ModelWare) StreamModel {
	for _, w := range slices.Backward(ws) {
		m = w(m)
	}
	return m
}

// WrapTools wraps every tool in ws, the first outermost.
func WrapTools(tools []Tool, ws ...ToolWare) []Tool {
	out := slices.Clone(tools)
	for i := range out {
		for _, w := range slices.Backward(ws) {
			out[i] = w(out[i])
		}
	}
	return out
}

// end::middleware[]

// tag::stream-func[]

// StreamFunc is a function that is a StreamModel, as http.HandlerFunc
// is a Handler, so a wrapper can return a closure.
type StreamFunc func(ctx context.Context,
	req Request) iter.Seq2[Delta, error]

// Stream calls f.
func (f StreamFunc) Stream(ctx context.Context,
	req Request) iter.Seq2[Delta, error] {
	return f(ctx, req)
}

// end::stream-func[]

// Generator turns a StreamModel into a Model that collects each reply
// whole, so a wrapped model can serve calls that don't stream, such as
// chapter 8's compaction summary, and get the same retries.
func Generator(m StreamModel) Model { return generator{m} }

type generator struct{ m StreamModel }

func (g generator) Generate(ctx context.Context, req Request) (Reply,
	error) {
	return Collect(g.m.Stream(ctx, req))
}

// errStopped is what an attempt returns when the caller stopped
// reading: nothing to retry, and nothing more to yield.
var errStopped = errors.New("agent: the caller stopped reading")
