package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/mcp"
	"github.com/blackwell-systems/go-agents-book/code/ch15/mcptools"
	"github.com/blackwell-systems/go-agents-book/code/ch15/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch15/triage"
)

// tag::start-tracker[]

// startTracker starts the tracker's MCP server, this same program run
// with -serve-tracker, on the job's issues. It gets an environment with
// nothing in it but what a program needs to start, so not the API key.
func startTracker(ctx context.Context, j *taskrun.Job) (
	*mcp.Client, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("start the tracker: %w", err)
	}
	cmd := mcp.Command{Path: self, Env: mcp.BaseEnv(),
		Args: []string{"-serve-tracker", j.IssuesFile()}}
	c, err := mcp.Start(ctx, cmd,
		mcp.Implementation{Name: "triage", Version: "0.10"})
	if err != nil {
		return nil, fmt.Errorf("start the tracker: %w", err)
	}
	return c, nil
}

// end::start-tracker[]

// tag::tracker-tools[]

// trackerTools replaces the agent's own issue tools with the tracker
// server's. The server is the book's own, so it is trusted: its
// read-only hints become safety flags. After a call that may have filed
// or commented, the job reads the issues again, since submit_diagnosis
// and the grade check them in this process.
func trackerTools(ctx context.Context, c *mcp.Client, j *taskrun.Job,
	tools []agent.Tool, pins mcptools.Pins) ([]agent.Tool, error) {
	own := triage.Without(tools, triage.IssueNames...)
	names := mcptools.Names{}
	for _, t := range own {
		if err := names.Reserve(t.Def.Name); err != nil {
			return nil, fmt.Errorf("the tracker's tools: %w", err)
		}
	}
	reload := func(context.Context, string) error {
		return j.ReloadIssues()
	}
	server := mcptools.Server{Name: "tracker", Trusted: true,
		Pins: pins, After: reload}
	served, err := mcptools.Tools(ctx, c, server, names)
	if err != nil {
		return nil, fmt.Errorf("the tracker's tools: %w", err)
	}
	for _, t := range served {
		own = append(own, triage.Redacted(t))
	}
	return own, nil
}

// end::tracker-tools[]

// renameIssueTools gives a prompt the server's names for the tracker
// tools, which the model sees with the server's prefix.
func renameIssueTools(prompt string) string {
	for _, n := range triage.IssueNames {
		prompt = strings.ReplaceAll(prompt, n,
			mcptools.ToolName("tracker", n))
	}
	return prompt
}

// tag::print-digests[]

// printDigests starts the tracker's server on no issues and prints its
// tools' digests as JSON, for a person to save, after reading the
// tools, and pass back with -tracker-pins.
func printDigests(ctx context.Context, w io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("print digests: %w", err)
	}
	empty := filepath.Join(os.TempDir(), "no-issues.json") // not created
	c, err := mcp.Start(ctx, mcp.Command{Path: self,
		Args: []string{"-serve-tracker", empty}, Env: mcp.BaseEnv()},
		mcp.Implementation{Name: "triage", Version: "0.10"})
	if err != nil {
		return fmt.Errorf("print digests: %w", err)
	}
	defer c.Close()
	pins, err := mcptools.Digests(ctx, c)
	if err != nil {
		return fmt.Errorf("print digests: %w", err)
	}
	b, err := json.Marshal(pins, jsontext.Multiline(true),
		json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("print digests: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\n", b); err != nil {
		return fmt.Errorf("print digests: %w", err)
	}
	return nil
}

// end::print-digests[]

// loadPins reads the digests -print-digests wrote; "" means no pins.
func loadPins(file string) (mcptools.Pins, error) {
	if file == "" {
		return nil, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("tracker pins: %w", err)
	}
	var pins mcptools.Pins
	if err := json.Unmarshal(b, &pins); err != nil {
		return nil, fmt.Errorf("tracker pins %s: %w", file, err)
	}
	return pins, nil
}
