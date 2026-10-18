package mcp

import (
	"context"
	"encoding/json/jsontext"
	"maps"
)

// tag::meta[]

type metaKey struct{}

// WithMeta returns a context whose tools/call requests carry key, with
// value, in their _meta. The key needs a prefix of the implementation's
// own, in reverse domain order, such as com.example.tracker/; prefixes
// whose second label is modelcontextprotocol or mcp are reserved.
func WithMeta(ctx context.Context, key string,
	value jsontext.Value) context.Context {
	m := maps.Clone(metaFrom(ctx))
	if m == nil {
		m = map[string]jsontext.Value{}
	}
	m[key] = value
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom returns the value of key in the _meta of the request a tool
// is running for, on the server's side, or nil if it has none.
func MetaFrom(ctx context.Context, key string) jsontext.Value {
	return metaFrom(ctx)[key]
}

// end::meta[]

func metaFrom(ctx context.Context) map[string]jsontext.Value {
	m, _ := ctx.Value(metaKey{}).(map[string]jsontext.Value)
	return m
}

// withMeta is the server's side: the request's extra _meta keys, for
// the tool to read with MetaFrom.
func withMeta(ctx context.Context,
	extra map[string]jsontext.Value) context.Context {
	if len(extra) == 0 {
		return ctx
	}
	return context.WithValue(ctx, metaKey{}, extra)
}

// withExtra adds the context's extra _meta keys to a request's _meta.
// A server of an earlier version gets them on their own.
func withExtra(ctx context.Context, meta *requestMeta) *requestMeta {
	extra := metaFrom(ctx)
	if len(extra) == 0 {
		return meta
	}
	m := requestMeta{}
	if meta != nil {
		m = *meta
	}
	m.Extra = extra
	return &m
}
