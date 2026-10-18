package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch02/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch02/replay"
)

var update = flag.Bool("update", false, "rewrite testdata/transcript.txt")

// TestReplayedChat runs the program with no key and compares its output
// with testdata/transcript.txt, which the chapter shows. Run with
// -update after changing the saved replies or the output format.
func TestReplayedChat(t *testing.T) {
	var out bytes.Buffer
	noKey := func(string) string { return "" }
	if err := run(t.Context(), noKey, nil, &out, ""); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	const golden = "testdata/transcript.txt"
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("output differs from %s:\n%s", golden, out.String())
	}
}

// checkSent is a RoundTripper that fails the test when a request body
// differs from the one saved with the response it replays.
type checkSent struct {
	t     *testing.T
	files fs.FS
	n     int
	next  http.RoundTripper
}

func (c *checkSent) RoundTrip(req *http.Request) (*http.Response, error) {
	c.n++
	sent, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read request: %w", err)
	}
	data, err := fs.ReadFile(c.files, fmt.Sprintf("chat-%02d.json", c.n))
	if err != nil {
		return nil, fmt.Errorf("saved call: %w", err)
	}
	var rec struct {
		Request jsontext.Value `json:"request"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("saved call: %w", err)
	}
	if !equalJSON(sent, rec.Request) {
		c.t.Errorf("call %d sent\n%s\nwant\n%s", c.n, sent, rec.Request)
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(sent))
	resp, err := c.next.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	return resp, nil
}

func equalJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil &&
		reflect.DeepEqual(x, y)
}

// TestSavedRequests checks that the chat program sends what the saved
// conversation says it sent, so the requests the chapter shows are the
// program's own.
func TestSavedRequests(t *testing.T) {
	files := os.DirFS("testdata/replies")
	inputs, err := fs.ReadFile(files, "chat-inputs.txt")
	if err != nil {
		t.Fatal(err)
	}
	client := &openrouter.Client{
		Model: defaultModel,
		HTTP: &http.Client{Transport: &checkSent{
			t: t, files: files, next: &replay.Transport{Files: files},
		}},
	}
	ctx := replay.WithScript(t.Context(), "chat")
	err = chat(ctx, client, bytes.NewReader(inputs), io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
}

// TestInterruptAtPrompt checks that cancelling the context ends a chat
// that is waiting for input.
func TestInterruptAtPrompt(t *testing.T) {
	in, w := io.Pipe() // input that never arrives
	defer w.Close()
	stop := errors.New("interrupt signal received")
	ctx, cancel := context.WithCancelCause(t.Context())
	time.AfterFunc(20*time.Millisecond, func() { cancel(stop) })

	err := chat(ctx, nil, in, io.Discard, false)
	for _, want := range []error{stop, context.Canceled} {
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want it to match %v", err, want)
		}
	}
}

// TestWrap checks that long lines break at spaces and short ones stay.
func TestWrap(t *testing.T) {
	text := "one two three\nfour five six seven"
	want := "one two\nthree\nfour five\nsix seven"
	if got := wrap(text, 9); got != want {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	if got := wrap("short", 9); got != "short" {
		t.Errorf("wrap(short) = %q", got)
	}
}

// TestShownJSON checks that the request and response the chapter shows
// are the ones saved in testdata/replies/length-01.json.
func TestShownJSON(t *testing.T) {
	data, err := os.ReadFile("testdata/replies/length-01.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Request  jsontext.Value `json:"request"`
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]jsontext.Value{
		"testdata/length-request.json":  rec.Request,
		"testdata/length-response.json": rec.Response,
	} {
		shown, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !equalJSON(shown, want) {
			t.Errorf("%s differs from the saved call", file)
		}
	}
}
