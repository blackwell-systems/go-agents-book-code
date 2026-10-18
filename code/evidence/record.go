package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// A tee is an http.RoundTripper that saves each exchange in the format
// replay.Transport reads. Unlike replay.Recorder it passes the response
// body through as it arrives, so stream timings stay real; the file is
// written when the body is closed.
type tee struct {
	dir    string
	script string

	mu   sync.Mutex
	n    int
	errs []error
}

func (t *tee) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.n++
	file := filepath.Join(t.dir, fmt.Sprintf("%s-%02d.json", t.script, t.n))
	t.mu.Unlock()
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read request: %w", err)
		}
		body = b
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		return nil, err //nolint:wrapcheck // a RoundTripper passes its transport's errors through
	}
	resp.Body = &teeBody{rc: resp.Body, save: func(data []byte) {
		if err := save(file, resp.StatusCode, body, data); err != nil {
			t.mu.Lock()
			t.errs = append(t.errs, err)
			t.mu.Unlock()
		}
	}}
	return resp, nil
}

// err reports any recording that couldn't be saved.
func (t *tee) err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return errors.Join(t.errs...)
}

type teeBody struct {
	rc   io.ReadCloser
	buf  bytes.Buffer
	save func([]byte)
	once sync.Once
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.buf.Write(p[:n])
	return n, err //nolint:wrapcheck // a Reader returns its errors as is
}

func (b *teeBody) Close() error {
	err := b.rc.Close()
	b.once.Do(func() { b.save(b.buf.Bytes()) })
	return err //nolint:wrapcheck // passed through unchanged
}

// save writes one exchange: the request body and the response, never
// the headers, so the key stays out of the file.
func save(file string, status int, req, resp []byte) error {
	rec := struct {
		Status       int            `json:"status"`
		Request      jsontext.Value `json:"request"`
		Response     jsontext.Value `json:"response,omitzero"`
		ResponseText string         `json:"response_text,omitzero"`
	}{Status: status, Request: req}
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
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := os.WriteFile(file, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return nil
}

// results appends measurements to a JSON Lines file.
type results struct {
	mu   sync.Mutex
	file string
}

func (r *results) add(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("results: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644)
	if err != nil {
		return fmt.Errorf("results: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("results: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("results: %w", err)
	}
	return nil
}

// load reads every line of a JSON Lines file into a slice.
func load[T any](file string) ([]T, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load results: %w", err)
	}
	var out []T
	for line := range bytes.Lines(data) {
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		out = append(out, v)
	}
	return out, nil
}
