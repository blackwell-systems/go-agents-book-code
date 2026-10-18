// Package triage is the crash-triage agent's job: remind's crash
// reports, the tools for investigating and fixing them, and the
// structured diagnosis the agent ends with.
package triage

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"os"
	"time"
)

// tag::report[]

// A Report is one crash report, as remind's crash package writes it:
// a recovered panic, or a request that got stuck, with what was going
// on at the time. Count reports how many times the same crash happened
// between FirstSeen and LastSeen.
type Report struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"` // the build that crashed
	Commit    string    `json:"commit"`
	Kind      string    `json:"kind"`  // "panic" or "stuck"
	Panic     string    `json:"panic"` // the panic value, as text
	Stack     string    `json:"stack"` // a stack trace, or a goroutine dump
	Request   Request   `json:"request"`
	Logs      []string  `json:"logs"` // log lines from just before
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Count     int       `json:"count"`
}

// A Request is the HTTP request being served when remind crashed.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// end::report[]

// LoadReports reads crash reports from a JSON Lines file, one report
// per line.
func LoadReports(file string) ([]Report, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("load reports: %w", err)
	}
	var out []Report
	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r Report
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("load reports: %s: %w", file, err)
		}
		out = append(out, r)
	}
	return out, nil
}
