package openrouter

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch08/replay"
)

const okReply = `{"choices":[{"finish_reason":"stop",` +
	`"message":{"role":"assistant","content":"hi"}}],` +
	`"usage":{"prompt_tokens":3,"completion_tokens":1,"cost":0.5}}`

// TestGenerateSends checks the HTTP request the client builds.
func TestGenerateSends(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
				t.Errorf("request = %s %s, want POST /v1/chat/completions",
					r.Method, r.URL.Path)
			}
			if h := r.Header.Get("Authorization"); h != "Bearer key" {
				t.Errorf("Authorization = %q, want %q", h, "Bearer key")
			}
			if h := r.Header.Get("Content-Type"); h != "application/json" {
				t.Errorf("Content-Type = %q", h)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("request body: %v", err)
			}
			_, _ = io.WriteString(w, okReply)
		}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test/model"}
	req := agent.Request{
		Messages: []agent.Message{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Hello"},
		},
		MaxTokens: 20,
	}
	reply, err := c.Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "test/model" || got.MaxTokens != 20 || len(got.Messages) != 2 ||
		got.Messages[1].Role != "user" || got.Messages[1].Content.Text != "Hello" {
		t.Errorf("sent %+v", got)
	}
	want := agent.Reply{
		Message:      agent.Message{Role: "assistant", Content: "hi"},
		FinishReason: "stop",
		Usage:        agent.Usage{PromptTokens: 3, CompletionTokens: 1, Cost: 0.5},
	}
	if reply.Message.Content != want.Message.Content ||
		reply.Message.Role != want.Message.Role ||
		reply.FinishReason != want.FinishReason || reply.Usage != want.Usage {
		t.Errorf("reply = %+v, want %+v", reply, want)
	}
}

// TestGenerateReplies runs the client on saved responses.
func TestGenerateReplies(t *testing.T) {
	tests := []struct {
		script  string
		content string // the reply's text, for a successful call
		finish  string
		usage   agent.Usage
		errText string // part of the error, for a failed call
	}{
		{
			script:  "chat",
			content: "A goroutine is a lightweight thread",
			finish:  "stop",
			usage: agent.Usage{PromptTokens: 43, CompletionTokens: 122,
				Cost: 6.53e-05},
		},
		{
			script:  "length",
			content: "Go, also known as Golang",
			finish:  "length",
			usage: agent.Usage{PromptTokens: 26, CompletionTokens: 20,
				Cost: 1.26e-05},
		},
		{script: "error", errText: "502 Bad Gateway: Provider returned error"},
		{script: "unauthorized", errText: "401 Unauthorized: User not found."},
	}
	for _, tt := range tests {
		t.Run(tt.script, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: &replay.Transport{
				Files: os.DirFS("../testdata/replies"),
			}}}
			ctx := replay.WithScript(t.Context(), tt.script)
			reply, err := c.Generate(ctx, agent.Request{})
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("err = %v, want one containing %q", err, tt.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(reply.Message.Content, tt.content) {
				t.Errorf("content = %q, want it to start %q",
					reply.Message.Content, tt.content)
			}
			if reply.FinishReason != tt.finish {
				t.Errorf("finish reason = %q, want %q", reply.FinishReason, tt.finish)
			}
			if reply.Usage != tt.usage {
				t.Errorf("usage = %+v, want %+v", reply.Usage, tt.usage)
			}
		})
	}
}

// TestGenerateFailures checks replies that are errors without being
// failed HTTP calls.
func TestGenerateFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string // part of the error
	}{
		{"no choices", 200, `{"choices": []}`, "no choices"},
		{"error in a choice", 200, `{"choices": [{"finish_reason": "error",` +
			`"error": {"code": 502, "message": "upstream died"}}]}`,
			"provider failed partway: 502 upstream died"},
		{"error page", 502, `<html>Bad Gateway</html>`, "502 Bad Gateway"},
		{"not JSON", 200, `<html>`, "decode response"},
		{"too large", 200, strings.Repeat(" ", maxBody+1), "too large"},
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
			_, err := c.Generate(t.Context(), agent.Request{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// TestGenerateCancel checks that a call stops at its own deadline or
// when the caller cancels, and that the error carries both the kind of
// cancellation and its cause, over HTTP/1 and HTTP/2, whose transports
// report them differently.
func TestGenerateCancel(t *testing.T) {
	errStop := errors.New("user stopped the run")
	tests := []struct {
		name    string
		timeout time.Duration
		cancel  bool // the caller cancels with errStop
		want    []error
	}{
		{"call deadline", 20 * time.Millisecond, false,
			[]error{ErrCallTimeout, context.DeadlineExceeded}},
		{"caller cancels", time.Minute, true,
			[]error{errStop, context.Canceled}},
	}
	for _, proto := range []string{"HTTP/1.1", "HTTP/2.0"} {
		release := make(chan struct{})
		srv := httptest.NewUnstartedServer(http.HandlerFunc(
			func(_ http.ResponseWriter, r *http.Request) {
				if r.Proto != proto {
					t.Errorf("request used %s, want %s", r.Proto, proto)
				}
				<-release // answer only when the test ends
			}))
		srv.EnableHTTP2 = proto == "HTTP/2.0"
		srv.StartTLS()
		for _, tt := range tests {
			t.Run(proto+" "+tt.name, func(t *testing.T) {
				c := &Client{BaseURL: srv.URL, HTTP: srv.Client(),
					Timeout: tt.timeout}
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				if tt.cancel {
					time.AfterFunc(20*time.Millisecond,
						func() { cancel(errStop) })
				}
				_, err := c.Generate(ctx, agent.Request{})
				for _, w := range tt.want {
					if !errors.Is(err, w) {
						t.Errorf("err = %v, want it to match %v", err, w)
					}
				}
			})
		}
		close(release) // before Close, which waits for the handlers
		srv.Close()
	}
}

// TestProbe checks token probabilities from a saved reply, and the
// error for a reply without them.
func TestProbe(t *testing.T) {
	c := &Client{HTTP: &http.Client{Transport: &replay.Transport{
		Files: os.DirFS("../testdata/replies"),
	}}}
	ctx := replay.WithScript(t.Context(), "probe")
	msgs := []agent.Message{{Role: "user", Content: "Hi"}}
	reply, err := c.Probe(ctx, msgs, 0, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reply.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want stop", reply.FinishReason)
	}
	first := reply.Tokens[0]
	if first.Text != "Con" || math.Abs(first.P-0.33) > 0.01 ||
		len(first.Top) != 3 || first.Top[1].Text != "Simple" {
		t.Errorf("first token = %+v", first)
	}

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, okReply)
		}))
	defer srv.Close()
	c = &Client{BaseURL: srv.URL}
	if _, err := c.Probe(t.Context(), msgs, 0, 3, 10); !errors.Is(err, ErrNoLogprobs) {
		t.Errorf("err = %v, want ErrNoLogprobs", err)
	}
}

// TestToolsOnTheWire checks how tools, tool calls, tool results, and
// reasoning details are sent, and how tool calls are read back.
func TestToolsOnTheWire(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"choices": [{"finish_reason": "tool_calls",
				"message": {"role": "assistant", "content": null,
				"reasoning_details": [{"type": "reasoning.text", "text": "hm"}],
				"tool_calls": [
					{"id": "c1", "type": "function", "function":
						{"name": "list_files", "arguments": ""}},
					{"id": "c2", "type": "function", "function":
						{"name": "read_file", "arguments": "{\"path\": \"a.go\"}"}}
				]}}]}`)
		}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	req := agent.Request{
		Tools: []agent.ToolDef{{Name: "read_file", Description: "Read.",
			Parameters: jsontext.Value(`{"type": "object"}`)}},
		Messages: []agent.Message{
			{Role: "user", Content: "Go"},
			{Role: "assistant", ReasoningDetails: jsontext.Value(`[{"x": 1}]`),
				ToolCalls: []agent.ToolCall{{ID: "c0", Name: "read_file",
					Arguments: jsontext.Value(`{"path": "b.go"}`)}}},
			{Role: "tool", ToolCallID: "c0", IsError: true, Content: "no file"},
			{Role: "tool", ToolCallID: "c9"},
		},
	}
	reply, err := c.Generate(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"model": "",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "read_file", "description": "Read.",
			"parameters": map[string]any{"type": "object"}}}},
		"messages": []any{
			map[string]any{"role": "user", "content": "Go"},
			map[string]any{"role": "assistant",
				"reasoning_details": []any{map[string]any{"x": 1.0}},
				"tool_calls": []any{map[string]any{"id": "c0", "type": "function",
					"function": map[string]any{"name": "read_file",
						"arguments": `{"path": "b.go"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "c0",
				"content": "Error: no file"},
			map[string]any{"role": "tool", "tool_call_id": "c9",
				"content": "(no output)"},
		},
	}
	if !reflect.DeepEqual(sent, want) {
		got, _ := json.Marshal(sent, jsontext.Multiline(true))
		t.Errorf("sent:\n%s", got)
	}

	m := reply.Message
	if len(m.ToolCalls) != 2 || string(m.ToolCalls[0].Arguments) != "{}" ||
		m.ToolCalls[1].Name != "read_file" ||
		string(m.ToolCalls[1].Arguments) != `{"path": "a.go"}` {
		t.Errorf("tool calls = %+v", m.ToolCalls)
	}
	if !bytes.Contains(m.ReasoningDetails, []byte("reasoning.text")) {
		t.Errorf("reasoning details = %s", m.ReasoningDetails)
	}
}

// TestAPIErrors checks the APIError for each status the chapter lists,
// and that Retryable is true only for the ones worth sending again.
func TestAPIErrors(t *testing.T) {
	for _, tt := range []struct {
		status    int
		retryable bool
	}{
		{400, false}, {401, false}, {402, false}, {403, false},
		{408, true}, {429, true}, {500, true}, {502, true}, {503, true},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w,
						`{"error": {"code": 0, "message": "nope"}}`)
				}))
			defer srv.Close()
			_, err := (&Client{BaseURL: srv.URL}).Generate(t.Context(),
				agent.Request{})
			e, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("err = %v, want an *APIError", err)
			}
			if e.Status != tt.status || e.Message != "nope" ||
				e.RetryAfter != 7*time.Second || e.Retryable() != tt.retryable {
				t.Errorf("got %+v, retryable %v", e, e.Retryable())
			}
		})
	}
}

// TestErrorInsideA200 checks that an error object in a 200 reply is an
// APIError with the code the provider gave, and that a failure partway
// through a reply is in the ErrBadReply family.
func TestErrorInsideA200(t *testing.T) {
	c := &Client{HTTP: &http.Client{Transport: &replay.Transport{
		Files: os.DirFS("../testdata/replies"),
	}}}
	_, err := c.Generate(replay.WithScript(t.Context(), "error"),
		agent.Request{})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 502 ||
		!e.Retryable() {
		t.Errorf("error in a 200: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"choices": [{"finish_reason": "error",
				"error": {"code": 502, "message": "upstream died"}}]}`)
		}))
	defer srv.Close()
	_, err = (&Client{BaseURL: srv.URL}).Generate(t.Context(), agent.Request{})
	if !errors.Is(err, agent.ErrProviderFailed) ||
		!errors.Is(err, agent.ErrBadReply) {
		t.Errorf("failure partway: %v", err)
	}
}

// blockingTransport answers no request: it waits until the request's
// context is done.
type blockingTransport struct{}

func (blockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, fmt.Errorf("blocked: %w", context.Cause(r.Context()))
}

// tag::synctest[]

// TestTimeoutSynctest checks the call deadline with testing/synctest,
// whose fake clock lets a two-minute timeout pass instantly.
func TestTimeoutSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &Client{HTTP: &http.Client{Transport: blockingTransport{}}}
		start := time.Now()
		_, err := c.Generate(t.Context(), agent.Request{})
		if !errors.Is(err, ErrCallTimeout) ||
			!errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
		if d := time.Since(start); d != 2*time.Minute {
			t.Errorf("the call gave up after %v, want 2m0s", d)
		}
	})
}

// end::synctest[]

// TestAPIErrorEdges checks errors with no code, an unknown status, and
// a body that isn't the API's JSON.
func TestAPIErrorEdges(t *testing.T) {
	if got := (&APIError{Status: 599, Message: "x"}).Error(); got != "openrouter: 599: x" {
		t.Errorf("unknown status: %q", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/a/chat/completions" {
				_, _ = io.WriteString(w, `{"error": {"message": "lost"}}`)
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>Bad Gateway</html>")
		}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL + "/a"}).Generate(t.Context(),
		agent.Request{})
	if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 502 {
		t.Errorf("error in a 200 with no code: %v", err)
	}
	_, err = (&Client{BaseURL: srv.URL + "/b"}).Generate(t.Context(),
		agent.Request{})
	if e, ok := errors.AsType[*APIError](err); !ok ||
		e.Message != "<html>Bad Gateway</html>" {
		t.Errorf("HTML error page: %v", err)
	}
}

// TestProbeMismatch checks that logprobs which don't spell the reply's
// text are an error, from a provider that sent such a reply.
func TestProbeMismatch(t *testing.T) {
	c := &Client{HTTP: &http.Client{Transport: &replay.Transport{
		Files: os.DirFS("../testdata/replies"),
	}}}
	ctx := replay.WithScript(t.Context(), "probe-mismatch")
	msgs := []agent.Message{{Role: "user", Content: "Hi"}}
	_, err := c.Probe(ctx, msgs, 0, 3, 10)
	if !errors.Is(err, ErrLogprobsMismatch) {
		t.Fatalf("err = %v, want ErrLogprobsMismatch", err)
	}
	if !strings.Contains(err.Error(), "CoreWeave") {
		t.Errorf("err = %v, want the provider named", err)
	}
}
