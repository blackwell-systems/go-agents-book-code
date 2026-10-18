package mcptools

import (
	"context"
	"encoding/json/jsontext"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/mcp"
)

// tag::key-meta[]

// KeyMeta is the _meta key under which the book's MCP client sends a
// call's idempotency key and the book's servers read it (chapter 15).
// MCP defines no such key, so it carries a prefix of the book's own.
const KeyMeta = "com.example.agent/idempotencyKey"

// withKey puts the call's idempotency key, if it has one, into the
// tools/call request's _meta.
func withKey(ctx context.Context) context.Context {
	key, ok := agent.KeyFrom(ctx)
	if !ok {
		return ctx
	}
	v, err := jsontext.AppendQuote(nil, key)
	if err != nil {
		return ctx // a key that isn't valid UTF-8 isn't sent
	}
	return mcp.WithMeta(ctx, KeyMeta, v)
}

// end::key-meta[]

// tag::keyed[]

// keyed gives a served tool the idempotency key its request carried,
// as agent.KeyFrom reads it, so the tool behaves as it would in the
// agent's own process.
func keyed(run func(context.Context, jsontext.Value) (string,
	error)) func(context.Context, jsontext.Value) (string, error) {
	return func(ctx context.Context, args jsontext.Value) (string,
		error) {
		if v := mcp.MetaFrom(ctx, KeyMeta); v != nil {
			if key, err := jsontext.AppendUnquote(nil, v); err == nil {
				ctx = agent.WithKey(ctx, string(key))
			}
		}
		return run(ctx, args)
	}
}

// end::keyed[]

// keyedMeta declares, in a served tool's _meta, that it honors the key
// sent under KeyMeta, which a client believes only from a trusted
// server. A server owner declares it per tool: a tool added later
// doesn't inherit it.
func keyedMeta(t agent.Tool) map[string]jsontext.Value {
	if !t.Keyed {
		return nil
	}
	return map[string]jsontext.Value{KeyMeta: jsontext.Value("true")}
}

// declaresKeyed reports whether a server's tool says it honors keys.
func declaresKeyed(info mcp.ToolInfo) bool {
	return string(info.Meta[KeyMeta]) == "true"
}
