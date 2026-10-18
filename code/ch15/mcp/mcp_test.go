package mcp

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// testServer offers a few tools with known behavior.
func testServer() *Server {
	return &Server{Info: Implementation{Name: "test", Version: "1.0"},
		Instructions: "Tools for tests.",
		Tools: []Tool{
			{Name: "echo", Description: "Echo the text.", ReadOnly: true,
				InputSchema: jsontext.Value(`{"type":"object","properties":{"text":{"type":"string"}}}`),
				Call: func(_ context.Context, args jsontext.Value) (string, error) {
					var a struct {
						Text  string `json:"text"`
						Sleep int    `json:"sleep"`
					}
					if err := json.Unmarshal(args, &a); err != nil {
						return "", fmt.Errorf("bad arguments: %w", err)
					}
					time.Sleep(time.Duration(a.Sleep) * time.Millisecond)
					return a.Text, nil
				}},
			{Name: "fail", Description: "Always fails.",
				Call: func(context.Context, jsontext.Value) (string, error) {
					return "", errors.New("the date must be in the future")
				}},
			{Name: "panic", Description: "Panics.",
				Call: func(context.Context, jsontext.Value) (string, error) {
					panic("oops")
				}},
			{Name: "file", Description: "Files something.", Call: func(context.Context, jsontext.Value) (string, error) {
				return "filed", nil
			}},
		}}
}

// pair connects a client to s in this process, over two pipes.
func pair(t *testing.T, s *Server) *Client {
	t.Helper()
	cr, sw := io.Pipe() // the server's output, the client's input
	sr, cw := io.Pipe() // the client's output, the server's input
	served := make(chan error, 1)
	go func() {
		served <- s.Serve(context.Background(), sr, sw)
		_ = sw.Close()
	}()
	c, err := Connect(t.Context(), Implementation{Name: "client", Version: "1"}, cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return c
}

func TestDiscover(t *testing.T) {
	c := pair(t, testServer())
	if c.Server().Name != "test" || c.Instructions() != "Tools for tests." || c.Legacy() {
		t.Errorf("server %+v, instructions %q, legacy %v", c.Server(),
			c.Instructions(), c.Legacy())
	}
}

// TestListPages checks that ListTools reads every page, in order, and
// that annotations follow the tools' safety flags.
func TestListPages(t *testing.T) {
	s := testServer()
	s.PageSize = 1
	tools, err := pair(t, s).ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, " "); got != "echo fail panic file" {
		t.Errorf("tools = %s", got)
	}
	a, b := tools[0].Annotations, tools[3].Annotations
	if !*a.ReadOnly || *a.Destructive || *b.ReadOnly || !*b.Destructive {
		t.Errorf("annotations: echo %+v, file %+v", a, b)
	}
	del := info(Tool{Name: "delete", Idempotent: true}).Annotations
	if !*del.Destructive || !*del.Idempotent {
		t.Errorf("an idempotent tool with side effects: %+v", del)
	}
}

// TestCallResults checks the two kinds of failure: a tool that fails or
// panics returns a result with IsError set; an unknown tool is a
// protocol error.
func TestCallResults(t *testing.T) {
	c := pair(t, testServer())
	ctx := t.Context()
	r, err := c.CallTool(ctx, "echo", jsontext.Value(`{"text":"hi"}`))
	if err != nil || r.IsError || r.Text() != "hi" {
		t.Errorf("echo = %+v, %v", r, err)
	}
	r, err = c.CallTool(ctx, "fail", nil)
	if err != nil || !r.IsError || r.Text() != "the date must be in the future" {
		t.Errorf("fail = %+v, %v", r, err)
	}
	r, err = c.CallTool(ctx, "panic", nil)
	if err != nil || !r.IsError || !strings.Contains(r.Text(), "panicked: oops") {
		t.Errorf("panic = %+v, %v", r, err)
	}
	_, err = c.CallTool(ctx, "nope", nil)
	if e, ok := errors.AsType[*Error](err); !ok || e.Code != CodeInvalidParams {
		t.Errorf("unknown tool: %v", err)
	}
}

// TestConcurrentCalls checks that responses reach the calls that made
// them when many calls are in flight and finish out of order.
func TestConcurrentCalls(t *testing.T) {
	c := pair(t, testServer())
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			args := fmt.Sprintf(`{"text":"call %d","sleep":%d}`, i, (i*7)%20)
			r, err := c.CallTool(t.Context(), "echo", jsontext.Value(args))
			if err != nil || r.Text() != fmt.Sprintf("call %d", i) {
				t.Errorf("call %d = %q, %v", i, r.Text(), err)
			}
		})
	}
	wg.Wait()
}

// TestCancel checks that a call past its timeout returns ErrTimeout,
// that the server stops the tool's work, and that the server goes on
// answering other calls.
func TestCancel(t *testing.T) {
	s := testServer()
	stopped := make(chan error, 1)
	s.Tools = append(s.Tools, Tool{Name: "hang", Call: func(ctx context.Context, _ jsontext.Value) (string, error) {
		<-ctx.Done()
		stopped <- context.Cause(ctx)
		return "", ctx.Err()
	}})
	c := pair(t, s)
	c.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := c.CallTool(t.Context(), "hang", nil)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hang: %v", err)
	}
	select {
	case cause := <-stopped:
		if !errors.Is(cause, errCancelled) {
			t.Errorf("the tool stopped for %v, not the client's cancellation", cause)
		}
		t.Logf("stopped %v after the call began", time.Since(start).Round(time.Millisecond))
	case <-time.After(5 * time.Second):
		t.Fatal("the server didn't stop the tool")
	}
	if r, err := c.CallTool(t.Context(), "echo", jsontext.Value(`{"text":"still here"}`)); err != nil || r.Text() != "still here" {
		t.Errorf("after the cancel: %q, %v", r.Text(), err)
	}
}

// raw sends one line to a server and returns the line it answers with.
func raw(t *testing.T, s *Server, line string) message {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	go func() { _ = s.Serve(t.Context(), sr, sw) }()
	go func() { _, _ = io.WriteString(cw, line+"\n") }()
	answer, err := readLine(bufio.NewReader(cr))
	_ = cw.Close()
	if err != nil {
		t.Fatal(err)
	}
	var m message
	if err := json.Unmarshal(answer, &m); err != nil {
		t.Fatalf("answer %q: %v", answer, err)
	}
	return m
}

// TestMeta checks that every request must carry its protocol version
// and capabilities, and that a version the server doesn't speak gets
// the versions it does.
func TestMeta(t *testing.T) {
	s := testServer()
	m := raw(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if m.Error == nil || m.Error.Code != CodeInvalidParams {
		t.Errorf("no _meta: %+v", m)
	}
	m = raw(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{`+
		`"io.modelcontextprotocol/protocolVersion":"1900-01-01",`+
		`"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if m.Error == nil || m.Error.Code != CodeUnsupportedVersion ||
		string(m.Error.Data) != `{"supported":["2026-07-28"],"requested":"1900-01-01"}` {
		t.Errorf("old version: %+v", m.Error)
	}
	m = raw(t, s, `{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	if m.Error == nil || m.Error.Code != CodeUnsupportedVersion {
		t.Errorf("initialize: %+v", m.Error)
	}
	m = raw(t, s, `not json`)
	if m.Error == nil || m.Error.Code != CodeParse || string(m.ID) != "null" {
		t.Errorf("not JSON: %+v", m)
	}
}

// TestFraming checks that a message holding newlines, in a string and
// between the tokens of a raw value, is still written as one line.
func TestFraming(t *testing.T) {
	var b strings.Builder
	c := newConn(strings.NewReader(""), &b)
	err := c.send(message{ID: jsontext.Value("1"),
		Result: jsontext.Value("{\n  \"text\": \"two\\nlines\"\n}")})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("sent %q", got)
	}
}

// legacyServer answers like a server of version 2025-11-25: server/discover
// is an unknown method, and initialize opens a session.
func legacyServer(r io.Reader, w io.WriteCloser, answerDiscover bool) {
	defer w.Close()
	c := newConn(r, w)
	for {
		m, err := c.receive()
		if err != nil {
			return
		}
		var result string
		switch m.Method {
		case "server/discover":
			if !answerDiscover {
				continue // some old servers never answer what they don't know
			}
			_ = c.send(message{ID: m.ID, Error: &Error{Code: CodeMethodNotFound,
				Message: "Method not found"}})
			continue
		case "initialize":
			result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"old","version":"0.9"}}`
		case "tools/list":
			result = `{"tools":[{"name":"search","inputSchema":{"type":"object"}}]}`
		default:
			continue // notifications/initialized and the like
		}
		_ = c.send(message{ID: m.ID, Result: jsontext.Value(result)})
	}
}

// TestLegacyFallback checks that a server that rejects server/discover,
// or doesn't answer it, gets the initialize handshake.
func TestLegacyFallback(t *testing.T) {
	probeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { probeTimeout = 5 * time.Second })
	for _, answer := range []bool{true, false} {
		cr, sw := io.Pipe()
		sr, cw := io.Pipe()
		go legacyServer(sr, sw, answer)
		c, err := Connect(t.Context(), Implementation{Name: "client", Version: "1"}, cr, cw)
		if err != nil {
			t.Fatalf("answers discover %v: %v", answer, err)
		}
		tools, err := c.ListTools(t.Context())
		if !c.Legacy() || c.Server().Name != "old" || err != nil || len(tools) != 1 {
			t.Errorf("answers discover %v: legacy %v, server %+v, tools %v, %v",
				answer, c.Legacy(), c.Server(), tools, err)
		}
		_ = c.Close()
	}
}

// TestServerGone checks that a call fails with ErrServerGone when the
// server's output ends, and that a server writing something other than
// messages to its output stops the client the same way.
func TestServerGone(t *testing.T) {
	for name, junk := range map[string]string{"closed": "", "stray output": "starting up...\n"} {
		cr, sw := io.Pipe()
		sr, cw := io.Pipe()
		s := testServer()
		go func() {
			br := bufio.NewReader(sr)
			c := newConn(br, sw)
			m, _ := c.receive() // server/discover: answer it properly
			result, _ := s.handle(t.Context(), m)
			_ = c.send(message{ID: m.ID, Result: result})
			if _, err := c.receive(); err != nil { // the tools/call
				return
			}
			_, _ = io.WriteString(sw, junk)
			_ = sw.Close()
		}()
		c, err := Connect(t.Context(), Implementation{Name: "client"}, cr, cw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.CallTool(t.Context(), "echo", nil)
		if !errors.Is(err, ErrServerGone) {
			t.Errorf("%s: %v", name, err)
		}
		t.Logf("%s: %v", name, err)
		_ = c.Close()
	}
}

// TestMalformed checks the answers to messages that aren't requests:
// JSON that isn't JSON-RPC 2.0, a batch, a request with no method, and
// IDs MCP doesn't allow.
func TestMalformed(t *testing.T) {
	s := testServer()
	for line, code := range map[string]int{
		`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`:       CodeInvalidRequest,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`:     CodeInvalidRequest,
		`{"jsonrpc":"2.0","id":1}`:                             CodeInvalidRequest,
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`:    CodeInvalidRequest,
		`{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`: CodeInvalidRequest,
		`{"jsonrpc":"2.0","id":1.5,"method":"tools/list"}`:     CodeInvalidRequest,
		`{"jsonrpc":`: CodeParse,
	} {
		m := raw(t, s, line)
		if m.Error == nil || m.Error.Code != code || string(m.ID) != "null" {
			t.Errorf("%s: %+v %s", line, m.Error, m.ID)
		}
	}
}

// TestVersionFirst checks that a version the server doesn't speak is
// reported as such even when the request lacks what that version needs.
func TestVersionFirst(t *testing.T) {
	m := raw(t, testServer(), `{"jsonrpc":"2.0","id":"a","method":"tools/list",`+
		`"params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}`)
	if m.Error == nil || m.Error.Code != CodeUnsupportedVersion || string(m.ID) != `"a"` {
		t.Errorf("%+v", m)
	}
}

// TestFinishOnEOF checks that a server whose input ends after a request
// still answers it, as when a request is piped to it by hand.
func TestFinishOnEOF(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{}},` +
		`"name":"echo","arguments":{"text":"done","sleep":50}}}` + "\n")
	var out strings.Builder
	if err := testServer().Serve(t.Context(), in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"text":"done"`) {
		t.Errorf("answer = %q", out.String())
	}
}

// scripted connects a client to a fake server that answers each request
// with reply, and returns the client and the error from Connect.
func scripted(t *testing.T, reply func(m message) (message, bool)) (*Client, error) {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() {
		defer sw.Close()
		c := newConn(sr, sw)
		for {
			m, err := c.receive()
			if err != nil {
				return
			}
			if len(m.ID) == 0 {
				continue
			}
			if r, ok := reply(m); ok {
				r.ID = m.ID
				_ = c.send(r)
			}
		}
	}()
	c, err := Connect(t.Context(), Implementation{Name: "client"}, cr, cw)
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

// TestNoFallback checks the two answers to server/discover after which
// the client must not fall back to initialize: a modern error naming
// other versions, and a result that doesn't list this client's.
func TestNoFallback(t *testing.T) {
	for name, answer := range map[string]message{
		"-32022": {Error: unsupported("2026-07-28")},
		"result": {Result: jsontext.Value(`{"resultType":"complete","supportedVersions":["2099-01-01"],` +
			`"capabilities":{},"ttlMs":0,"cacheScope":"public"}`)},
	} {
		initialized := false
		_, err := scripted(t, func(m message) (message, bool) {
			if m.Method == "initialize" {
				initialized = true
			}
			return answer, true
		})
		if !errors.Is(err, ErrVersion) || initialized {
			t.Errorf("%s: %v, initialize sent %v", name, err, initialized)
		}
	}
}

// TestServerRequests checks that a request from the server isn't taken
// for a response: an old server's ping gets an empty result, anything
// else method not found, and the call waiting is still answered.
func TestServerRequests(t *testing.T) {
	probeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { probeTimeout = 5 * time.Second })
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	answers := make(chan message, 2)
	go func() {
		defer sw.Close()
		c := newConn(sr, sw)
		for {
			m, err := c.receive()
			if err != nil {
				return
			}
			switch m.Method {
			case "server/discover":
				_ = c.send(message{ID: m.ID, Error: &Error{Code: CodeMethodNotFound, Message: "no"}})
			case "initialize":
				_ = c.send(message{ID: m.ID, Result: jsontext.Value(
					`{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"old","version":"1"}}`)})
			case "tools/list":
				// Ask the client two things before answering, with an ID that
				// is also the waiting call's.
				_ = c.send(message{ID: m.ID, Method: "ping"})
				_ = c.send(message{ID: jsontext.Value(`"x"`), Method: "roots/list"})
				answers <- mustReceive(c)
				answers <- mustReceive(c)
				_ = c.send(message{ID: m.ID, Result: jsontext.Value(`{"tools":[]}`)})
			case "":
			}
		}
	}()
	c, err := Connect(t.Context(), Implementation{Name: "client"}, cr, cw)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The two answers go from goroutines of their own, so they may come
	// in either order; match them by ID, as a server would.
	ping, roots := <-answers, <-answers
	if string(ping.ID) == `"x"` {
		ping, roots = roots, ping
	}
	if ping.Error != nil || string(ping.Result) != "{}" {
		t.Errorf("ping answer: %+v", ping)
	}
	if roots.Error == nil || roots.Error.Code != CodeMethodNotFound {
		t.Errorf("roots/list answer: %+v", roots)
	}
}

func mustReceive(c *conn) message {
	m, _ := c.receive()
	return m
}

// TestIncomplete checks that a result asking for input fails the call,
// for every method, not only tools/call.
func TestIncomplete(t *testing.T) {
	c, err := scripted(t, func(m message) (message, bool) {
		switch m.Method {
		case "server/discover":
			return message{Result: jsontext.Value(`{"resultType":"complete","supportedVersions":["2026-07-28"],` +
				`"capabilities":{},"ttlMs":0,"cacheScope":"public"}`)}, true
		default:
			return message{Result: jsontext.Value(`{"resultType":"input_required","inputRequests":{}}`)}, true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTools(t.Context()); !errors.Is(err, errIncomplete) {
		t.Errorf("tools/list: %v", err)
	}
	if _, err := c.CallTool(t.Context(), "x", nil); !errors.Is(err, errIncomplete) {
		t.Errorf("tools/call: %v", err)
	}
}

// TestCloseConnect checks that Close on a client from Connect ends a
// call that is waiting, without waiting for its timeout.
func TestCloseConnect(t *testing.T) {
	c, err := scripted(t, func(m message) (message, bool) {
		if m.Method == "server/discover" {
			return message{Result: jsontext.Value(`{"resultType":"complete","supportedVersions":["2026-07-28"],` +
				`"capabilities":{},"ttlMs":0,"cacheScope":"public"}`)}, true
		}
		return message{}, false // never answers anything else
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	go func() {
		_, err := c.CallTool(t.Context(), "slow", nil)
		failed <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = c.Close()
	select {
	case err := <-failed:
		if !errors.Is(err, ErrServerGone) {
			t.Errorf("call after Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close left the call waiting")
	}
}

// TestRequestState checks that a result asking only for its state back
// gets a new call with that state, and nothing more.
func TestRequestState(t *testing.T) {
	var states []string
	c, err := scripted(t, func(m message) (message, bool) {
		switch m.Method {
		case "server/discover":
			return message{Result: jsontext.Value(`{"resultType":"complete","supportedVersions":["2026-07-28"],` +
				`"capabilities":{},"ttlMs":0,"cacheScope":"public"}`)}, true
		case "tools/call":
			var p struct {
				State *string `json:"requestState"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.State == nil {
				states = append(states, "")
				return message{Result: jsontext.Value(`{"resultType":"input_required","requestState":"s1"}`)}, true
			}
			states = append(states, *p.State)
			return message{Result: jsontext.Value(`{"resultType":"complete","content":[{"type":"text","text":"done"}]}`)}, true
		}
		return message{}, false
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.CallTool(t.Context(), "x", nil)
	if err != nil || r.Text() != "done" || strings.Join(states, ",") != ",s1" {
		t.Errorf("CallTool = %q, %v; states sent %q", r.Text(), err, states)
	}
}

// TestModernError checks that another error MCP itself defines, from a
// current server, isn't taken for an old server's answer.
func TestModernError(t *testing.T) {
	initialized := false
	_, err := scripted(t, func(m message) (message, bool) {
		if m.Method == "initialize" {
			initialized = true
		}
		return message{Error: &Error{Code: -32021, Message: "missing capability"}}, true
	})
	if err == nil || initialized {
		t.Errorf("err %v, initialize sent %v", err, initialized)
	}
}
