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

// tag::transport[]

// Transport is an http.RoundTripper that answers each request with the
// next saved response for its script, read from Files.
type Transport struct {
	Files fs.FS // saved calls, such as an embedded testdata directory
	calls counter
}

// RoundTrip serves the next saved response.
func (t *Transport) RoundTrip(
	req *http.Request) (*http.Response, error) {
	if _, err := drain(req); err != nil { // <1>
		return nil, err
	}
	ctx := req.Context()
	if err := context.Cause(ctx); err != nil {
		return nil, fmt.Errorf("replay: %w", err) // <2>
	}
	script := scriptOf(ctx)
	n := t.calls.next(script)
	rec, err := load(t.Files, script, n)
	if err != nil {
		return nil, err
	}
	return rec.response(req), nil
}

// end::transport[]

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
func (r *Recorder) RoundTrip(
	req *http.Request) (*http.Response, error) {
	script := scriptOf(req.Context())
	n := r.calls.next(script)
	body, err := drain(req)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context()) // <1>
	out.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := r.transport().RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("record: read response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))            // <2>
	err = r.save(name(script, n), resp.StatusCode, body, data) // <3>
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// end::record[]

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
