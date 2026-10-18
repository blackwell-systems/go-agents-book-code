package main

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"strings"
	"testing"
	"time"

	handmcp "github.com/blackwell-systems/go-agents-book/code/ch10/mcp"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHandClientSDKServer connects chapter 10's hand-written client to
// the SDK server: the client's server/discover probe finds a server of
// the current version, and the tools work.
func TestHandClientSDKServer(t *testing.T) {
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = newServer(issues).Run(ctx, &mcp.IOTransport{Reader: sr, Writer: sw})
	}()
	c, err := handmcp.Connect(ctx, handmcp.Implementation{Name: "hand", Version: "1"}, cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Legacy() || c.Server().Name != "tracker" {
		t.Errorf("legacy %v, server %+v", c.Legacy(), c.Server())
	}
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools %+v, %v", tools, err)
	}
	for _, tl := range tools { // the SDK lists tools sorted by name
		if tl.Name == "issue_file" {
			t.Logf("the schema the SDK generated for issue_file: %s", tl.InputSchema)
		}
	}
	r, err := c.CallTool(ctx, "issue_file", jsontext.Value(
		`{"title":"store.Get returns a deleted reminder","body":"...","fingerprint":"fp_4828a6"}`))
	if err != nil || r.IsError || r.Text() != "filed #1" {
		t.Errorf("issue_file = %+v, %v", r, err)
	}
	r, err = c.CallTool(ctx, "issue_search", jsontext.Value(`{"phrase":"deleted"}`))
	if err != nil || r.Text() != "#1 store.Get returns a deleted reminder\n" {
		t.Errorf("issue_search = %q, %v", r.Text(), err)
	}
	r, err = c.CallTool(ctx, "issue_file", jsontext.Value(`{"title":3}`))
	if err != nil || !r.IsError {
		t.Errorf("bad arguments = %+v, %v", r, err)
	}
	t.Logf("the SDK's answer to bad arguments: %s", r.Text())
}

// TestSDKClientHandServer connects the SDK's client to chapter 10's
// hand-written server.
func TestSDKClientHandServer(t *testing.T) {
	s := &handmcp.Server{Info: handmcp.Implementation{Name: "hand", Version: "1"},
		Tools: []handmcp.Tool{{Name: "echo", Description: "Echo the text.",
			InputSchema: jsontext.Value(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			Call: func(_ context.Context, args jsontext.Value) (string, error) {
				return string(args), nil
			}}}}
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() {
		_ = s.Serve(context.Background(), sr, sw)
		_ = sw.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo",
		Arguments: map[string]any{"text": "hi"}})
	if err != nil || res.IsError {
		t.Fatalf("echo = %+v, %v", res, err)
	}
	if got := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, `"hi"`) {
		t.Errorf("echo = %s", got)
	}
}
