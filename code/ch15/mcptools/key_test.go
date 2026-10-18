package mcptools

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/mcp"
)

// TestKeyMeta checks that a call's idempotency key crosses MCP: the
// client sends it in _meta, and a tool served with Offer reads it with
// agent.KeyFrom, as it would in the agent's own process.
func TestKeyMeta(t *testing.T) {
	var got []string
	served := agent.Tool{Def: agent.ToolDef{Name: "issue_file"},
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			k, _ := agent.KeyFrom(ctx)
			got = append(got, k)
			return "filed", nil
		}}
	c, _ := serve(t, &mcp.Server{
		Info:  mcp.Implementation{Name: "tracker", Version: "1"},
		Tools: Offer([]agent.Tool{served})})
	tools, err := Tools(t.Context(), c, Server{Name: "tracker"}, Names{})
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key("run-1", "call_1")
	for _, ctx := range []context.Context{
		agent.WithKey(t.Context(), key), t.Context()} {
		if _, err := tools[0].Run(ctx, jsontext.Value(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != key || got[1] != "" {
		t.Errorf("keys the server saw: %q, want [%s \"\"]", got, key)
	}
}

// TestKeyedDeclared checks that a tool is Keyed only if it declares,
// in its own _meta, that it honors keys, and only from a trusted
// server: a tool added to a server later doesn't inherit the claim.
func TestKeyedDeclared(t *testing.T) {
	run := func(context.Context, jsontext.Value) (string, error) {
		return "ok", nil
	}
	served := Offer([]agent.Tool{
		{Def: agent.ToolDef{Name: "issue_file"}, Keyed: true, Run: run},
		{Def: agent.ToolDef{Name: "issue_label"}, Run: run},
	})
	c, _ := serve(t, &mcp.Server{
		Info: mcp.Implementation{Name: "tracker", Version: "1"}, Tools: served})
	for _, trusted := range []bool{true, false} {
		tools, err := Tools(t.Context(), c, Server{Name: "tracker",
			Trusted: trusted}, Names{})
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools {
			want := trusted && tl.Def.Name == "tracker__issue_file"
			if tl.Keyed != want {
				t.Errorf("trusted %v: %s Keyed %v", trusted, tl.Def.Name,
					tl.Keyed)
			}
		}
	}
}

var update = flag.Bool("update", false, "rewrite the golden files")

// lineTap records each line written through it.
type lineTap struct {
	w     io.WriteCloser
	mu    *sync.Mutex
	lines *[]string
}

func (l lineTap) Write(p []byte) (int, error) {
	l.mu.Lock()
	for line := range strings.Lines(string(p)) {
		*l.lines = append(*l.lines, line)
	}
	l.mu.Unlock()
	return l.w.Write(p) //nolint:wrapcheck // a writer passes errors on
}

func (l lineTap) Close() error { return l.w.Close() } //nolint:wrapcheck // as Write

// TestKeyWire records the params of a keyed tools/call request as the
// client sends them, for the chapter: the key in _meta, beside the
// protocol's own keys.
func TestKeyWire(t *testing.T) {
	served := agent.Tool{Def: agent.ToolDef{Name: "issue_file"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "filed #1", nil
		}}
	s := &mcp.Server{Info: mcp.Implementation{Name: "tracker",
		Version: "1"}, Tools: Offer([]agent.Tool{served})}
	var mu sync.Mutex
	var lines []string
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() { _ = s.Serve(context.Background(), sr, sw) }()
	c, err := mcp.Connect(t.Context(), mcp.Implementation{Name: "triage",
		Version: "0.15"}, cr, lineTap{cw, &mu, &lines})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = sw.Close() })
	tools, err := Tools(t.Context(), c, Server{Name: "tracker"}, Names{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithKey(t.Context(), agent.Key("k1", "toolu_01"))
	args := jsontext.Value(`{"title":"Get panics"}`)
	if _, err := tools[0].Run(ctx, args); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var got []byte
	for _, line := range lines {
		var m struct {
			Method string         `json:"method"`
			Params jsontext.Value `json:"params"`
		}
		if json.Unmarshal([]byte(line), &m) == nil &&
			m.Method == "tools/call" {
			if err := m.Params.Indent(jsontext.WithIndent("  ")); err != nil {
				t.Fatal(err)
			}
			got = append(m.Params, '\n')
		}
	}
	path := filepath.Join("testdata", "keyed-call.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("request differs from %s; run go test -update:\n%s",
			path, got)
	}
}
