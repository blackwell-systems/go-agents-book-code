package main

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// echo is a tool that returns its arguments.
var echo = agent.Tool{
	Def: agent.ToolDef{Name: "echo",
		Parameters: jsontext.Value(`{"type": "object"}`)},
	Run: func(_ context.Context, args jsontext.Value) (string, error) {
		return string(args), nil
	},
}

// scripted returns a Runner for chapter 6's agent with a Fake model.
func scripted(replies ...agent.Message) Runner {
	return func(ctx context.Context, task string) iter.Seq[agent.Event] {
		m := agent.Streamed(&agent.Fake{Script: replies})
		msgs := []agent.Message{{Role: "user", Content: task}}
		return agent.Events(ctx, m, []agent.Tool{echo}, msgs,
			agent.Limits{MaxTurns: 5})
	}
}

// stuck is a StreamModel that waits until its call is cancelled.
type stuck struct {
	cause error         // why the run was cancelled
	done  chan struct{} // closed when the run is over
}

func (s *stuck) Stream(ctx context.Context,
	_ agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		<-ctx.Done()
		yield(agent.Delta{}, fmt.Errorf("wait: %w", ctx.Err()))
	}
}

// runner returns a Runner whose model is s. When the run is over, it
// records the cause of its cancellation and closes s.done.
func (s *stuck) runner() Runner {
	return func(ctx context.Context, task string) iter.Seq[agent.Event] {
		msgs := []agent.Message{{Role: "user", Content: task}}
		events := agent.Events(ctx, s, nil, msgs,
			agent.Limits{MaxTurns: 5})
		return func(yield func(agent.Event) bool) {
			defer close(s.done)
			defer func() { s.cause = context.Cause(ctx) }()
			for ev := range events {
				if !yield(ev) {
					return
				}
			}
		}
	}
}

// event is one event read from a stream, or a comment if name is "".
type event struct {
	name, data, comment string
}

// readEvents reads a stream in the server-sent events format and
// yields its events and comments as they arrive.
func readEvents(r io.Reader) iter.Seq2[event, error] {
	return func(yield func(event, error) bool) {
		sc := bufio.NewScanner(r)
		var ev event
		for sc.Scan() {
			field, value, _ := strings.Cut(sc.Text(), ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "": // a blank line ends an event; ":" starts a comment
				if sc.Text() != "" {
					ev.comment = value
				}
				if ev != (event{}) && !yield(ev, nil) {
					return
				}
				ev = event{}
			case "event":
				ev.name = value
			case "data":
				ev.data = value
			default:
				yield(ev, fmt.Errorf("unexpected line %q", sc.Text()))
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(event{}, err)
		}
	}
}

// get starts a request for the event stream of task. The caller
// closes the response body.
func get(ctx context.Context, t *testing.T, url,
	task string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		url+"/events?task="+task, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// newServer serves run's events, with a heartbeat every beat.
func newServer(t *testing.T, run Runner,
	beat time.Duration) *httptest.Server {
	t.Helper()
	s := &Server{Run: run, Heartbeat: beat, WriteWait: 5 * time.Second}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestStream runs a scripted agent through the server and checks the
// headers and every event the client reads.
func TestStream(t *testing.T) {
	srv := newServer(t, scripted(
		says("Echoing.", agent.Call("c1", "echo", `{"x":1}`)),
		says("done"),
	), time.Minute)
	resp := get(t.Context(), t, srv.URL, "task")
	defer resp.Body.Close()
	for k, want := range map[string]string{
		"Content-Type":  "text/event-stream",
		"Cache-Control": "no-cache",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	var got []event
	for ev, err := range readEvents(resp.Body) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	want := []event{
		{comment: "run started"},
		{name: "text", data: `{"text":"Echoing."}`},
		{name: "call", data: `{"id":"c1","name":"echo","args":"{\"x\":1}"}`},
		{name: "result", data: `{"id":"c1","content":"{\"x\":1}"}`},
		{name: "turn", data: `{"turn":1}`},
		{name: "text", data: `{"text":"done"}`},
		{name: "stopped", data: `{"answer":"done"}`},
	}
	if !slices.Equal(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
}

func TestEncode(t *testing.T) {
	stopped := func(err error, msgs ...agent.Message) agent.Event {
		return agent.Event{Kind: agent.StoppedEvent, Err: err,
			Result: agent.Result{Messages: msgs}}
	}
	tests := []struct {
		name string
		ev   agent.Event
		want string
	}{
		{
			name: "newlines stay inside the data line",
			ev:   agent.Event{Kind: agent.TextEvent, Text: "a\n\nb"},
			want: "event: text\ndata: {\"text\":\"a\\n\\nb\"}\n\n",
		},
		{
			name: "a failed tool",
			ev: agent.Event{Kind: agent.ToolResultEvent, Tool: agent.Message{
				Role: "tool", ToolCallID: "c1", Content: "no", IsError: true}},
			want: "event: result\n" +
				`data: {"id":"c1","content":"no","is_error":true}` + "\n\n",
		},
		{
			name: "invalid UTF-8 in tool output",
			ev: agent.Event{Kind: agent.ToolResultEvent,
				Tool: agent.Message{ToolCallID: "c1", Content: "a\xffb"}},
			want: "event: result\n" +
				`data: {"id":"c1","content":"a` + "�" + `b"}` + "\n\n",
		},
		{
			name: "invalid JSON arguments",
			ev: agent.Event{Kind: agent.CallEvent, Call: agent.Call(
				"c1", "echo", `{"x":`)},
			want: "event: call\n" +
				`data: {"id":"c1","name":"echo","args":"{\"x\":"}` + "\n\n",
		},
		{
			name: "an answer",
			ev:   stopped(nil, says("the answer")),
			want: "event: stopped\ndata: {\"answer\":\"the answer\"}\n\n",
		},
		{
			name: "an error",
			ev:   stopped(agent.ErrMaxTurns, says("", agent.Call("c", "x", "{}"))),
			want: "event: stopped\n" +
				`data: {"error":"agent: budget exhausted: max turns reached"}` +
				"\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encode(tt.ev)
			if err != nil || got != tt.want {
				t.Errorf("encode = %q, %v\nwant %q", got, err, tt.want)
			}
		})
	}
	if _, err := encode(agent.Event{Kind: 99}); err == nil {
		t.Error("encode of an unknown kind: no error")
	}
}

// TestEncodeIsJSON checks that every data line is valid JSON.
func TestEncodeIsJSON(t *testing.T) {
	msg, err := encode(agent.Event{Kind: agent.TextEvent,
		Text: "quote \" backslash \\ tab \t"})
	if err != nil {
		t.Fatal(err)
	}
	_, data, _ := strings.Cut(msg, "data: ")
	var d struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != "quote \" backslash \\ tab \t" {
		t.Errorf("text = %q", d.Text)
	}
}

// TestHeartbeat checks that a stream sends comments while the model is
// quiet.
func TestHeartbeat(t *testing.T) {
	m := &stuck{done: make(chan struct{})}
	srv := newServer(t, m.runner(), 10*time.Millisecond)
	resp := get(t.Context(), t, srv.URL, "task")
	defer resp.Body.Close()
	beats := 0
	for ev, err := range readEvents(resp.Body) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.comment == "heartbeat" {
			beats++
		}
		if beats == 3 {
			break
		}
	}
	if beats != 3 {
		t.Errorf("read %d heartbeats, want 3", beats)
	}
}

// TestDisconnect checks that a client going away cancels the run, and
// that the handler waits for the run to stop before it returns.
func TestDisconnect(t *testing.T) {
	m := &stuck{done: make(chan struct{})}
	s := &Server{Run: m.runner(), Heartbeat: time.Minute,
		WriteWait: 5 * time.Second}
	handled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request) {
		s.Handler().ServeHTTP(w, r)
		select {
		case <-m.done:
		default:
			t.Error("the handler returned while the run went on")
		}
		close(handled)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	resp := get(ctx, t, srv.URL, "task")
	defer resp.Body.Close()
	for ev := range readEvents(resp.Body) {
		if ev.comment == "run started" {
			break
		}
	}
	cancel() // the client goes away
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler didn't return after the client left")
	}
	if !errors.Is(m.cause, context.Canceled) {
		t.Errorf("the run's cause = %v, want context.Canceled", m.cause)
	}
}

// TestFailedSend checks that when a send fails, the handler stops the
// run and waits for it before returning. A ResponseRecorder can't set
// a write deadline, so the first send fails.
func TestFailedSend(t *testing.T) {
	m := &stuck{done: make(chan struct{})}
	s := &Server{Run: m.runner(), Heartbeat: time.Minute,
		WriteWait: time.Second}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/events?task=x", nil)
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	select {
	case <-m.done:
	default:
		t.Fatal("the handler returned while the run went on")
	}
	if !errors.Is(m.cause, context.Canceled) {
		t.Errorf("the run's cause = %v, want context.Canceled", m.cause)
	}
}

func TestBadRequests(t *testing.T) {
	srv := newServer(t, scripted(), time.Minute)
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/events", http.StatusBadRequest},
		{http.MethodPost, "/events?task=x", http.StatusMethodNotAllowed},
		{http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		req, err := http.NewRequestWithContext(t.Context(), tt.method,
			srv.URL+tt.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path,
				resp.StatusCode, tt.want)
		}
	}
}

func TestPage(t *testing.T) {
	srv := newServer(t, scripted(), time.Minute)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "new EventSource(") {
		t.Errorf("the page has no EventSource:\n%s", b)
	}
}

// TestDemo runs the program's own Runner, unpaced, through the server:
// the scripted model fixes the duration project, and go test passes.
func TestDemo(t *testing.T) {
	srv := newServer(t, demo(0, 0), time.Minute)
	resp := get(t.Context(), t, srv.URL, "fix")
	defer resp.Body.Close()
	var results []string
	var last event
	for ev, err := range readEvents(resp.Body) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.name == "result" {
			results = append(results, ev.data)
		}
		last = ev
	}
	if len(results) != 6 || !strings.Contains(results[0], "FAIL") ||
		!strings.Contains(results[5], "ok") {
		t.Errorf("tool results = %q", results)
	}
	if last.name != "stopped" || !strings.Contains(last.data, "answer") {
		t.Errorf("last event = %+v, want an answer", last)
	}
}

// TestServeShutdown checks that stopping the program ends the streams
// still open, so the server shuts down in time.
func TestServeShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, "localhost:0", pw) }()
	addr, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	url := strings.TrimSpace(strings.TrimPrefix(addr, "open "))
	resp := get(t.Context(), t, url, "task")
	defer resp.Body.Close()
	cancel() // as Ctrl-C does
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Errorf("reading the stream: %v", err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve didn't return")
	}
}
