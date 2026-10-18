package openrouter

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch02/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch02/replay"
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
		got.Messages[1] != (chatMessage{Role: "user", Content: "Hello"}) {
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
		{script: "error", errText: "provider error 502"},
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
			"provider error 502: upstream died"},
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
