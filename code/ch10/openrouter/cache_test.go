package openrouter

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch10/agent"
)

// TestCachePointWire checks that a cache point is sent as a text part
// with a cache_control marker, and other messages as plain strings.
func TestCachePointWire(t *testing.T) {
	c := &Client{Model: "m"}
	wire := c.wireRequest(agent.Request{Messages: []agent.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "task", CachePoint: true},
	}})
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"role":"system","content":"rules"}`,
		`{"role":"user","content":[{"type":"text","text":"task",` +
			`"cache_control":{"type":"ephemeral"}}]}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("request lacks %s:\n%s", want, b)
		}
	}
}

// TestEmptyCachePoint checks that a cache point with no text, such as
// an assistant message that only calls tools, sends no empty text part.
func TestEmptyCachePoint(t *testing.T) {
	c := &Client{Model: "m"}
	wire := c.wireRequest(agent.Request{Messages: []agent.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", CachePoint: true, ToolCalls: []agent.ToolCall{
			agent.Call("c1", "crash_list", `{}`)}},
	}})
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"text":""`) {
		t.Errorf("request sends an empty text part:\n%s", b)
	}
	if !strings.Contains(string(b), `{"role":"assistant","tool_calls":`) {
		t.Errorf("assistant message isn't sent as before:\n%s", b)
	}
}

// TestResponseFormatWire checks the json_schema wrapper, and that a
// constrained reply asks for providers that support it.
func TestResponseFormatWire(t *testing.T) {
	c := &Client{Model: "m"}
	wire := c.wireRequest(agent.Request{
		Messages:       []agent.Message{{Role: "user", Content: "x"}},
		ResponseFormat: jsontext.Value(`{"type":"object"}`)})
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"response_format":{"type":"json_schema",` +
		`"json_schema":{"name":"reply","strict":true,"schema":{"type":"object"}}}`,
		`"provider":{"require_parameters":true}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("request lacks %s:\n%s", want, b)
		}
	}
}

// TestContentAndCacheUsage checks the forms a reply's content can take,
// and that cache writes are read from usage.
func TestContentAndCacheUsage(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`"hello"`, "hello"}, {`null`, ""},
		{`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab"},
	} {
		var c content
		if err := json.Unmarshal([]byte(tc.in), &c); err != nil || c.Text != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.in, c.Text, err, tc.want)
		}
	}
	var u chatUsage
	err := json.Unmarshal([]byte(`{"prompt_tokens": 900, "prompt_tokens_details":
		{"cached_tokens": 600, "cache_write_tokens": 300}}`), &u)
	if err != nil {
		t.Fatal(err)
	}
	if got := toUsage(u); got.CachedTokens != 600 || got.CacheWriteTokens != 300 {
		t.Errorf("usage = %+v", got)
	}
}
