package mcp

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"
)

// tag::server-tool[]

// A Tool is one tool a Server offers.
type Tool struct {
	Name        string
	Description string
	InputSchema jsontext.Value // a JSON Schema for the arguments

	// ReadOnly and Idempotent become the tool's annotations: chapter
	// 7's safety flags, as hints for the client.
	ReadOnly, Idempotent bool

	// Call runs the tool. An error means the tool ran and failed, such
	// as on bad argument values; the client gets it as a result with
	// isError set, for the model to read and correct.
	Call func(ctx context.Context, args jsontext.Value) (string, error)
}

// end::server-tool[]

// tag::server[]

// A Server offers tools over the protocol. It keeps no state between
// requests: each one carries what the server needs to answer it.
type Server struct {
	Info         Implementation
	Instructions string // for the model, sent by server/discover
	Tools        []Tool // listed in this order, the same every time

	// PageSize is how many tools one tools/list page holds; 0 means
	// every tool on one page.
	PageSize int

	// Log receives the server's log lines. A server on standard input
	// and output logs to standard error: anything on standard output
	// that isn't a message breaks the client.
	Log *log.Logger
}

// end::server[]

// errServerStopped is the cause of a request's cancellation when the
// server stops reading, such as when its input closes.
var errServerStopped = errors.New("mcp: server stopped")

// errBadID answers a message that is neither a request with an ID MCP
// allows, a string or an integer, nor a notification nor a response.
var errBadID = &Error{Code: CodeInvalidRequest,
	Message: "not a request with a string or integer ID"}

// tag::serve[]

// Serve answers requests from r on w until r ends, each request in a
// goroutine of its own, and waits for them all before it returns.
func (s *Server) Serve(ctx context.Context, r io.Reader,
	w io.Writer) error {
	ctx, stop := context.WithCancelCause(ctx)
	calls := &inflight{c: newConn(r, w),
		cancel: map[string]context.CancelCauseFunc{}}
	defer calls.wg.Wait()
	defer stop(errServerStopped) // runs first: cancels what is running
	for {
		m, err := calls.c.receive()
		if errors.Is(err, io.EOF) { // the client closed our input
			calls.finish(drainGrace) // then the defers cancel the rest
			return nil
		}
		if rpcErr, ok := errors.AsType[*Error](err); ok {
			s.reply(calls.c, jsontext.Value("null"), nil, rpcErr)
			continue
		}
		if err != nil {
			return fmt.Errorf("read request: %w", err)
		}
		s.dispatch(ctx, m, calls)
	}
}

// end::serve[]

// tag::dispatch[]

// dispatch acts on one message: a notification at once, a request in a
// goroutine of its own, and anything else with an error.
func (s *Server) dispatch(ctx context.Context, m message,
	calls *inflight) {
	switch {
	case m.Method != "" && len(m.ID) == 0:
		s.notified(m, calls)
	case m.Method != "" && validID(m.ID):
		s.start(ctx, m, calls)
	case m.Method == "" && (m.Result != nil || m.Error != nil):
		// a response, to a request this server never sends
	default:
		s.reply(calls.c, jsontext.Value("null"), nil, errBadID)
	}
}

// end::dispatch[]

// tag::inflight[]

// inflight holds the requests that are running, by ID, with the cancel
// function of each, so a notifications/cancelled can stop it.
type inflight struct {
	c  *conn
	wg sync.WaitGroup

	mu     sync.Mutex
	cancel map[string]context.CancelCauseFunc
}

// start runs a request in a goroutine of its own. A request that was
// cancelled gets no response: the client has stopped waiting for it.
func (s *Server) start(ctx context.Context, m message,
	calls *inflight) {
	id := canonicalID(m.ID)
	ctx, cancel := context.WithCancelCause(ctx)
	if !calls.add(id, cancel) {
		s.reply(calls.c, m.ID, nil, &Error{Code: CodeInvalidRequest,
			Message: "request ID " + id + " is already in use"})
		return
	}
	calls.wg.Go(func() {
		defer calls.remove(id)
		result, rpcErr := s.handle(ctx, m)
		if ctx.Err() == nil {
			s.reply(calls.c, m.ID, result, rpcErr)
		}
	})
}

// end::inflight[]

func (f *inflight) add(id string, cancel context.CancelCauseFunc) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.cancel[id]; ok {
		cancel(nil)
		return false
	}
	f.cancel[id] = cancel
	return true
}

func (f *inflight) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel[id](nil)
	delete(f.cancel, id)
}

// drainGrace is how long a server whose input has closed waits for the
// requests still running to answer, before it cancels them.
const drainGrace = 2 * time.Second

// finish waits up to d for the running requests to end.
func (f *inflight) finish(d time.Duration) {
	done := make(chan struct{})
	go func() { // ends once every request does, cancelled or not
		f.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// stop cancels a request, if it is still running. An unknown ID is
// ignored: the request may have finished as the client gave up on it.
func (f *inflight) stop(id string, cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cancel, ok := f.cancel[id]; ok {
		cancel(cause)
	}
}

// errCancelled is the cause of a request the client cancelled.
var errCancelled = errors.New("mcp: cancelled by the client")

// notified handles a notification. The only one this server acts on is
// notifications/cancelled; others are ignored, as the protocol allows.
func (s *Server) notified(m message, calls *inflight) {
	if m.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID jsontext.Value `json:"requestId"`
		Reason    string         `json:"reason"`
	}
	err := json.Unmarshal(m.Params, &p)
	if err != nil || len(p.RequestID) == 0 {
		return // a malformed notification is ignored
	}
	id := canonicalID(p.RequestID)
	s.logf("request %s cancelled: %s", id, p.Reason)
	calls.stop(id, fmt.Errorf("%w: %s", errCancelled, p.Reason))
}

// reply sends a response, logging a failure to send: the client has
// gone, and Serve finds out when its input ends.
func (s *Server) reply(c *conn, id, result jsontext.Value,
	rpcErr *Error) {
	m := message{ID: id, Result: result, Error: rpcErr}
	if rpcErr == nil && result == nil {
		m.Result = jsontext.Value("{}")
	}
	if err := c.send(m); err != nil {
		s.logf("reply to %s: %v", id, err)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

// tag::handle[]

// handle answers one request. Each must say, in its _meta, which
// version it speaks and what the client can do: the server remembers
// nothing from earlier requests.
func (s *Server) handle(ctx context.Context, m message) (
	jsontext.Value, *Error) {
	if m.Method == "initialize" { // the handshake of earlier versions
		return nil, legacyRefused(m.Params)
	}
	var p requestParams
	if err := json.Unmarshal(orJSON(m.Params, "{}"), &p); err != nil {
		return nil, &Error{Code: CodeInvalidParams,
			Message: err.Error()}
	}
	if rpcErr := checkMeta(p.Meta); rpcErr != nil {
		return nil, rpcErr
	}
	switch m.Method {
	case "server/discover":
		return s.result(s.discover())
	case "tools/list":
		return s.list(p.Cursor)
	case "tools/call":
		return s.call(ctx, p.Name, p.Arguments)
	}
	return nil, &Error{Code: CodeMethodNotFound,
		Message: "no method " + m.Method}
}

// end::handle[]

// requestParams holds the params of every request this server answers.
type requestParams struct {
	Meta      *requestMeta   `json:"_meta"`
	Name      string         `json:"name"`
	Arguments jsontext.Value `json:"arguments"`
	Cursor    *string        `json:"cursor"`
}

// tag::check-meta[]

// checkMeta checks the _meta a request must carry. The version comes
// first: what else a request must carry depends on it, and a version
// the server doesn't speak gets the list of versions it does, so the
// client can retry with one of them.
func checkMeta(meta *requestMeta) *Error {
	switch {
	case meta == nil || meta.Version == "":
		return &Error{Code: CodeInvalidParams,
			Message: "the request has no _meta." + metaVersion}
	case meta.Version != Version:
		return unsupported(meta.Version)
	case meta.Capabilities == nil:
		return &Error{Code: CodeInvalidParams,
			Message: "the request has no _meta." + metaCapabilities}
	}
	return nil
}

// end::check-meta[]

// unsupported is the error for a protocol version the server doesn't
// speak, listing the one it does.
func unsupported(requested string) *Error {
	data, _ := rawJSON(struct { // two strings: this can't fail
		Supported []string `json:"supported"`
		Requested string   `json:"requested"`
	}{[]string{Version}, requested})
	return &Error{Code: CodeUnsupportedVersion,
		Message: "unsupported protocol version " + strconv.Quote(requested),
		Data:    data}
}

// legacyRefused answers an initialize request. This server speaks only
// the stateless version, and says which, since a client of the older
// versions can't find out any other way.
func legacyRefused(params jsontext.Value) *Error {
	var p struct {
		Version string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p) // a missing version is reported as ""
	return unsupported(p.Version)
}

// result encodes a result, with the server's identity in its _meta.
func (s *Server) result(v any) (jsontext.Value, *Error) {
	b, err := rawJSON(v)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return b, nil
}

func (s *Server) base() resultBase {
	return resultBase{ResultType: "complete",
		Meta: &resultMeta{Server: &s.Info}}
}

// toolsTTL is how long a client may reuse the tool list: the tools are
// fixed for as long as the process runs.
const toolsTTL = 3_600_000 // an hour, in milliseconds

func (s *Server) discover() DiscoverResult {
	return DiscoverResult{resultBase: s.base(),
		TTL: toolsTTL, Scope: "public",
		SupportedVersions: []string{Version},
		Capabilities:      jsontext.Value(`{"tools":{"listChanged":false}}`),
		Instructions:      s.Instructions}
}

// tag::list[]

// list returns one page of the tools. The cursor is the index of the
// page's first tool, as a string; clients treat it as opaque.
func (s *Server) list(cursor *string) (jsontext.Value, *Error) {
	start := 0
	if cursor != nil {
		n, err := strconv.Atoi(*cursor)
		if err != nil || n < 0 || n > len(s.Tools) {
			return nil, &Error{Code: CodeInvalidParams,
				Message: "invalid cursor " + strconv.Quote(*cursor)}
		}
		start = n
	}
	end := len(s.Tools)
	if s.PageSize > 0 {
		end = min(start+s.PageSize, end)
	}
	page := listToolsResult{resultBase: s.base(),
		TTL: toolsTTL, Scope: "public", Tools: []ToolInfo{}}
	for _, t := range s.Tools[start:end] {
		page.Tools = append(page.Tools, info(t))
	}
	if end < len(s.Tools) {
		next := strconv.Itoa(end)
		page.NextCursor = &next
	}
	return s.result(page)
}

// end::list[]

// info describes a tool for tools/list. A tool with no side effects is
// read-only, idempotent, and not destructive.
func info(t Tool) ToolInfo {
	return ToolInfo{Name: t.Name, Description: t.Description,
		InputSchema: orJSON(t.InputSchema, `{"type":"object"}`),
		Annotations: &Annotations{ReadOnly: new(t.ReadOnly),
			Destructive: new(!t.ReadOnly), // idempotent can still delete
			Idempotent:  new(t.ReadOnly || t.Idempotent),
			OpenWorld:   new(false)}}
}

// tag::call[]

// call runs a tool. An unknown tool is a protocol error; a tool that
// fails, or panics, returns a result with isError set, which the client
// passes to the model.
func (s *Server) call(ctx context.Context, name string,
	args jsontext.Value) (jsontext.Value, *Error) {
	i := slices.IndexFunc(s.Tools, func(t Tool) bool {
		return t.Name == name
	})
	if i < 0 {
		return nil, &Error{Code: CodeInvalidParams,
			Message: "unknown tool: " + name}
	}
	out, err := runTool(ctx, s.Tools[i], orJSON(args, "{}"))
	result := struct {
		resultBase
		CallResult
	}{resultBase: s.base(),
		Content: []Content{{Type: "text", Text: out}}}
	if err != nil {
		result.Content[0].Text, result.IsError = err.Error(), true
		s.logf("%s failed: %v", name, err)
	}
	return s.result(result)
}

// end::call[]

// runTool runs a tool, turning a panic into an error: one bad tool
// doesn't take the server down with it.
func runTool(ctx context.Context, t Tool,
	args jsontext.Value) (out string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panicked: %v", t.Name, p)
		}
	}()
	return t.Call(ctx, args)
}

// canonicalID is a request ID in one spelling, so 7 and 7 written with
// different spacing are the same request.
func canonicalID(id jsontext.Value) string {
	c := id.Clone()
	if err := c.Compact(); err != nil {
		return string(id)
	}
	return string(c)
}
