package orsdk

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
	or "github.com/blackwell-systems/go-agents-book/code/ch12/openrouter"
)

// reply is a chat completion with text, a tool call, reasoning, and
// usage, in the format chapter 2's client reads.
const reply = `{"id":"gen-1","object":"chat.completion","created":1,` +
	`"model":"m","choices":[{"index":0,"finish_reason":"tool_calls",` +
	`"message":{"role":"assistant","content":"Reading it.",` +
	`"reasoning_details":[{"type":"reasoning.encrypted","data":"c2ln",` +
	`"id":"r1","format":"anthropic-claude-v1","index":0}],` +
	`"tool_calls":[{"id":"call_1","type":"function","function":` +
	`{"name":"code_read","arguments":"{\"path\":\"a.go\"}"}}]}}],` +
	`"usage":{"prompt_tokens":1300,"completion_tokens":40,` +
	`"total_tokens":1340,"cost":0.0042,"prompt_tokens_details":` +
	`{"cached_tokens":1000,"cache_write_tokens":200},` +
	`"completion_tokens_details":{"reasoning_tokens":25}}}`

// TestGenerate checks the request the SDK sends for the agent's
// messages, and the mapping of its result back onto a Reply.
func TestGenerate(t *testing.T) {
	var body []byte
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	})
	c := New("key", "m", srv.URL, srv.Client())
	got, err := c.Generate(t.Context(), agent.Request{MaxTokens: 100,
		Messages: []agent.Message{
			{Role: "system", Content: "be brief", CachePoint: true},
			{Role: "user", Content: "fix it"},
		}, Tools: []agent.ToolDef{{Name: "code_read", Description: "read",
			Parameters: jsontext.Value(`{"type":"object","properties":` +
				`{"path":{"type":"string"}}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model":"m"`, `"max_tokens":100`,
		`"cache_control":{"type":"ephemeral"}`, `"name":"code_read"`,
		`"content":"fix it"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	m := got.Message
	if m.Content != "Reading it." || len(m.ToolCalls) != 1 ||
		m.ToolCalls[0].ID != "call_1" ||
		string(m.ToolCalls[0].Arguments) != `{"path":"a.go"}` ||
		got.FinishReason != "tool_calls" || m.ReasoningFrom != "" ||
		!strings.Contains(string(m.ReasoningDetails), `"data":"c2ln"`) {
		t.Errorf("reply = %+v", got)
	}
	u := got.Usage
	if u.PromptTokens != 1300 || u.CachedTokens != 1000 ||
		u.CacheWriteTokens != 200 || u.ReasoningTokens != 25 ||
		u.Cost != 0.0042 {
		t.Errorf("usage = %+v", u)
	}
}

// TestNoRetries checks that the SDK's own retries are off: a 503 is
// sent once and comes back as the hand-written client's APIError,
// which says it's worth retrying, so chapter 12 can retry it.
func TestNoRetries(t *testing.T) {
	var calls atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":503,`+
			`"message":"no provider available"}}`)
	})
	c := New("key", "m", srv.URL, srv.Client())
	_, err := c.Generate(t.Context(), agent.Request{
		Messages: []agent.Message{{Role: "user", Content: "hi"}}})
	e, ok := errors.AsType[*or.APIError](err)
	if !ok || e.Status != 503 || !e.Retryable() ||
		e.RetryAfter != 7*time.Second || e.Message != "no provider available" {
		t.Errorf("err = %#v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d calls, want 1", n)
	}
}

// TestDeadlineDuringRetries checks that a deadline which ends the
// SDK's own retries comes back as the deadline, not as the last status
// the SDK saw: with its retries on, the SDK retries a server that
// always answers 503 until the context ends.
func TestDeadlineDuringRetries(t *testing.T) {
	var calls atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":503,`+
			`"message":"no provider available"}}`)
	})
	c := &Client{model: "m", sdk: openrouter.New( // default retries
		openrouter.WithSecurity("key"), openrouter.WithServerURL(srv.URL),
		openrouter.WithClient(statusClient{srv.Client()}))}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := c.Generate(ctx, agent.Request{
		Messages: []agent.Message{{Role: "user", Content: "hi"}}})
	if n := calls.Load(); n < 2 {
		t.Errorf("%d calls; the SDK's retries should be on", n)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	type retryable interface {
		error
		Retryable() bool
	}
	if e, ok := errors.AsType[retryable](err); ok && e.Retryable() {
		t.Errorf("err = %v says it is worth retrying", err)
	}
}

// TestReasoningStaysHome checks that reasoning goes back only when
// OpenRouter produced it.
func TestReasoningStaysHome(t *testing.T) {
	details := jsontext.Value(`[{"type":"reasoning.encrypted",` +
		`"data":"c2ln","id":"r1","format":"anthropic-claude-v1","index":0}]`)
	for _, tc := range []struct {
		from string
		sent bool
	}{{"", true}, {"anthropic@openrouter.ai", false}} {
		m := chatMessage(agent.Message{Role: "assistant",
			ReasoningDetails: details, ReasoningFrom: tc.from})
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if sent := strings.Contains(string(b), "c2ln"); sent != tc.sent {
			t.Errorf("from %q: sent %v, want %v: %s", tc.from, sent,
				tc.sent, b)
		}
	}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestCacheMarks checks that a cache point on any role reaches the
// request as a cache_control marker. The first version of this adapter
// lost the marker on tool results, the message chapter 8's CacheMarks
// marks most often, and a live run read 30 percent of its input from
// the cache instead of about 90.
func TestCacheMarks(t *testing.T) {
	for _, m := range []agent.Message{
		{Role: "system", Content: "s", CachePoint: true},
		{Role: "user", Content: "u", CachePoint: true},
		{Role: "assistant", Content: "a", CachePoint: true},
		{Role: "tool", ToolCallID: "c", Content: "t", CachePoint: true},
	} {
		b, err := json.Marshal(chatMessage(m))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"cache_control":{"type":"ephemeral"}`) {
			t.Errorf("%s message lost its cache marker: %s", m.Role, b)
		}
	}
}

// TestErrorInReply checks a 200 reply whose body is an error object,
// which OpenRouter sends when a provider fails after the status was
// sent: it becomes the APIError chapter 2's client returns.
func TestErrorInReply(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"code":502,`+
			`"message":"provider returned an error"}}`)
	})
	_, err := New("key", "m", srv.URL, srv.Client()).Generate(t.Context(),
		agent.Request{Messages: []agent.Message{{Role: "user", Content: "hi"}}})
	e, ok := errors.AsType[*or.APIError](err)
	if !ok || e.Status != 502 || !e.Retryable() ||
		e.Message != "provider returned an error" {
		t.Errorf("err = %#v", err)
	}
}

// TestTimeout checks that a call past Timeout stops with chapter 2's
// cause, since the SDK's own timeout is replaced with the HTTP client.
func TestTimeout(t *testing.T) {
	srv := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		select { // longer than the timeout, but not forever
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	c := New("key", "m", srv.URL, srv.Client())
	c.Timeout = 50 * time.Millisecond
	_, err := c.Generate(t.Context(), agent.Request{
		Messages: []agent.Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, or.ErrCallTimeout) {
		t.Errorf("err = %v, want ErrCallTimeout", err)
	}
}
