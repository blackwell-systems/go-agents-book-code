package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sync"
)

// exchange is one recorded API call: what was sent and what came back.
type exchange struct {
	Request  jsontext.Value `json:"request"`
	Status   int            `json:"status"`
	Response jsontext.Value `json:"response"`
}

// replayer is an http.RoundTripper that answers each request with the
// next recorded response, so the program runs offline. It doesn't
// compare requests; a run that takes a different path just runs out.
type replayer struct {
	mu   sync.Mutex
	runs []exchange
}

func loadReplay(files fs.FS, file string) (*replayer, error) {
	data, err := fs.ReadFile(files, file)
	if err != nil {
		return nil, fmt.Errorf("load replay: %w", err)
	}
	r := &replayer{}
	if err := json.Unmarshal(data, &r.runs); err != nil {
		return nil, fmt.Errorf("load replay %s: %w", file, err)
	}
	return r, nil
}

func (r *replayer) RoundTrip(req *http.Request) (*http.Response,
	error) {
	if req.Body != nil {
		req.Body.Close()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) == 0 {
		return nil, errors.New("replay: no more recorded responses")
	}
	ex := r.runs[0]
	r.runs = r.runs[1:]
	return &http.Response{
		StatusCode: ex.Status,
		Status: fmt.Sprintf("%d %s", ex.Status,
			http.StatusText(ex.Status)),
		Header:  http.Header{"Content-Type": {"application/json"}},
		Body:    io.NopCloser(bytes.NewReader(ex.Response)),
		Request: req,
	}, nil
}

// recorder is an http.RoundTripper that passes each request on and
// keeps a copy of it and its response, for save to write out.
type recorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	runs []exchange
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response,
	error) {
	sent, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("record request: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(sent))
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("record response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(got))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, exchange{
		Request: sent, Status: resp.StatusCode, Response: got})
	return resp, nil
}

func (r *recorder) save(file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := json.Marshal(r.runs, jsontext.WithIndent("\t"))
	if err != nil {
		return fmt.Errorf("save recording: %w", err)
	}
	if err := os.WriteFile(file, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("save recording: %w", err)
	}
	return nil
}
