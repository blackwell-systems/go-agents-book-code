package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/out.txt")

// script is a fake chat completions server: it answers each request
// with the next reply in its list and keeps every request it got.
type script struct {
	mu       sync.Mutex
	replies  []string
	requests []chatRequest
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var req chatRequest
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.requests = append(s.requests, req)
	if len(s.replies) == 0 {
		http.Error(w, `{"error": {"message": "script ran out"}}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, s.replies[0])
	s.replies = s.replies[1:]
}

// toolReply is a reply that calls one tool.
func toolReply(id, name, args string) string {
	return mustJSON(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{
						"name": name, "arguments": args},
				}},
				"reasoning_details": nil,
			},
		}},
	})
}

// answerReply is a reply with text and no tool calls.
func answerReply(text, finish string) string {
	return mustJSON(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": finish,
			"message": map[string]any{
				"role": "assistant", "content": text},
		}},
		"usage": map[string]any{"prompt_tokens": 10,
			"completion_tokens": 5, "cost": 0.0001},
	})
}

// mustJSON encodes a fixed test value; it can fail only on a bug in
// the test itself.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func sample(t *testing.T) fs.FS {
	t.Helper()
	files, _, err := openDir("")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func start(t *testing.T, replies ...string) (*Client, *script) {
	t.Helper()
	s := &script{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Model: "test/model"}, s
}

func TestRunAnswersAfterTools(t *testing.T) {
	c, s := start(t,
		toolReply("c1", "list_files", "{}"),
		toolReply("c2", "read_file", `{"path": "CHANGELOG.md"}`),
		answerReply("v1.3.0 added it.", "stop"))
	msgs := []Message{{Role: "user", Content: "When?"}}
	answer, msgs, err := Run(context.Background(), c,
		fileTools(sample(t)), msgs, 5, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "v1.3.0 added it." {
		t.Errorf("answer = %q", answer)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := "user assistant tool assistant tool assistant"
	if got := strings.Join(roles, " "); got != want {
		t.Errorf("roles = %s, want %s", got, want)
	}
	if !strings.Contains(msgs[2].Content, "config/defaults.yaml") {
		t.Errorf("list_files result = %q", msgs[2].Content)
	}
	if !strings.Contains(msgs[4].Content, "v1.3.0") {
		t.Errorf("read_file result = %q", msgs[4].Content)
	}
	// The second request carries the first call and its result, and
	// the tool definitions go out with every request.
	second := s.requests[1]
	if n := len(second.Messages); n != 3 ||
		second.Messages[2].ToolCallID != "c1" {
		t.Errorf("second request messages = %+v", second.Messages)
	}
	if len(second.Tools) != 2 {
		t.Errorf("second request has %d tools", len(second.Tools))
	}
	// A null reasoning_details is not sent back.
	if second.Messages[1].Reasoning != nil {
		t.Errorf("reasoning sent back: %s", second.Messages[1].Reasoning)
	}
}

func TestRunStops(t *testing.T) {
	loop := toolReply("c", "list_files", "{}")
	tests := []struct {
		name    string
		replies []string
		want    error
	}{
		{"turn limit", []string{loop, loop, loop}, ErrMaxTurns},
		{"cut off", []string{answerReply("Half an", "length")}, errCutOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := start(t, tt.replies...)
			msgs := []Message{{Role: "user", Content: "Go"}}
			_, _, err := Run(context.Background(), c,
				fileTools(sample(t)), msgs, 3, io.Discard)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRunTool(t *testing.T) {
	tools := fileTools(sample(t))
	call := func(name, args string) ToolCall {
		var c ToolCall
		c.Function.Name, c.Function.Arguments = name, args
		return c
	}
	tests := []struct {
		name, tool, args, want string
	}{
		{"read", "read_file", `{"path": "README.md"}`, "# shortlink"},
		{"missing file", "read_file", `{"path": "NOTES.md"}`,
			"Error: read NOTES.md"},
		{"outside", "read_file", `{"path": "../main.go"}`,
			"Error: read ../main.go"},
		{"bad arguments", "read_file", `{"path": 7}`, "Error: arguments"},
		{"unknown tool", "delete_file", `{}`,
			"The tools are: list_files, read_file."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runTool(context.Background(), tools,
				call(tt.tool, tt.args))
			if !strings.Contains(got, tt.want) {
				t.Errorf("got %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// TestRootRefusesLinks checks the claim the tools' comment makes: a
// symbolic link out of the directory doesn't get past os.Root.
func TestRootRefusesLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	files, closeFiles, err := openDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles()
	if out, err := readFile(files, "link"); err == nil {
		t.Errorf("read through the link: %q", out)
	}
}

func TestAPIErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"401", 401, `{"error": {"message": "User not found.",
			"code": 401}}`, "401 Unauthorized: User not found."},
		{"error in a 200", 200, `{"choices": [],
			"error": {"message": "Provider returned error"}}`,
			"Provider returned error"},
		{"no choices", 200, `{"choices": []}`, "no choices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
				}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL}
			_, err := c.Chat(context.Background(), nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestReplay runs the program as `go run ./firstagent` does with no
// key: the recorded model replies, and the real tools on the embedded
// sample project. testdata/out.txt is the output; -update rewrites it.
func TestReplay(t *testing.T) {
	var out bytes.Buffer
	noKey := func(string) string { return "" }
	err := run(context.Background(), noKey, "", "", defaultQuestion, &out)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "out.txt")
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("output differs from %s:\n%s", golden, out.String())
	}
}

func TestOwnQuestionNeedsKey(t *testing.T) {
	noKey := func(string) string { return "" }
	err := run(context.Background(), noKey, "", "", "Who wrote it?",
		io.Discard)
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("err = %v", err)
	}
}
