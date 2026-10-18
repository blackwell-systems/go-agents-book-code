package mcp

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"os"
	"testing"
)

var echoArgs = jsontext.Value(`{"text":"hello"}`)

// BenchmarkCall measures one tools/call three ways: to a server in its
// own process, over pipes to a server in this process, and the tool's
// function called directly, which is what a built-in tool costs.
func BenchmarkCall(b *testing.B) {
	b.Run("process", func(b *testing.B) {
		c, err := Start(b.Context(), Command{Path: os.Args[0],
			Env: BaseEnv("MCP_TEST_SERVER=1")}, Implementation{Name: "bench"})
		if err != nil {
			b.Fatal(err)
		}
		defer c.Close()
		benchCalls(b, c)
	})
	b.Run("pipe", func(b *testing.B) {
		benchCalls(b, pairB(b))
	})
	b.Run("direct", func(b *testing.B) {
		call := testServer().Tools[0].Call
		for b.Loop() {
			if _, err := call(context.Background(), echoArgs); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchCalls(b *testing.B, c *Client) {
	for b.Loop() {
		if _, err := c.CallTool(b.Context(), "echo", echoArgs); err != nil {
			b.Fatal(err)
		}
	}
}

// pairB is pair for a benchmark.
func pairB(b *testing.B) *Client {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() {
		_ = testServer().Serve(context.Background(), sr, sw)
		_ = sw.Close()
	}()
	c, err := Connect(b.Context(), Implementation{Name: "bench"}, cr, cw)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	return c
}
