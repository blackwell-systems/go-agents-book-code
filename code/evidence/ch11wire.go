package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agent11 "github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch11/anthropic"
	openrouter11 "github.com/blackwell-systems/go-agents-book/code/ch11/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch11/orsdk"
)

// Chapter 11's wire comparison (E2): the last request of the recorded
// m1 run, as the agent held it, encoded by each of the chapter's three
// clients; and the stream events per reply in each API's recordings.

// wireSummary prints both tables.
func wireSummary(w io.Writer, dir string) error {
	replies := filepath.Join(dir, "..", "..", "ch11", "testdata", "replies")
	req, err := lastRequest(replies, "m1-done-after-delete")
	if err != nil || req == nil {
		return err
	}
	fmt.Fprintln(w, "== wire: the recorded m1 run's last request, "+
		"encoded by each client (bytes)")
	fmt.Fprintln(w, "client\tsystem prompt\ttools\tconversation\t"+
		"of it, reasoning\ttotal")
	for _, c := range []struct {
		name string
		send func(*http.Client, string) error
	}{
		{"openrouter (chapter 2)", func(h *http.Client, url string) error {
			c := &openrouter11.Client{BaseURL: url, Model: "m", HTTP: h}
			_, err := c.Generate(context.Background(), *req)
			return err //nolint:wrapcheck // only the request matters
		}},
		{"orsdk (the SDK)", func(h *http.Client, url string) error {
			_, err := orsdk.New("k", "m", url, h).Generate(
				context.Background(), *req)
			return err //nolint:wrapcheck // only the request matters
		}},
		{"anthropic (Messages API)", func(h *http.Client, url string) error {
			c := &anthropic.Client{BaseURL: url, Model: "m", HTTP: h}
			_, err := c.Generate(context.Background(), *req)
			return err //nolint:wrapcheck // only the request matters
		}},
	} {
		body := capture(c.send)
		fmt.Fprintf(w, "%s\t%s\n", c.name, partSizes(body))
	}
	fmt.Fprintln(w)
	return eventSummary(w, replies)
}

// capture runs send against a local server and returns the request
// body it sent; the server's answer doesn't matter.
func capture(send func(*http.Client, string) error) []byte {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			http.Error(w, `{"error":{"message":"captured"}}`,
				http.StatusBadRequest)
		}))
	defer srv.Close()
	_ = send(srv.Client(), srv.URL) // the 400 is expected
	return body
}

// partSizes splits a request body into its parts, in either format.
func partSizes(body []byte) string {
	var r struct {
		System   jsontext.Value   `json:"system"`
		Tools    jsontext.Value   `json:"tools"`
		Messages []jsontext.Value `json:"messages"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "unreadable"
	}
	system, conv, reasoning := len(r.System), 0, 0
	for _, m := range r.Messages {
		var role struct {
			Role      string         `json:"role"`
			Reasoning jsontext.Value `json:"reasoning_details"`
		}
		_ = json.Unmarshal(m, &role)
		if role.Role == "system" {
			system += len(m)
			continue
		}
		conv += len(m)
		reasoning += len(role.Reasoning)
	}
	return fmt.Sprintf("%d\t%d\t%d\t%d\t%d", system, len(r.Tools), conv,
		reasoning, len(body))
}

// lastRequest reads a recorded script's last request and turns it back
// into the agent's request, as the loop held it.
func lastRequest(replies, script string) (*agent11.Request, error) {
	files, err := filepath.Glob(filepath.Join(replies, script+"-*.json"))
	if err != nil {
		return nil, fmt.Errorf("wire: %w", err)
	}
	files = slices.DeleteFunc(files, func(f string) bool {
		m := callNumber.FindStringSubmatch(filepath.Base(f))
		return m == nil || m[1] != script // another script's calls
	})
	if len(files) == 0 {
		return nil, nil
	}
	slices.SortFunc(files, func(a, b string) int {
		return callIndex(a) - callIndex(b)
	})
	rec, err := readRecording(files[len(files)-1])
	if err != nil {
		return nil, err
	}
	return rec.Request.agent11Request()
}

// agent11Request is agentRequest for chapter 11's types.
func (r savedRequest) agent11Request() (*agent11.Request, error) {
	out := &agent11.Request{MaxTokens: r.MaxTokens}
	for _, m := range r.Messages {
		text, err := contentText(m.Content)
		if err != nil {
			return nil, err
		}
		am := agent11.Message{Role: m.Role, Content: text,
			ToolCallID: m.ToolCallID}
		if d := m.ReasoningDetails; len(d) > 0 && d.Kind() == '[' {
			am.ReasoningDetails = slices.Clone(d)
		}
		for _, c := range m.ToolCalls {
			am.ToolCalls = append(am.ToolCalls, agent11.ToolCall{ID: c.ID,
				Name:      c.Function.Name,
				Arguments: jsontext.Value(c.Function.Arguments)})
		}
		out.Messages = append(out.Messages, am)
	}
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, agent11.ToolDef{Name: t.Function.Name,
			Description: t.Function.Description,
			Parameters:  slices.Clone(t.Function.Parameters),
			Strict:      t.Function.Strict})
	}
	return out, nil
}

// eventSummary counts the stream events of each recorded reply, per
// API: chat completions chunks and Messages API events.
func eventSummary(w io.Writer, replies string) error {
	rows := []struct{ name, script string }{
		{"chat completions (openrouter)", "m1-done-after-delete"},
		{"Messages API (anthropic)", "m1-done-after-delete-anthropic"},
	}
	printed := false
	for _, r := range rows {
		files, err := filepath.Glob(filepath.Join(replies, r.script+"-*.json"))
		if err != nil {
			return fmt.Errorf("events: %w", err)
		}
		var counts []int
		for _, f := range files {
			if m := callNumber.FindStringSubmatch(filepath.Base(f)); m == nil ||
				m[1] != r.script {
				continue // another script whose name starts the same
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return fmt.Errorf("events: %w", err)
			}
			var rec struct {
				Stream string `json:"response_text"`
			}
			if json.Unmarshal(b, &rec) != nil || rec.Stream == "" {
				continue
			}
			counts = append(counts, bytes.Count([]byte(rec.Stream),
				[]byte("\ndata:"))+boolInt(strings.HasPrefix(rec.Stream, "data:")))
		}
		if len(counts) == 0 {
			continue
		}
		if !printed {
			fmt.Fprintln(w, "== events: stream events per recorded reply "+
				"of m1-done-after-delete")
			fmt.Fprintln(w, "API\treplies\tevents (median)\tevents (total)")
			printed = true
		}
		total := 0
		for _, c := range counts {
			total += c
		}
		fmt.Fprintf(w, "%s\t%d\t%v\t%d\n", r.name, len(counts),
			median(counts), total)
	}
	if printed {
		fmt.Fprintln(w)
	}
	return nil
}
