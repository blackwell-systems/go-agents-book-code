package openrouter

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
)

// savedStream returns the body of a saved streamed reply.
func savedStream(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile("../testdata/replies/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Text string `json:"response_text"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Text
}

// collect reads a stream and joins its pieces: the text, each call's
// ID, name, and arguments, the finish reason, and the usage.
func collect(t *testing.T, c *Client) (string, error) {
	t.Helper()
	var b strings.Builder
	for d, err := range c.Stream(t.Context(), agent.Request{}) {
		if err != nil {
			return b.String(), err
		}
		b.WriteString(d.Text)
		if c := d.Call; c != nil {
			fmt.Fprintf(&b, "[%d %s %s %s]", c.Index, c.ID, c.Name, c.Arguments)
		}
		if d.FinishReason != "" {
			b.WriteString("(finish " + d.FinishReason + ")")
		}
		if d.Usage != nil {
			fmt.Fprintf(&b, "(usage %d)", d.Usage.PromptTokens)
		}
		if d.ReasoningDetails != nil {
			fmt.Fprintf(&b, "(reasoning %d bytes)", len(d.ReasoningDetails))
		}
	}
	return b.String(), nil
}

// serve returns a server that writes body in pieces of n bytes,
// flushing after each, so the client reads it in those pieces.
func serve(body string, n int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < len(body); i += n {
				_, _ = io.WriteString(w, body[i:min(i+n, len(body))])
				w.(http.Flusher).Flush()
			}
		}))
}

// tag::split-test[]

// TestStreamSplitAnywhere sends a recorded stream, with two tool calls
// split across chunks, in pieces of 1, 7, and 4096 bytes. However the
// bytes arrive, the client must produce the pieces saved in
// testdata/out/collected.txt.
func TestStreamSplitAnywhere(t *testing.T) {
	body := savedStream(t, "fix-03.json")
	want, err := os.ReadFile("../testdata/out/collected.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 7, 4096, len(body)} {
		srv := serve(body, n)
		got, err := collect(t, &Client{BaseURL: srv.URL})
		srv.Close()
		if err != nil {
			t.Fatalf("pieces of %d: %v", n, err)
		}
		if got != string(want) {
			t.Errorf("pieces of %d:\n%s\nwant\n%s", n, got, want)
		}
	}
}

// end::split-test[]

func TestStreamLongLine(t *testing.T) {
	long := strings.Repeat("x", 100<<10) // longer than bufio's default
	body := fmt.Sprintf("data: {\"choices\": [{\"delta\": {\"content\": %q},"+
		" \"finish_reason\": \"stop\"}]}\n\ndata: [DONE]\n\n", long)
	srv := serve(body, 4096)
	defer srv.Close()
	got, err := collect(t, &Client{BaseURL: srv.URL})
	if err != nil || !strings.HasPrefix(got, long) {
		t.Errorf("long line: %d bytes, %v", len(got), err)
	}
}

// tag::idle-test[]

// TestStreamIdleSynctest sends one chunk and then nothing. With the
// fake
// clock, the 30-second idle timeout passes at once, and the stream ends
// with ErrStreamIdle as the cause.
func TestStreamIdleSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		defer w.Close()
		go func() {
			chunk := `{"choices": [{"delta": {"content": "Hel"}}]}`
			_, _ = io.WriteString(w,
				": keep-alive\n\ndata: "+chunk+"\n\n")
		}()
		c := &Client{HTTP: &http.Client{Transport: pipeTransport{r}}}
		start := time.Now()
		got, err := collect(t, c)
		if got != "Hel" || !errors.Is(err, ErrStreamIdle) {
			t.Errorf("got %q, %v", got, err)
		}
		if d := time.Since(start); d != 30*time.Second {
			t.Errorf("gave up after %v, want 30s", d)
		}
	})
}

// end::idle-test[]

// pipeTransport answers every request with a 200 whose body is r.
type pipeTransport struct{ r io.Reader }

func (p pipeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := io.NopCloser(p.r)
	go func() { // a closed request ends the body, as the network would
		<-req.Context().Done()
		if pr, ok := p.r.(*io.PipeReader); ok {
			pr.CloseWithError(req.Context().Err())
		}
	}()
	return &http.Response{StatusCode: http.StatusOK, Body: body,
		Header: http.Header{}, Request: req}, nil
}

// TestStreamIdleRealConnection checks the idle timeout over a real
// connection, with a short timeout.
func TestStreamIdleRealConnection(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w,
				`data: {"choices": [{"delta": {"content": "Hi"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			<-release
		}))
	defer srv.Close()
	defer close(release)
	c := &Client{BaseURL: srv.URL, IdleTimeout: 100 * time.Millisecond}
	_, err := collect(t, c)
	if !errors.Is(err, ErrStreamIdle) {
		t.Errorf("err = %v, want ErrStreamIdle", err)
	}
}

// TestStreamCommentsKeepItAlive sends nothing but comment lines for a
// minute, each less than the idle timeout after the last, and then the
// reply. The comments keep the stream alive.
func TestStreamCommentsKeepItAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		go func() {
			defer w.Close()
			for range 3 {
				_, _ = io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
				time.Sleep(20 * time.Second)
			}
			_, _ = io.WriteString(w, `data: {"choices": [{"delta":`+
				` {"content": "ok"}, "finish_reason": "stop"}]}`+
				"\n\ndata: [DONE]\n\n")
		}()
		c := &Client{HTTP: &http.Client{Transport: pipeTransport{r}}}
		got, err := collect(t, c)
		if err != nil || got != "ok(finish stop)" {
			t.Errorf("got %q, %v", got, err)
		}
	})
}

// TestStreamIdleAfterFinish accepts a reply whose stream went quiet
// after its finish reason, before the usage and [DONE].
func TestStreamIdleAfterFinish(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `data: {"choices": [{"delta":`+
				` {"content": "done"}, "finish_reason": "stop"}]}`+"\n\n")
			w.(http.Flusher).Flush()
			<-release
		}))
	defer srv.Close()
	defer close(release)
	c := &Client{BaseURL: srv.URL, IdleTimeout: 100 * time.Millisecond}
	got, err := collect(t, c)
	if err != nil || got != "done(finish stop)" {
		t.Errorf("got %q, %v", got, err)
	}
}

// TestStreamErrorCodeString decodes a mid-stream error whose code is a
// string, as OpenRouter documents.
func TestStreamErrorCodeString(t *testing.T) {
	srv := serve(`data: {"choices": [{"delta": {"content": "Hi"}}]}`+"\n\n"+
		`data: {"error": {"code": "server_error", "message": "boom"},`+
		` "choices": [{"delta": {}, "finish_reason": "error"}]}`+"\n\n", 4096)
	defer srv.Close()
	_, err := collect(t, &Client{BaseURL: srv.URL})
	if !errors.Is(err, agent.ErrProviderFailed) ||
		!strings.Contains(err.Error(), "server_error boom") {
		t.Errorf("err = %v", err)
	}
}

// TestStreamCollectedGolden writes testdata/out/collected.txt from the
// recorded stream, read whole, with -update, and checks what it holds.
func TestStreamCollectedGolden(t *testing.T) {
	srv := serve(savedStream(t, "fix-03.json"), 1<<20)
	defer srv.Close()
	got, err := collect(t, &Client{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edit_file", "run_tests",
		"time.Minute,", "(finish tool_calls)", "(reasoning"} {
		if !strings.Contains(got, want) {
			t.Errorf("collected stream lacks %q:\n%s", want, got)
		}
	}
	if *updateCollected {
		if err := os.WriteFile("../testdata/out/collected.txt", []byte(got),
			0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var updateCollected = flag.Bool("update", false, "rewrite collected.txt")

// TestStreamUTF8Split splits a reply with non-ASCII text a byte at a
// time, inside its characters.
func TestStreamUTF8Split(t *testing.T) {
	body := `data: {"choices": [{"delta": {"content": "conversación ¿sí?"},` +
		` "finish_reason": "stop"}]}` + "\n\ndata: [DONE]\n\n"
	srv := serve(body, 1)
	defer srv.Close()
	got, err := collect(t, &Client{BaseURL: srv.URL})
	if err != nil || got != "conversación ¿sí?(finish stop)" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestStreamBadInput(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"malformed chunk", 200, "data: {\"choices\": [\n\n",
			func(err error) bool { return strings.Contains(err.Error(), "decode chunk") }},
		{"rate limited", 429, `{"error": {"code": 429, "message": "slow down"}}`,
			func(err error) bool {
				e, ok := errors.AsType[*APIError](err)
				return ok && e.Status == 429 && e.Retryable()
			}},
		{"too long", 200, "data: " + strings.Repeat("x", maxBody+1) + "\n\n",
			func(err error) bool { return errors.Is(err, errTooLarge) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
				}))
			defer srv.Close()
			_, err := collect(t, &Client{BaseURL: srv.URL})
			if err == nil || !tt.check(err) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// TestStreamIdleHTTP2 checks the idle timeout over HTTP/2, whose
// transport reports cancellation differently from HTTP/1.1.
func TestStreamIdleHTTP2(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor != 2 {
				t.Errorf("request used %s", r.Proto)
			}
			_, _ = io.WriteString(w,
				`data: {"choices": [{"delta": {"content": "Hi"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			<-release
		}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	defer close(release)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(),
		IdleTimeout: 100 * time.Millisecond}
	_, err := collect(t, c)
	if !errors.Is(err, ErrStreamIdle) {
		t.Errorf("err = %v, want ErrStreamIdle", err)
	}
}

// TestStreamReasoning checks that a reasoning record in a stream comes
// through as a Delta.
func TestStreamReasoning(t *testing.T) {
	srv := serve(`data: {"choices": [{"delta": {"reasoning_details":`+
		` [{"type": "reasoning.text", "signature": "abc"}]}}]}`+"\n\n"+
		`data: {"choices": [{"delta": {"content": "ok"},`+
		` "finish_reason": "stop"}]}`+"\n\ndata: [DONE]\n\n", 4096)
	defer srv.Close()
	got, err := collect(t, &Client{BaseURL: srv.URL})
	if err != nil || !strings.HasPrefix(got, "(reasoning") {
		t.Errorf("got %q, %v", got, err)
	}
}
