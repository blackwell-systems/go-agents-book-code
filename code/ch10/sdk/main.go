// Command sdk serves two of the book's issue tracker tools over MCP
// with the MCP Go SDK, enough to show the shape of an SDK server next
// to chapter 10's hand-written one. It is its own module, so the book's
// main module stays free of dependencies through Part 2.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tag::sdk-args[]

type searchArgs struct {
	Phrase string `json:"phrase" jsonschema:"text to find"`
}

type fileArgs struct {
	Title       string `json:"title" jsonschema:"the problem in a line"`
	Body        string `json:"body" jsonschema:"cause, evidence, fix"`
	Fingerprint string `json:"fingerprint" jsonschema:"the group's"`
}

// end::sdk-args[]

// tag::sdk-server[]

// newServer offers two of the tracker's tools. AddTool generates each
// tool's schema from its arguments' type and checks every call against
// it; an error from a handler reaches the client as a result with
// isError set.
func newServer(issues *tracker.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "tracker",
		Version: "0.10"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "issue_search",
		Description: "List the open issues that contain a phrase.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		search(issues))
	mcp.AddTool(s, &mcp.Tool{Name: "issue_file",
		Description: "File a new issue for a crash group."},
		file(issues))
	return s
}

// end::sdk-server[]

// tag::sdk-handlers[]

func search(issues *tracker.Store) mcp.ToolHandlerFor[searchArgs, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest,
		a searchArgs) (*mcp.CallToolResult, any, error) {
		var b strings.Builder
		q := tracker.Query{Text: a.Phrase, Status: "open"}
		for _, is := range issues.Search(q) {
			fmt.Fprintf(&b, "#%d %s\n", is.ID, is.Title)
		}
		found := cmp.Or(b.String(), "no open issue matches")
		return text(found), nil, nil
	}
}

func file(issues *tracker.Store) mcp.ToolHandlerFor[fileArgs, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest,
		a fileArgs) (*mcp.CallToolResult, any, error) {
		is, err := issues.Create(a.Title, a.Body, []string{"crash"},
			a.Fingerprint)
		if err != nil {
			return nil, nil, fmt.Errorf("issue_file: %w", err)
		}
		return text(fmt.Sprintf("filed #%d", is.ID)), nil, nil
	}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// end::sdk-handlers[]

// tag::sdk-main[]

func main() {
	data := flag.String("data", "issues.json",
		"file the issues are kept in")
	flag.Parse()
	log.SetOutput(os.Stderr) // standard output carries only messages
	issues, err := tracker.Open(*data, time.Now)
	if err != nil {
		log.Fatal(err)
	}
	s := newServer(issues)
	err = s.Run(context.Background(), &mcp.StdioTransport{})
	if err != nil {
		log.Fatal(err)
	}
}

// end::sdk-main[]
