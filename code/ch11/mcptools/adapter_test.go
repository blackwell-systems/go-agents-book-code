package mcptools

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch11/mcp"
)

// serve connects a client to s in this process, and returns the client
// and a function that kills the server, as a crash would.
func serve(t *testing.T, s *mcp.Server) (*mcp.Client, func()) {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() { _ = s.Serve(context.Background(), sr, sw) }()
	c, err := mcp.Connect(t.Context(), mcp.Implementation{Name: "test"}, cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, func() { _ = sw.Close() }
}

func echoServer() *mcp.Server {
	echo := func(_ context.Context, args jsontext.Value) (string, error) {
		return string(args), nil
	}
	return &mcp.Server{Info: mcp.Implementation{Name: "anything", Version: "1"},
		Tools: []mcp.Tool{
			{Name: "search", Description: "Search.\u200b", ReadOnly: true, Call: echo,
				InputSchema: jsontext.Value(`{"type":"object","properties":{"q":{"type":"string","description":"words\u202e"}}}`)},
			{Name: "file", Description: "File.", Call: func(context.Context, jsontext.Value) (string, error) {
				return "", errors.New("the title is empty")
			}},
		}}
}

// TestTools checks what an agent tool made from a server's holds: the
// prefixed name, the labeled description, the cleaned schema, no strict
// mode, and safety flags only from a trusted server.
func TestTools(t *testing.T) {
	c, _ := serve(t, echoServer())
	for _, trusted := range []bool{false, true} {
		tools, err := Tools(t.Context(), c, Server{Name: "s", Trusted: trusted}, Names{})
		if err != nil {
			t.Fatal(err)
		}
		search := tools[0]
		if search.Def.Name != "s__search" || search.Def.Strict ||
			!strings.HasPrefix(search.Def.Description, `[The MCP server "s"`) ||
			strings.Contains(search.Def.Description, "\u200b") ||
			strings.Contains(string(search.Def.Parameters), "\u202e") {
			t.Errorf("search: %+v", search.Def)
		}
		if search.ReadOnly != trusted || tools[1].ReadOnly {
			t.Errorf("trusted %v: read-only %v, %v", trusted, search.ReadOnly, tools[1].ReadOnly)
		}
	}
}

// TestToolsRefused checks pins: with every tool pinned the tools come
// through; a tool not pinned, or changed since, stops Tools with
// ErrUnreviewed. A name taken by the agent's own tools stops it too.
func TestToolsRefused(t *testing.T) {
	c, _ := serve(t, echoServer())
	pins, err := Digests(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := Tools(t.Context(), c, Server{Name: "s", Pins: pins}, Names{})
	if err != nil || len(tools) != 2 {
		t.Errorf("all pinned: %v, %v", tools, err)
	}
	delete(pins, "file")
	if _, err := Tools(t.Context(), c, Server{Name: "s", Pins: pins}, Names{}); !errors.Is(err, ErrUnreviewed) {
		t.Errorf("a tool not pinned: %v", err)
	}
	pins["file"] = strings.Repeat("0", 64)
	if _, err := Tools(t.Context(), c, Server{Name: "s", Pins: pins}, Names{}); !errors.Is(err, ErrUnreviewed) {
		t.Errorf("a tool changed: %v", err)
	}
	names := Names{}
	_ = names.Reserve("s__file")
	if _, err := Tools(t.Context(), c, Server{Name: "s"}, names); err == nil {
		t.Error("a server tool took one of the agent's names")
	}
}

// TestDeadServer checks that a tool error comes back as an error the
// model reads, and that a server process that dies in the middle of a
// run stops it with ErrToolFatal, quoting the server's last words,
// instead of letting the model try again.
func TestDeadServer(t *testing.T) {
	c, err := mcp.Start(t.Context(), mcp.Command{Path: os.Args[0],
		Env: mcp.BaseEnv("MCPTOOLS_TEST_SERVER=1")}, mcp.Implementation{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := Tools(t.Context(), c, Server{Name: "s", Trusted: true}, Names{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]agent.Tool{}
	for _, tl := range tools {
		byName[tl.Def.Name] = tl
	}
	if _, err := byName["s__file"].Run(t.Context(), nil); err == nil ||
		errors.Is(err, agent.ErrToolFatal) || err.Error() != "the title is empty" {
		t.Errorf("a tool error: %v", err)
	}
	model := &agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("1", "s__search", `{"q":"x"}`)),
		agent.Calls(agent.Call("2", "s__crash", `{}`)),
		agent.Calls(agent.Call("3", "s__search", `{"q":"x"}`)),
		agent.Says("done"),
	}}
	_, err = agent.Run(t.Context(), model, tools, []agent.Message{
		{Role: "user", Content: "search"}}, agent.Limits{MaxTurns: 5})
	if !errors.Is(err, agent.ErrToolFatal) || !errors.Is(err, mcp.ErrServerGone) ||
		!strings.Contains(err.Error(), "out of memory") {
		t.Errorf("run with a server that died: %v", err)
	}
}

// TestMain runs the test binary as echoServer, with a tool that kills
// it, when MCPTOOLS_TEST_SERVER is set.
func TestMain(m *testing.M) {
	if os.Getenv("MCPTOOLS_TEST_SERVER") == "" {
		os.Exit(m.Run())
	}
	s := echoServer()
	s.Tools = append(s.Tools, mcp.Tool{Name: "crash",
		Call: func(context.Context, jsontext.Value) (string, error) {
			fmt.Fprintln(os.Stderr, "server: out of memory, giving up")
			os.Exit(3)
			return "", nil
		}})
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}

// TestResultCapped checks that a server's result reaches the model with
// its hidden characters removed and cut to maxResult.
func TestResultCapped(t *testing.T) {
	c, _ := serve(t, &mcp.Server{Info: mcp.Implementation{Name: "big", Version: "1"},
		Tools: []mcp.Tool{{Name: "dump", Call: func(context.Context, jsontext.Value) (string, error) {
			return "a\u200bb" + strings.Repeat("x", 2*maxResult), nil
		}}}})
	tools, err := Tools(t.Context(), c, Server{Name: "big"}, Names{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tools[0].Run(t.Context(), nil)
	if err != nil || !strings.HasPrefix(out, "abx") || len(out) > maxResult+10 ||
		!strings.HasSuffix(out, "[...]") {
		t.Errorf("result: %d bytes, starts %q, %v", len(out), out[:5], err)
	}
}
