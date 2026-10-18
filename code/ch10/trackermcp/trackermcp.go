// Package trackermcp is the book's issue tracker as an MCP server: the
// triage agent's four issue tools, served over the protocol by hand
// (chapter 10).
package trackermcp

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch10/mcp"
	"github.com/blackwell-systems/go-agents-book/code/ch10/mcptools"
	"github.com/blackwell-systems/go-agents-book/code/ch10/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::serve[]

// Serve serves the issues kept in file over r and w until r ends, with
// its log lines on logw. The tools are chapter 7's, built by the same
// registry, so their schemas are the ones the agent sent the model.
func Serve(ctx context.Context, file string, now func() time.Time,
	r io.Reader, w, logw io.Writer) error {
	issues, err := tracker.Open(file, now)
	if err != nil {
		return fmt.Errorf("tracker server: %w", err)
	}
	s := &mcp.Server{
		Info: mcp.Implementation{Name: "tracker", Version: "0.10"},
		Instructions: "The issue tracker for remind: search, read, " +
			"file, and comment on issues.",
		Tools: mcptools.Offer(triage.IssueTools(issues)),
		Log:   log.New(logw, "tracker: ", 0),
	}
	s.Log.Printf("serving %d tools, issues in %s", len(s.Tools), file)
	if err := s.Serve(ctx, r, w); err != nil {
		return fmt.Errorf("tracker server: %w", err)
	}
	return nil
}

// end::serve[]
