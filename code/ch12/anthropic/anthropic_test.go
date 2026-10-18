package anthropic

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// The responses in these tests are written by hand from the Messages
// API's documentation; the recorded runs in ch12/testdata check the
// same paths against the live API.

// TestWireRequest checks the mapping of a conversation onto the API:
// the system prompt moves to its own field, one reply's tool results
// share one user turn with is_error set, a note after them joins that
// turn, arguments go as objects, cache points become markers, and
// MaxTokens gets its default.
func TestWireRequest(t *testing.T) {
	c := &Client{Model: "m", BaseURL: "https://example.com/v1"}
	req := agent.Request{Messages: []agent.Message{
		{Role: "system", Content: "be brief", CachePoint: true},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "looking", ToolCalls: []agent.ToolCall{
			{ID: "a", Name: "read", Arguments: jsontext.Value(`{"p":1}`)},
			{ID: "b", Name: "read", Arguments: jsontext.Value(`{"p":2,`)},
		}},
		{Role: "tool", ToolCallID: "a", Content: "one"},
		{Role: "tool", ToolCallID: "b", Content: "bad", IsError: true,
			CachePoint: true},
		{Role: "user", Content: "[a note from the harness]"},
	}, Tools: []agent.ToolDef{{Name: "read",
		Parameters: jsontext.Value(`{"type":"object"}`)}}}
	got := mustJSON(t, c.wireRequest(req, false))
	want := `{"model":"m","max_tokens":16000,` +
		`"system":[{"type":"text","text":"be brief",` +
		`"cache_control":{"type":"ephemeral"}}],"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"fix it"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"looking"},` +
		`{"type":"tool_use","id":"a","name":"read","input":{"p":1}},` +
		`{"type":"tool_use","id":"b","name":"read","input":{}}]},` +
		`{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"a","content":"one"},` +
		`{"type":"tool_result","tool_use_id":"b","content":"bad",` +
		`"is_error":true,"cache_control":{"type":"ephemeral"}},` +
		`{"type":"text","text":"[a note from the harness]"}]}],` +
		`"tools":[{"name":"read","input_schema":{"type":"object"}}],` +
		`"thinking":{"type":"adaptive","block_binding":` +
		`{"prefix_mismatch_behavior":"drop_block"}}}`
	if got != want {
		t.Errorf("request:\n got %s\nwant %s", got, want)
	}
}

// TestReasoningStaysHome checks that thinking blocks go back only to
// the client that produced them: same API and host, unchanged; any
// other source, left out.
func TestReasoningStaysHome(t *testing.T) {
	c := &Client{Model: "m", BaseURL: "https://openrouter.ai/api/v1"}
	blocks := jsontext.Value(`[{"type":"thinking","thinking":"",` +
		`"signature":"c2ln"},{"type":"redacted_thinking","data":"ZGF0YQ=="}]`)
	msg := agent.Message{Role: "assistant", ReasoningDetails: blocks,
		ToolCalls: []agent.ToolCall{{ID: "x", Name: "t",
			Arguments: jsontext.Value(`{}`)}}}
	for _, tc := range []struct {
		from string
		sent bool
	}{
		{"anthropic@openrouter.ai", true},
		{"anthropic@api.anthropic.com", false}, // another account
		{"", false},                            // chapter 2's client
	} {
		msg.ReasoningFrom = tc.from
		got := mustJSON(t, c.wireRequest(agent.Request{
			Messages: []agent.Message{msg}}, false))
		sent := strings.Contains(got, `"signature":"c2ln"`) &&
			strings.Contains(got, `"data":"ZGF0YQ=="`)
		if sent != tc.sent {
			t.Errorf("from %q: reasoning sent %v, want %v\n%s",
				tc.from, sent, tc.sent, got)
		}
	}
}

// TestFinishReasons checks the mapping of every stop reason, and that
// the loop turns a refusal into ErrRefused.
func TestFinishReasons(t *testing.T) {
	for stop, want := range map[string]string{
		"end_turn": "stop", "stop_sequence": "stop",
		"tool_use": "tool_calls", "max_tokens": "length",
		"model_context_window_exceeded": "length",
		"refusal":                       "refusal", "pause_turn": "error", "new_reason": "error",
	} {
		if got := finishReason(stop); got != want {
			t.Errorf("%s: got %s, want %s", stop, got, want)
		}
	}
	srv := serve(t, http.StatusOK, `{"content":[{"type":"text",`+
		`"text":"I can't help with that."}],"stop_reason":"refusal",`+
		`"usage":{"input_tokens":10,"output_tokens":5}}`)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := agent.Run(t.Context(), c, nil, []agent.Message{
		{Role: "user", Content: "hi"}}, agent.Limits{MaxTurns: 3})
	if !errors.Is(err, agent.ErrRefused) || !errors.Is(err, agent.ErrBadReply) {
		t.Errorf("run: %v, want ErrRefused in the ErrBadReply family", err)
	}
}

// generateReply is a reply with a thinking block, text, and a call.
const generateReply = `{"content":[` +
	`{"type":"thinking","thinking":"","signature":"EoQB"},` +
	`{"type":"text","text":"Reading it."},` +
	`{"type":"tool_use","id":"toolu_1","name":"code_read",` +
	`"input":{"path":"a.go"}}],"stop_reason":"tool_use",` +
	`"usage":{"input_tokens":100,"output_tokens":40,` +
	`"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,` +
	`"output_tokens_details":{"thinking_tokens":25}}}`

// TestGenerate checks a whole reply: text, call, thinking kept exactly
// as sent and marked with its source, usage, and the cost from the
// price table.
func TestGenerate(t *testing.T) {
	srv := serve(t, http.StatusOK, generateReply)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client(),
		Prices: Prices{"m": {Input: 1, Output: 10, CacheRead: 0.1,
			CacheWrite: 1.25}}}
	r, err := c.Generate(t.Context(), agent.Request{})
	if err != nil {
		t.Fatal(err)
	}
	checkReply(t, c, r)
}

func checkReply(t *testing.T, c *Client, r agent.Reply) {
	t.Helper()
	m := r.Message
	if m.Content != "Reading it." || len(m.ToolCalls) != 1 ||
		string(m.ToolCalls[0].Arguments) != `{"path":"a.go"}` ||
		m.ToolCalls[0].ID != "toolu_1" || r.FinishReason != "tool_calls" {
		t.Errorf("reply = %+v", r)
	}
	kept := `[{"type":"thinking","thinking":"","signature":"EoQB"},` +
		`{"type":"text","text":"Reading it."},{"type":"tool_use",` +
		`"id":"toolu_1","name":"code_read","input":{"path":"a.go"}}]`
	if got := canonical(t, m.ReasoningDetails); got !=
		canonical(t, jsontext.Value(kept)) {
		t.Errorf("content kept = %s", got)
	}
	if m.ReasoningFrom != c.Name() {
		t.Errorf("reasoning from %q, want %q", m.ReasoningFrom, c.Name())
	}
	u := r.Usage
	if u.PromptTokens != 1300 || u.CachedTokens != 1000 ||
		u.CacheWriteTokens != 200 || u.ReasoningTokens != 25 {
		t.Errorf("usage = %+v", u)
	}
	want := (100*1 + 1000*0.1 + 200*1.25 + 40*10) / 1e6
	if math.Abs(u.Cost-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", u.Cost, want)
	}
}

// streamReply is the same reply as generateReply, as events.
const streamReply = "event: message_start\n" +
	`data: {"type":"message_start","message":{"content":[],` +
	`"usage":{"input_tokens":100,"output_tokens":1,` +
	`"cache_read_input_tokens":1000,"cache_creation_input_tokens":200}}}` +
	"\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n" +
	`data: {"type":"content_block_start","index":0,"content_block":` +
	`{"type":"thinking","thinking":""}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":0,"delta":` +
	`{"type":"thinking_delta","thinking":""}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":0,"delta":` +
	`{"type":"signature_delta","signature":"EoQB"}}` + "\n\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	`data: {"type":"content_block_start","index":1,"content_block":` +
	`{"type":"text","text":""}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":1,"delta":` +
	`{"type":"text_delta","text":"Reading"}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":1,"delta":` +
	`{"type":"text_delta","text":" it."}}` + "\n\n" +
	`data: {"type":"content_block_stop","index":1}` + "\n\n" +
	`data: {"type":"content_block_start","index":2,"content_block":` +
	`{"type":"tool_use","id":"toolu_1","name":"code_read","input":{}}}` +
	"\n\n" +
	`data: {"type":"content_block_delta","index":2,"delta":` +
	`{"type":"input_json_delta","partial_json":"{\"path\":"}}` + "\n\n" +
	`data: {"type":"content_block_delta","index":2,"delta":` +
	`{"type":"input_json_delta","partial_json":" \"a.go\"}"}}` + "\n\n" +
	`data: {"type":"content_block_stop","index":2}` + "\n\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},` +
	`"usage":{"output_tokens":40,"output_tokens_details":` +
	`{"thinking_tokens":25}}}` + "\n\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// TestStream checks that the events assemble into the same reply
// Generate returns.
func TestStream(t *testing.T) {
	srv := serve(t, http.StatusOK, streamReply)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client(),
		Prices: Prices{"m": {Input: 1, Output: 10, CacheRead: 0.1,
			CacheWrite: 1.25}}}
	r, err := agent.Collect(c.Stream(t.Context(), agent.Request{}))
	if err != nil {
		t.Fatal(err)
	}
	r.Message.ToolCalls[0].Arguments = jsontext.Value(compact(t,
		r.Message.ToolCalls[0].Arguments))
	checkReply(t, c, r)
}

// TestStreamCutOff checks that a stream ending before message_stop is
// an incomplete response, as chapter 6's is.
func TestStreamCutOff(t *testing.T) {
	cut := streamReply[:strings.Index(streamReply, `data: {"type":"message_delta"`)]
	srv := serve(t, http.StatusOK, cut)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := agent.Collect(c.Stream(t.Context(), agent.Request{}))
	if !errors.Is(err, agent.ErrIncompleteResponse) {
		t.Errorf("err = %v, want ErrIncompleteResponse", err)
	}
}

// TestErrors checks that the API's errors become APIErrors that say
// whether a retry could help, including an overloaded API, which
// answers 529, and an error event partway through a stream.
func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		retryable bool
	}{
		{529, `{"type":"error","error":{"type":"overloaded_error",` +
			`"message":"Overloaded"}}`, true},
		{429, `{"type":"error","error":{"type":"rate_limit_error",` +
			`"message":"slow down"}}`, true},
		{400, `{"type":"error","error":{"type":"invalid_request_error",` +
			`"message":"max_tokens: Field required"}}`, false},
		{http.StatusOK, "data: {\"type\":\"message_start\",\"message\":" +
			"{\"content\":[],\"usage\":{}}}\n\ndata: {\"type\":\"error\"," +
			"\"error\":{\"type\":\"overloaded_error\",\"message\":" +
			"\"Overloaded\"}}\n\n", true},
	} {
		srv := serve(t, tc.status, tc.body)
		c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
		_, err := agent.Collect(c.Stream(t.Context(), agent.Request{}))
		e, ok := errors.AsType[*APIError](err)
		if !ok || e.Retryable() != tc.retryable || e.Message == "" {
			t.Errorf("status %d: err %v, want retryable %v", tc.status,
				err, tc.retryable)
		}
	}
}

// TestBilledCost checks that a cost the server reports, as OpenRouter's
// Messages API does, wins over the price table.
func TestBilledCost(t *testing.T) {
	billed := strings.Replace(generateReply, `"usage":{`,
		`"usage":{"cost":0.5,`, 1)
	srv := serve(t, http.StatusOK, billed)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client(),
		Prices: Prices{"m": {Input: 1}}}
	r, err := c.Generate(t.Context(), agent.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.Cost != 0.5 {
		t.Errorf("cost = %v, want the billed 0.5", r.Usage.Cost)
	}
}

// TestPrices checks the long-context tier: a prompt of exactly the
// threshold pays the base price, one token more pays the higher price
// for every token.
func TestPrices(t *testing.T) {
	p, err := DefaultPrices()
	if err != nil {
		t.Fatal(err)
	}
	base := p.Cost("claude-haiku-5-5", agent.Usage{PromptTokens: 100_000,
		CompletionTokens: 1000})
	long := p.Cost("claude-haiku-5-5", agent.Usage{PromptTokens: 100_001,
		CompletionTokens: 1000})
	if math.Abs(base-0.0105) > 1e-9 || math.Abs(long-0.0525005) > 1e-9 {
		t.Errorf("costs %v and %v", base, long)
	}
	if p.Cost("unknown-model", agent.Usage{PromptTokens: 5}) != 0 {
		t.Error("an unknown model was priced")
	}
}

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("anthropic-version") == "" ||
				r.URL.Path != "/messages" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
	t.Cleanup(srv.Close)
	return srv
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func compact(t *testing.T, v jsontext.Value) string {
	t.Helper()
	v = jsontext.Value(append([]byte(nil), v...))
	if err := v.Compact(); err != nil {
		t.Fatal(err)
	}
	return string(v)
}

func canonical(t *testing.T, v jsontext.Value) string {
	t.Helper()
	v = jsontext.Value(append([]byte(nil), v...))
	if err := v.Canonicalize(); err != nil {
		t.Fatal(err)
	}
	return string(v)
}

// TestEchoUnchanged checks that a reply's content goes back exactly as
// it came, even when thinking and text interleave, which rebuilding
// from the text and calls would reorder.
func TestEchoUnchanged(t *testing.T) {
	content := `[{"type":"thinking","thinking":"","signature":"QQ=="},` +
		`{"type":"text","text":"one"},` +
		`{"type":"thinking","thinking":"","signature":"Qg=="},` +
		`{"type":"tool_use","id":"t1","name":"read","input":{"p":1}},` +
		`{"type":"text","text":"two"}]`
	srv := serve(t, http.StatusOK, `{"content":`+content+
		`,"stop_reason":"tool_use","usage":{}}`)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
	r, err := c.Generate(t.Context(), agent.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "onetwo" || len(r.Message.ToolCalls) != 1 {
		t.Errorf("reply = %+v", r.Message)
	}
	got := mustJSON(t, c.wireRequest(agent.Request{
		Messages: []agent.Message{r.Message}}, false))
	var sent struct {
		Messages []struct {
			Content jsontext.Value `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatal(err)
	}
	if canonical(t, sent.Messages[0].Content) !=
		canonical(t, jsontext.Value(content)) {
		t.Errorf("sent back:\n%s\nwant\n%s", sent.Messages[0].Content,
			content)
	}
}

// TestStreamEdgeCases checks a signature that arrives in the block's
// start rather than in a delta, and a usage-only message_delta after
// the one with the stop reason, which must not overwrite it.
func TestStreamEdgeCases(t *testing.T) {
	body := `data: {"type":"message_start","message":{"content":[],` +
		`"usage":{"input_tokens":5}}}` + "\n\n" +
		`data: {"type":"content_block_start","index":0,"content_block":` +
		`{"type":"thinking","thinking":"","signature":"U1RBUlQ="}}` + "\n\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"output_tokens":3}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{},` +
		`"usage":{"output_tokens":4}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	srv := serve(t, http.StatusOK, body)
	c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
	r, err := agent.Collect(c.Stream(t.Context(), agent.Request{}))
	if err != nil {
		t.Fatal(err)
	}
	if r.FinishReason != "stop" || r.Usage.CompletionTokens != 4 {
		t.Errorf("finish %q, output %d", r.FinishReason,
			r.Usage.CompletionTokens)
	}
	if !strings.Contains(string(r.Message.ReasoningDetails),
		`"signature":"U1RBUlQ="`) {
		t.Errorf("signature lost: %s", r.Message.ReasoningDetails)
	}
}

// TestErrorInBody checks a 200 whose body is an error, and an error
// type the client doesn't know, which comes back as a 502.
func TestErrorInBody(t *testing.T) {
	for _, tc := range []struct {
		body      string
		status    int
		retryable bool
	}{
		{`{"type":"error","error":{"type":"billing_error",` +
			`"message":"no credit"}}`, 402, false},
		{`{"type":"error","error":{"type":"new_error","message":"?"}}`,
			502, true},
	} {
		srv := serve(t, http.StatusOK, tc.body)
		c := &Client{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}
		_, err := c.Generate(t.Context(), agent.Request{})
		e, ok := errors.AsType[*APIError](err)
		if !ok || e.Status != tc.status || e.Retryable() != tc.retryable {
			t.Errorf("%s: err %v", tc.body, err)
		}
	}
}

// TestLoadPricesStrict checks that a misspelled field is an error, not
// a price of zero.
func TestLoadPricesStrict(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prices.json")
	err := os.WriteFile(file, []byte(`{"m":{"inptu":1}}`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrices(file); err == nil {
		t.Error("a misspelled price field loaded")
	}
}

// TestTimeoutCauses checks that both timeout causes report Timeout,
// and that errors.Is still finds each one.
func TestTimeoutCauses(t *testing.T) {
	for _, e := range []error{ErrCallTimeout, ErrStreamIdle} {
		wrapped := fmt.Errorf("send request: %w", e)
		to, ok := errors.AsType[interface {
			error
			Timeout() bool
		}](wrapped)
		if !ok || !to.Timeout() || !errors.Is(wrapped, e) {
			t.Errorf("%v: Timeout %v, Is %v", e, ok, errors.Is(wrapped, e))
		}
	}
}
