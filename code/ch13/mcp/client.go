package mcp

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors a caller checks for.
var (
	// ErrServerGone means the server stopped: its process exited, or
	// its output closed or stopped being protocol messages. No call to
	// it can succeed until it is started again.
	ErrServerGone = errors.New("mcp: the server stopped")

	// ErrTimeout is the cause of a call that ran past Client.Timeout.
	ErrTimeout = errors.New("mcp: the call timed out")

	// ErrVersion means the client and the server have no protocol
	// version in common.
	ErrVersion = errors.New("mcp: no protocol version in common")
)

// tag::client[]

// A Client talks to one server. Its methods are safe to call from
// several goroutines at once: each call waits for its own response.
type Client struct {
	// Timeout is how long one call may take; 0 means a minute. A call
	// that runs past it is cancelled at the server too.
	Timeout time.Duration

	info    Implementation // the client's, sent on each request
	server  Implementation // the server's, from its first reply
	about   string         // the server's instructions for the model
	version string         // the protocol version in use
	legacy  atomic.Bool    // the server speaks a version before Version

	c      *conn
	in     io.Closer // the server's input; closing it asks it to exit
	out    io.Reader // the server's output
	next   atomic.Int64
	mu     sync.Mutex
	wait   map[int64]chan message // calls waiting for a response, by ID
	done   chan struct{}          // closed when reading stops
	err    error                  // why reading stopped
	stderr *tail                  // the end of its standard error

	proc      *exec.Cmd
	closeOnce sync.Once
	closeErr  error
}

// end::client[]

// Connect connects to a server on r and w, which carry its output and
// its input, and finds out which protocol version it speaks. Start is
// the usual way in; Connect is for a server already running, such as
// one in the same process in tests.
func Connect(ctx context.Context, info Implementation, r io.Reader,
	w io.WriteCloser) (*Client, error) {
	c := newClient(info, r, w, newTail())
	if err := c.connect(ctx); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func newClient(info Implementation, r io.Reader, w io.WriteCloser,
	stderr *tail) *Client {
	c := &Client{info: info, version: Version, c: newConn(r, w), in: w,
		out: r, wait: map[int64]chan message{}, done: make(chan struct{}),
		stderr: stderr}
	go c.read() // stops when the server's output ends; Close ends it
	return c
}

// errStray is why a client stops reading a server that wrote something
// other than a protocol message to its output.
var errStray = errors.New("it wrote a line that isn't a protocol " +
	"message to its output")

// tag::read[]

// read reads the server's messages until its output ends. A line that
// isn't a protocol message stops the client: a server that writes
// anything else to standard output can't be read reliably.
func (c *Client) read() {
	var err error
	for {
		var m message
		if m, err = c.c.receive(); err != nil {
			break
		}
		c.route(m)
	}
	if _, ok := errors.AsType[*Error](err); ok {
		err = errStray
	}
	c.stderr.wait(stderrWait) // its reason may still be coming
	c.err = fmt.Errorf("%w: %w%s", ErrServerGone, err, c.stderr.last())
	close(c.done)
}

// end::read[]

// tag::route[]

// route hands a response to the call waiting for it. A response no call
// waits for, such as one to a call already cancelled, is dropped. A
// request from the server is answered from a goroutine of its own,
// which ends when the send does, so the reader never waits on a server
// that isn't reading.
func (c *Client) route(m message) {
	switch {
	case len(m.ID) == 0:
		return // a notification: this client asks for none
	case m.Method != "":
		go c.answer(m)
		return
	}
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		return // not one of this client's IDs, which are numbers
	}
	c.mu.Lock()
	ch, ok := c.wait[id]
	delete(c.wait, id)
	c.mu.Unlock()
	if ok {
		ch <- m // buffered: never blocks
	}
}

// end::route[]

// stderrWait is how long read waits, once the server's output ends, for
// the rest of its standard error.
const stderrWait = 100 * time.Millisecond

// answer replies to a request from the server. The current version has
// servers send no requests on standard input and output; a server of an
// earlier version may send ping, to check the client is there, which
// gets an empty result. Anything else is a method this client doesn't
// have.
func (c *Client) answer(m message) {
	reply := message{ID: m.ID, Error: &Error{Code: CodeMethodNotFound,
		Message: "this client offers no " + m.Method}}
	if m.Method == "ping" && c.legacy.Load() {
		reply = message{ID: m.ID, Result: jsontext.Value("{}")}
	}
	_ = c.c.send(reply) // a send that fails means the server has gone
}

// tag::call-method[]

// call sends a request and waits for its response, the call's timeout,
// or ctx, whichever comes first. A call that gives up tells the server,
// with notifications/cancelled, so the server can stop its work.
func (c *Client) call(ctx context.Context, method string,
	params, result any) error {
	ctx, cancel := context.WithTimeoutCause(ctx,
		cmp.Or(c.Timeout, time.Minute), ErrTimeout)
	defer cancel()
	p, err := rawJSON(params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	id := c.next.Add(1)
	ch := c.expect(id)
	defer c.forget(id)
	idJSON := jsontext.Value(strconv.FormatInt(id, 10))
	err = c.c.send(message{ID: idJSON, Method: method, Params: p})
	if err != nil {
		return fmt.Errorf("%s: %w: %w", method, ErrServerGone, err)
	}
	select {
	case m := <-ch:
		return decodeResponse(method, m, result)
	case <-c.done:
		return fmt.Errorf("%s: %w", method, c.err)
	case <-ctx.Done():
		if method != "initialize" { // older versions forbid this
			c.cancelCall(idJSON, context.Cause(ctx))
		}
		return fmt.Errorf("%s: %w", method, ctxErr(ctx))
	}
}

// end::call-method[]

// expect registers a call waiting for the response to request id.
func (c *Client) expect(id int64) chan message {
	ch := make(chan message, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wait[id] = ch
	return ch
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.wait, id)
}

// cancelCall tells the server a call was abandoned. It is a
// notification, so there is no answer to wait for; a failure to send it
// means the server has gone, which the next call finds out.
func (c *Client) cancelCall(id jsontext.Value, cause error) {
	p, err := rawJSON(struct {
		RequestID jsontext.Value `json:"requestId"`
		Reason    string         `json:"reason"`
	}{id, cause.Error()})
	if err == nil {
		_ = c.c.send(message{Method: "notifications/cancelled", Params: p})
	}
}

// ctxErr is a context's error with its cause, when the cause says more.
func ctxErr(ctx context.Context) error {
	if cause := context.Cause(ctx); !errors.Is(ctx.Err(), cause) {
		return fmt.Errorf("%w: %w", ctx.Err(), cause)
	}
	return ctx.Err() //nolint:wrapcheck // returned as is, for errors.Is
}

func decodeResponse(method string, m message, result any) error {
	if m.Error != nil {
		return fmt.Errorf("%s: %w", method, m.Error)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(m.Result, result); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

// meta is the _meta for a request: the protocol version and the
// client's name and capabilities, none. A server of an earlier version
// gets none, since its session already holds all of that.
func (c *Client) meta() *requestMeta {
	if c.legacy.Load() {
		return nil
	}
	none := jsontext.Value("{}")
	return &requestMeta{Version: c.version, Capabilities: &none,
		Client: &c.info}
}

// probeTimeout is how long connect waits for an answer to
// server/discover before taking the server for an older one that
// doesn't answer methods it doesn't know.
var probeTimeout = 5 * time.Second

// tag::probe[]

// connect finds out which protocol the server speaks. A server of
// version 2026-07-28 or later answers server/discover; an older one
// answers with an error of its own, or not at all, and gets the
// initialize handshake those versions use.
func (c *Client) connect(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	d, err := c.Discover(pctx)
	rpcErr, isRPC := errors.AsType[*Error](err)
	switch {
	case err == nil && !slices.Contains(d.SupportedVersions, Version):
		return fmt.Errorf("%w: the server speaks %s; this client, %s",
			ErrVersion, strings.Join(d.SupportedVersions, ", "),
			Version)
	case err == nil:
		c.server, c.about = serverInfo(d.resultBase), d.Instructions
		return nil
	case isRPC && rpcErr.Code == CodeUnsupportedVersion:
		return fmt.Errorf("%w: %w", ErrVersion, rpcErr)
	case isRPC && modern(rpcErr.Code): // a current server, refusing
		return fmt.Errorf("server/discover: %w", rpcErr)
	case errors.Is(err, ErrServerGone) ||
		errors.Is(err, errIncomplete) || ctx.Err() != nil:
		return err
	}
	return c.initialize(ctx) // legacy.go
}

// end::probe[]

// modern reports whether an error code is one MCP itself defines, from
// -32099 to -32020, which only a server of the current version sends.
func modern(code int) bool { return code >= -32099 && code <= -32020 }

func serverInfo(b resultBase) Implementation {
	if b.Meta == nil || b.Meta.Server == nil {
		return Implementation{}
	}
	return *b.Meta.Server
}

// Server is the server's name and version, as it reports them.
func (c *Client) Server() Implementation { return c.server }

// Instructions is the server's guidance for the model, if it sent any.
// Like its tool descriptions, it comes from the server, not from you,
// and nothing checks it: the book's agent doesn't put it in the
// prompt, and one that does should strip, cap, label, and pin it as
// mcptools does a description.
func (c *Client) Instructions() string { return c.about }

// Legacy reports whether the server speaks a protocol version before
// Version, reached through the initialize handshake.
func (c *Client) Legacy() bool { return c.legacy.Load() }

// Discover asks the server which protocol versions and capabilities it
// has.
func (c *Client) Discover(ctx context.Context) (DiscoverResult, error) {
	var d DiscoverResult
	err := c.call(ctx, "server/discover", struct {
		Meta *requestMeta `json:"_meta,omitzero"`
	}{c.meta()}, &d)
	if err == nil {
		err = complete("server/discover", d.resultBase)
	}
	return d, err
}

// errIncomplete means a result asks for something before it is done: a
// question for the person, say. This client declares none of the
// capabilities a server would need to ask through, so it can't answer.
var errIncomplete = errors.New("the server asked for input this " +
	"client can't give")

// complete checks a result is a finished one: resultType "complete",
// or absent, which a server of an earlier version leaves it.
func complete(method string, b resultBase) error {
	if b.ResultType == "" || b.ResultType == "complete" {
		return nil
	}
	return fmt.Errorf("%s: %w (resultType %q)", method, errIncomplete,
		b.ResultType)
}

// tag::list-tools[]

// maxPages bounds how many pages ListTools reads: a server that keeps
// handing out cursors would otherwise keep the client reading forever.
const maxPages = 100

// ListTools returns every tool the server offers, reading each page of
// the list. Two tools with the same name are an error: the protocol
// says names are unique within a server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var tools []ToolInfo
	var cursor *string
	for range maxPages {
		var page listToolsResult
		err := c.call(ctx, "tools/list", struct {
			Meta   *requestMeta `json:"_meta,omitzero"`
			Cursor *string      `json:"cursor,omitzero"`
		}{c.meta(), cursor}, &page)
		if err == nil {
			err = complete("tools/list", page.resultBase)
		}
		if err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == nil {
			return tools, unique(tools)
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("tools/list: more than %d pages", maxPages)
}

// end::list-tools[]

func unique(tools []ToolInfo) error {
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t.Name] {
			return fmt.Errorf("tools/list: two tools named %q", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

// maxRetries is how many times CallTool sends a call again for a
// server that asked only to have its state back.
const maxRetries = 3

// tag::call-tool[]

// CallTool calls a tool. A server may answer that it needs more first,
// with resultType "input_required": requests for the client to fulfill,
// or only an opaque requestState to send back. This client fulfills no
// requests, so those are an error; a requestState alone it echoes on a
// new call, as the protocol allows.
func (c *Client) CallTool(ctx context.Context, name string,
	args jsontext.Value) (CallResult, error) {
	var state *string
	for range maxRetries + 1 {
		var r callReply
		err := c.call(ctx, "tools/call", callParams{c.meta(), name,
			orJSON(args, "{}"), state}, &r)
		if err != nil {
			return CallResult{}, err
		}
		if r.ResultType != "input_required" {
			err := complete("tools/call "+name, r.resultBase)
			return r.CallResult, err
		}
		if len(r.InputRequests) > 0 || r.RequestState == nil {
			return CallResult{}, fmt.Errorf("tools/call %s: %w", name,
				errIncomplete)
		}
		state = r.RequestState
	}
	return CallResult{}, fmt.Errorf("tools/call %s: %w after %d "+
		"retries", name, errIncomplete, maxRetries)
}

// end::call-tool[]

type callParams struct {
	Meta         *requestMeta   `json:"_meta,omitzero"`
	Name         string         `json:"name"`
	Arguments    jsontext.Value `json:"arguments"`
	RequestState *string        `json:"requestState,omitzero"`
}

type callReply struct {
	resultBase
	CallResult
	InputRequests map[string]jsontext.Value `json:"inputRequests"`
	RequestState  *string                   `json:"requestState"`
}

// tag::text[]

// Text is the result as one string for the model: each text item, and
// a short note for any other item, which a tool message can't carry.
func (r CallResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		switch {
		case c.Type == "text":
			parts = append(parts, c.Text)
		case c.URI != "":
			parts = append(parts,
				fmt.Sprintf("[a %s at %s]", c.Type, c.URI))
		default:
			parts = append(parts, fmt.Sprintf(
				"[%s content, %s, not shown]", c.Type,
				cmp.Or(c.MimeType, "of unknown type")))
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 {
		return string(r.StructuredContent)
	}
	return strings.Join(parts, "\n")
}

// end::text[]

// tag::command[]

// A Command says how to start a server: the program, its arguments, and
// its whole environment, of which nothing is inherited, so a server
// never sees your API keys unless you hand them over by name.
type Command struct {
	Path string
	Args []string
	Env  []string  // the server's entire environment; see BaseEnv
	Dir  string    // its working directory; "" means this process's
	Log  io.Writer // gets the server's standard error, if set
}

// BaseEnv is an environment with what most programs need to run, taken
// from this process's, plus extra ("NAME=value" pairs).
func BaseEnv(extra ...string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
		"USERPROFILE", "SYSTEMROOT", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

// end::command[]

// tag::start[]

// Start starts a server as a child process and connects to it over its
// standard input and output. The server stops when ctx ends or Close is
// called, whichever comes first.
func Start(ctx context.Context, cmd Command,
	info Implementation) (*Client, error) {
	// Not exec.CommandContext: its cancel kills the process, and the
	// protocol asks a client to close the server's input first. Close
	// does that, then kills; ctx ending calls Close.
	p := exec.Command(cmd.Path, cmd.Args...) //nolint:noctx // see above
	p.Env = append([]string{}, cmd.Env...)   // nil would inherit ours
	p.Dir = cmd.Dir
	stdin, stdout, stderr, err := pipes(p)
	if err == nil {
		err = p.Start()
	}
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	t := &tail{done: make(chan struct{})}
	go t.drain(stderr, cmd.Log) // ends when the process does
	c := newClient(info, stdout, stdin, t)
	c.proc = p
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	if err := c.connect(ctx); err != nil {
		stop()
		err = fmt.Errorf("connect to %s: %w", cmd.Path, err)
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

// end::start[]

// pipes connects to a command's standard input, output, and error.
func pipes(p *exec.Cmd) (io.WriteCloser, io.ReadCloser, io.ReadCloser,
	error) {
	stdin, err := p.StdinPipe()
	if err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // Start wraps it
	}
	stdout, err := p.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // Start wraps it
	}
	stderr, err := p.StderrPipe()
	return stdin, stdout, stderr, err //nolint:wrapcheck // Start wraps it
}

// shutdownGrace is how long Close waits for a server to exit after its
// input closes, before killing it.
const shutdownGrace = 5 * time.Second

// tag::close[]

// Close stops the server, in the order the protocol asks: close its
// input, which tells it to exit; wait for it; and kill it if it hasn't
// exited after shutdownGrace. Calls still waiting fail with
// ErrServerGone.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.in.Close()
		if c.proc == nil { // Connect: no process to wait for
			if out, ok := c.out.(io.Closer); ok {
				c.closeErr = errors.Join(c.closeErr, out.Close())
			}
			return
		}
		select { // its output ends when it exits
		case <-c.done:
		case <-time.After(shutdownGrace):
			c.closeErr = errors.Join(c.closeErr, c.proc.Process.Kill())
			select { // a child of the server may still hold its output
			case <-c.done:
			case <-time.After(shutdownGrace):
			}
		}
		c.stderr.wait(stderrWait)
		_ = c.proc.Wait() // after the reads, as os/exec asks; the exit
		// status of a server told to stop says nothing more
	})
	return c.closeErr
}

// end::close[]

// A tail keeps the last lines a server wrote to standard error, to say
// in an error why the server stopped.
type tail struct {
	mu    sync.Mutex
	lines []string
	done  chan struct{} // closed when its standard error ends
}

// newTail returns a tail with nothing to drain, for Connect.
func newTail() *tail {
	t := &tail{done: make(chan struct{})}
	close(t.done)
	return t
}

// wait waits up to d for the standard error to end.
func (t *tail) wait(d time.Duration) {
	select {
	case <-t.done:
	case <-time.After(d):
	}
}

const tailLines = 5

// drain reads r until it ends, keeping the last lines and copying every
// line to log, if set.
func (t *tail) drain(r io.Reader, log io.Writer) {
	defer close(t.done)
	br := bufio.NewReader(r)
	for {
		line, err := readLine(br)
		if err != nil {
			return
		}
		if log != nil {
			_, _ = fmt.Fprintf(log, "%s\n", line) // logging is best effort
		}
		t.mu.Lock()
		t.lines = append(t.lines, string(line))
		if len(t.lines) > tailLines {
			t.lines = t.lines[1:]
		}
		t.mu.Unlock()
	}
}

// last is the kept lines as a suffix for an error message.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return ""
	}
	return "; its last words on standard error: " +
		strings.Join(t.lines, " | ")
}
