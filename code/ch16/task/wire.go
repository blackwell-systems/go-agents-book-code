package task

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
)

// Replies holds the scripted replies every version is run against, in
// chapter 2's replay format: diagnose, the job with no surprises;
// tool-error, which starts with a call to a package that doesn't
// exist; and retry, which starts with a 503.
//
//go:embed replies
var Replies embed.FS

// tag::replay[]

// Replay returns a Config whose calls are answered from Replies, by the
// script named in each call's context (replay.WithScript), and the Wire
// that keeps what was sent.
func Replay() (Config, *Wire) {
	files, err := fs.Sub(Replies, "replies")
	if err != nil {
		panic(err) // the embedded directory is always there
	}
	w := &Wire{Next: &replay.Transport{Files: files}}
	return Config{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  "replay", // Genkit's plugin refuses an empty key
		HTTP:    &http.Client{Transport: w},
	}, w
}

// end::replay[]

// Config is what every version needs to reach the model.
type Config struct {
	BaseURL string       // OpenRouter's, or a replay's
	APIKey  string       // any non-empty value in replay
	HTTP    *http.Client // carries the replaying or recording transport
}

// Outcome is what a version reports about its run, beyond what the run
// did to Env.
type Outcome struct {
	Pauses int // times the run stopped to wait for a person
	Saved  int // bytes kept between a pause and its resume
}

// tag::wire[]

// Wire is an http.RoundTripper that keeps a copy of every request body
// it passes on to Next, so the versions' requests can be compared.
type Wire struct {
	Next http.RoundTripper

	mu     sync.Mutex
	bodies [][]byte
}

// RoundTrip copies the body and sends a clone of the request on, since
// a RoundTripper must not change the request it was given.
func (w *Wire) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := drain(req)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.bodies = append(w.bodies, body)
	w.mu.Unlock()
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := w.Next.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("send through wire: %w", err)
	}
	return resp, nil
}

// end::wire[]

// drain reads and closes the request body, as the RoundTripper contract
// requires, and returns what it read.
func drain(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read request: %w", err)
	}
	return b, nil
}

// Bodies returns the request bodies sent so far, in order.
func (w *Wire) Bodies() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.bodies...)
}
