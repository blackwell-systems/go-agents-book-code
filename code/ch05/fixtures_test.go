package main

import (
	"bytes"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The saved runs fix and fix-again are recorded from the live API with
// -record. The rest of testdata/replies are hand-written replies in
// OpenRouter's format, recorded through the real client and tools, until
// they are recorded from the live API with -record. Run with -fixtures
// to write them again after changing the scripts below.
var makeFixtures = flag.Bool("fixtures", false,
	"rewrite the hand-written saved runs in testdata/replies")

// tag::turn[]

// A turn is one scripted assistant reply, or an error in its place.
type turn struct {
	text   string
	calls  [][2]string // tool name and arguments
	finish string      // the finish reason, if not the usual one
	sameID bool        // give every call in the reply the same ID
	status int         // an HTTP error status instead of a reply
	errMsg string      // the error message for status
}

var scripts = map[string][]turn{
	// One script per way a run can stop, for chapter 5's tests.
	"dup-id": {
		{calls: [][2]string{{"read_file", `{"path": "duration.go"}`},
			{"read_file", `{"path": "duration_test.go"}`}},
			sameID: true},
	},
	"truncated": {
		{calls: [][2]string{{"list_files", `{}`}}},
		{text: "The bug is in the units table: the entry for 'm' maps",
			finish: "length"},
	},
	"filtered": {
		{text: "", finish: "content_filter"},
	},
	"provider-failed": {
		{text: "Let me look at", finish: "error"},
	},
	// end::turn[]
	"circles":    circles(),
	"status-400": {{status: 400, errMsg: "Invalid request: messages is empty"}},
	"status-401": {{status: 401, errMsg: "No auth credentials found"}},
	"status-402": {{status: 402, errMsg: "Insufficient credits"}},
	"status-403": {{status: 403, errMsg: "Input was flagged by moderation"}},
	"status-408": {{status: 408, errMsg: "Request timed out"}},
	"status-429": {{status: 429, errMsg: "Rate limit exceeded"}},
	"status-500": {{status: 500, errMsg: "Internal server error"}},
	"status-502": {{status: 502, errMsg: "Provider returned error"}},
	"status-503": {{status: 503, errMsg: "No provider available"}},

	"recover": {
		{calls: [][2]string{{"read_files", `{"path": "duration.go"}`}}},
		{calls: [][2]string{{"read_file", `{"path": ["duration.go"]}`}}},
		{calls: [][2]string{{"read_file", `{"path": "duration.go"}`}}},
		{calls: [][2]string{{"edit_file", `{"path": "duration.go", ` +
			`"old": "'m':time.Second,", "new": "'m':time.Minute,"}`}}},
		{calls: [][2]string{{"edit_file", `{"path": "duration.go", ` +
			`"old": "'m': time.Second,", "new": "'m': time.Minute,"}`}}},
		{calls: [][2]string{{"run_tests", `{}`}}},
		{text: "Fixed: 'm' now maps to time.Minute in duration.go, and " +
			"the tests pass."},
	},
}

// circles is a run that never finishes: the model runs the tests on
// every turn and never edits anything, until the turn limit stops it.
func circles() []turn {
	var ts []turn
	for range 20 {
		ts = append(ts, turn{calls: [][2]string{{"run_tests", `{}`}}})
	}
	return ts
}

func TestMakeFixtures(t *testing.T) {
	if !*makeFixtures {
		t.Skip("run with -fixtures to rewrite testdata/replies")
	}
	for name, script := range scripts {
		srv := httptest.NewServer(scripted(t, name, script))
		dir := t.TempDir()
		getenv := func(k string) string {
			return map[string]string{"OPENROUTER_API_KEY": "hand-written",
				"OPENROUTER_BASE_URL": srv.URL}[k]
		}
		// Most scripts end the run with an error on purpose.
		_ = run(t.Context(), getenv, io.Discard,
			config{script: name, record: dir})
		srv.Close()
		for i := range script {
			file := fmt.Sprintf("%s-%02d.json", name, i+1)
			b, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			b = bytes.Replace(b, []byte("{\n"), []byte(`{
  "note": "Hand-written in OpenRouter's response format until recorded from the live API with -record.",
`), 1)
			err = os.WriteFile(filepath.Join("testdata/replies", file), b, 0o644)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

// scripted returns a handler that answers each request with the next
// turn of the script, in OpenRouter's format, with usage estimated
// from the request's size at the default model's prices.
func scripted(t *testing.T, name string, script []turn) http.Handler {
	var mu sync.Mutex
	n := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		tn := script[n]
		n++
		if tn.status != 0 {
			w.WriteHeader(tn.status)
			_, _ = fmt.Fprintf(w, `{"error": {"code": %d, "message": %q}}`,
				tn.status, tn.errMsg)
			return
		}
		msg := fixMessage{Role: "assistant"}
		if tn.text != "" {
			msg.Content = &tn.text
		}
		for i, c := range tn.calls {
			var tc fixCall
			tc.ID = fmt.Sprintf("call_%s_%d_%d",
				strings.ReplaceAll(name, "-", ""), n, i+1)
			if tn.sameID {
				tc.ID = fmt.Sprintf("call_%s_%d",
					strings.ReplaceAll(name, "-", ""), n)
			}
			tc.Type = "function"
			tc.Function.Name, tc.Function.Arguments = c[0], c[1]
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		finish, native := "stop", "end_turn"
		if len(msg.ToolCalls) > 0 {
			finish, native = "tool_calls", "tool_use"
		}
		if tn.finish != "" {
			finish, native = tn.finish, map[string]string{
				"length": "max_tokens", "content_filter": "refusal",
				"error": "error"}[tn.finish]
		}
		out, _ := json.Marshal(msg)
		in, outTok := len(body)/4, len(out)/4+3
		cost := float64(in)*0.10/1e6 + float64(outTok)*0.50/1e6
		resp := fixResponse{
			ID:       fmt.Sprintf("gen-hand-%s-%d", name, n),
			Provider: "Anthropic", Model: defaultModel,
			Object: "chat.completion", Created: 1760001000 + n,
			Choices: []fixChoice{{FinishReason: finish,
				NativeFinishReason: native, Message: msg}},
			Usage: fixUsage{in, outTok, in + outTok,
				math.Round(cost*1e10) / 1e10},
		}
		if err := json.MarshalWrite(w, resp); err != nil {
			t.Error(err)
		}
	})
}

// The response, in the order OpenRouter writes its fields.
type (
	fixResponse struct {
		ID       string      `json:"id"`
		Provider string      `json:"provider"`
		Model    string      `json:"model"`
		Object   string      `json:"object"`
		Created  int         `json:"created"`
		Choices  []fixChoice `json:"choices"`
		Usage    fixUsage    `json:"usage"`
	}
	fixChoice struct {
		Index              int        `json:"index"`
		FinishReason       string     `json:"finish_reason"`
		NativeFinishReason string     `json:"native_finish_reason"`
		Message            fixMessage `json:"message"`
	}
	fixMessage struct {
		Role      string    `json:"role"`
		Content   *string   `json:"content"` // null with only tool calls
		ToolCalls []fixCall `json:"tool_calls,omitzero"`
	}
	fixCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	fixUsage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		Cost             float64 `json:"cost"`
	}
)
