// Package replay records model API calls to files and replays them, so
// programs and tests can run with no network, no key, and no cost.
package replay

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// ErrNoRecording means a call had no saved response: the program made a
// call the recording doesn't have.
var ErrNoRecording = errors.New("replay: no saved response")

// A recording is one saved call: the request body that was sent and the
// response that came back. Headers aren't saved, so the API key never
// reaches a file. A response body that isn't JSON, such as a gateway's
// HTML error page, is saved as text instead.
type recording struct {
	Status       int            `json:"status"`
	Request      jsontext.Value `json:"request"`
	Response     jsontext.Value `json:"response,omitzero"`
	ResponseText string         `json:"response_text,omitzero"`
}

// body returns the saved response body.
func (rec recording) body() []byte {
	if rec.Response != nil {
		return rec.Response
	}
	return []byte(rec.ResponseText)
}

type scriptKey struct{}

// tag::script[]

// WithScript names the script that calls made with ctx belong to.
// Each script is a sequence of saved calls, replayed in order.
func WithScript(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, scriptKey{}, name)
}

func scriptOf(ctx context.Context) string {
	name, _ := ctx.Value(scriptKey{}).(string)
	if name == "" {
		return "default"
	}
	return name
}

// end::script[]

// name is the file name of the nth saved call (from 1) in a script.
func name(script string, n int) string {
	return fmt.Sprintf("%s-%02d.json", script, n)
}

// A counter numbers calls per script. The zero value is ready to use.
type counter struct {
	mu sync.Mutex
	n  map[string]int
}

// next returns the number of this call in script, starting at 1.
func (c *counter) next(script string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[script]++
	return c.n[script]
}

// set makes n the number of the call just made in script.
func (c *counter) set(script string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[script] = n
}

// tag::transport[]

// Transport is an http.RoundTripper that answers each request with the
// next saved response for its script, read from Files. The first call
// of a script in a process finds its place in the script, so a run
// resumed partway, in a new process, replays from where it is.
type Transport struct {
	Files fs.FS // saved calls, such as an embedded testdata directory
	calls counter
}

// RoundTrip serves the next saved response.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := drain(req) // <1>
	if err != nil {
		return nil, err
	}
	ctx := req.Context()
	if err := context.Cause(ctx); err != nil {
		return nil, fmt.Errorf("replay: %w", err) // <2>
	}
	script := scriptOf(ctx)
	n := t.calls.next(script)
	if n == 1 { // this process's first call: find its place
		if n, err = find(t.Files, script, body); err != nil {
			return nil, err
		}
		t.calls.set(script, n)
	}
	rec, err := load(t.Files, script, n)
	if err != nil {
		return nil, err
	}
	return rec.response(req), nil
}

// end::transport[]

// find returns the number of the saved call in script that body, a
// process's first request for it, should get: the call recorded with
// the same request; or else the call whose request ends with the same
// assistant message, as a resumed run's first request does, with the
// assistant messages before it deciding between several. A request
// with no assistant message that matches no call is a new run's, and
// gets the first call; one with an assistant message that matches no
// call, or several alike, is an error, never another call's reply.
func find(files fs.FS, script string, body []byte) (int, error) {
	want, mine := compact(body), assistants(body)
	var last, whole []int // calls ending as mine does; with all of mine
	for n := 1; ; n++ {
		rec, err := load(files, script, n)
		if errors.Is(err, ErrNoRecording) {
			break
		}
		if err != nil {
			return 0, err
		}
		if bytes.Equal(compact(rec.Request), want) {
			return n, nil
		}
		theirs := assistants(rec.Request)
		if len(mine) == 0 || len(theirs) == 0 ||
			!bytes.Equal(theirs[len(theirs)-1], mine[len(mine)-1]) {
			continue
		}
		last = append(last, n)
		if slices.EqualFunc(theirs, mine, bytes.Equal) {
			whole = append(whole, n)
		}
	}
	switch {
	case len(mine) == 0:
		return 1, nil
	case len(whole) == 1:
		return whole[0], nil
	case len(whole) == 0 && len(last) == 1:
		return last[0], nil
	case len(last) == 0:
		return 0, fmt.Errorf("%w: no recorded call of script %q "+
			"matches this conversation", ErrNoRecording, script)
	}
	return 0, fmt.Errorf("replay: calls %v of script %q all match this "+
		"conversation", last, script)
}

// compact returns body compacted, or body as it is if it isn't JSON.
func compact(body []byte) []byte {
	c := jsontext.Value(slices.Clone(body))
	if c.Compact() != nil {
		return body
	}
	return c
}

// assistants returns the assistant messages of a chat request, in
// order and compacted.
func assistants(body []byte) [][]byte {
	var req struct {
		Messages []jsontext.Value `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	var out [][]byte
	for _, m := range req.Messages {
		var msg struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(m, &msg) != nil || msg.Role != "assistant" {
			continue
		}
		out = append(out, compact(m))
	}
	return out
}

// load reads the nth saved call of a script.
func load(files fs.FS, script string, n int) (recording, error) {
	var rec recording
	data, err := fs.ReadFile(files, name(script, n))
	if errors.Is(err, fs.ErrNotExist) {
		return rec, fmt.Errorf("%w: call %d of script %q",
			ErrNoRecording, n, script)
	}
	if err != nil {
		return rec, fmt.Errorf("replay: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("replay %s: %w", name(script, n), err)
	}
	return rec, nil
}

// response rebuilds the saved response as an answer to req.
func (rec recording) response(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: rec.Status,
		Status: fmt.Sprintf("%d %s", rec.Status,
			http.StatusText(rec.Status)),
		Header:  http.Header{"Content-Type": {"application/json"}},
		Body:    io.NopCloser(bytes.NewReader(rec.body())),
		Request: req,
	}
}

// drain reads and closes the request body, as the RoundTripper contract
// requires, and returns what it read.
func drain(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read request: %w", err)
	}
	return body, nil
}

// tag::recorder[]

// Recorder is an http.RoundTripper that sends each request on through
// Next and saves the request body and the response to Dir, numbered by
// script, for Transport to replay later.
type Recorder struct {
	Dir   string
	Next  http.RoundTripper // http.DefaultTransport if nil
	calls counter
}

// end::recorder[]

// tag::record[]

// RoundTrip sends the request and saves the exchange. Calls are
// numbered as they're sent, so each script must be one conversation,
// making one call at a time.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	script := scriptOf(req.Context())
	body, err := drain(req)
	if err != nil {
		return nil, err
	}
	n := r.calls.next(script)
	if n == 1 { // this process's first call: find its place
		if n, err = r.place(script, body); err != nil {
			return nil, err
		}
		r.calls.set(script, n)
	}
	out := req.Clone(req.Context()) // <1>
	out.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := r.transport().RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	resp.Body = &tee{body: resp.Body, save: func(data []byte) error { // <2>
		return r.save(name(script, n), resp.StatusCode, body, data) // <3>
	}}
	return resp, nil
}

// end::record[]

// tee passes a response body through as the client reads it, so a
// stream arrives as it is written, and saves the body once the client
// has read it to the end or closed it. A body whose read failed, such
// as a cancelled stream, isn't saved.
type tee struct {
	body io.ReadCloser
	buf  bytes.Buffer
	save func([]byte) error
	done bool // saved, or not to be saved
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.body.Read(p)
	t.buf.Write(p[:n])
	switch {
	case errors.Is(err, io.EOF):
		if serr := t.finish(); serr != nil {
			return n, serr
		}
	case err != nil:
		t.done = true
	}
	return n, err //nolint:wrapcheck // a Reader returns io.EOF as is
}

// Close saves what was read, if it hasn't been saved, and closes the
// body. A stream reader may stop at its last event and close the body
// without reading to the end.
func (t *tee) Close() error {
	return errors.Join(t.finish(), t.body.Close())
}

func (t *tee) finish() error {
	if t.done {
		return nil
	}
	t.done = true
	return t.save(t.buf.Bytes())
}

// place returns the number a process's first call of script is saved
// under. A new run's starts at 1, replacing an earlier recording. A
// resumed run's continues where its first process left off: the call
// whose request it repeats, which that process may not have finished,
// or else the call after the last one saved.
func (r *Recorder) place(script string, body []byte) (int, error) {
	n, err := find(os.DirFS(r.Dir), script, body)
	if !errors.Is(err, ErrNoRecording) {
		return n, err
	}
	names, err := filepath.Glob(filepath.Join(r.Dir, script+"-*.json"))
	if err != nil {
		return 0, fmt.Errorf("record: %w", err)
	}
	last := 0
	for _, f := range names {
		num := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f),
			script+"-"), ".json")
		if k, err := strconv.Atoi(num); err == nil && k > 0 {
			last = max(last, k)
		}
	}
	return last + 1, nil
}

func (r *Recorder) transport() http.RoundTripper {
	if r.Next == nil {
		return http.DefaultTransport
	}
	return r.Next
}

// save writes one call to Dir. Only the request body is saved, never
// the headers, so the API key stays out of the file.
func (r *Recorder) save(file string, status int, req, resp []byte) error {
	rec := recording{Status: status, Request: req}
	if !rec.Request.IsValid() {
		rec.Request = jsontext.Value(`null`)
	}
	if jsontext.Value(resp).IsValid() {
		rec.Response = resp
	} else {
		rec.ResponseText = string(resp)
	}
	data, err := json.Marshal(rec, jsontext.Multiline(true))
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	path := filepath.Join(r.Dir, file)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return nil
}
