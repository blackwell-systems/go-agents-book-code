package mcp

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// wire records every line that passes in either direction, in order.
type wire struct {
	mu    sync.Mutex
	lines []string
}

// tap returns a writer that records each line it passes on to w.
func (wr *wire) tap(arrow string, w io.WriteCloser) io.WriteCloser {
	return tapWriter{wr, arrow, w}
}

type tapWriter struct {
	wr    *wire
	arrow string
	w     io.WriteCloser
}

func (t tapWriter) Write(p []byte) (int, error) {
	t.wr.mu.Lock()
	for line := range strings.Lines(string(p)) {
		t.wr.lines = append(t.wr.lines, fmt.Sprintf("%s %4d  %s", t.arrow,
			len(line), strings.TrimSuffix(line, "\n")))
	}
	t.wr.mu.Unlock()
	return t.w.Write(p) //nolint:wrapcheck // a writer passes errors on
}

func (t tapWriter) Close() error { return t.w.Close() } //nolint:wrapcheck // as Write

// TestTranscript records one session's messages, as the chapter shows
// them: discovery, a list in two pages, a call that works, one that
// fails, an unknown tool, and a call cancelled at its timeout. Each
// line is the direction (> client to server, < server to client), the
// message's size in bytes with its newline, and the message.
func TestTranscript(t *testing.T) {
	s := testServer()
	s.PageSize = 2
	s.Tools = append(s.Tools, Tool{Name: "hang", Call: func(ctx context.Context, _ jsontext.Value) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	var wr wire
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- s.Serve(context.Background(), sr, wr.tap("<", sw))
		_ = sw.Close()
	}()
	ctx := t.Context()
	c, err := Connect(ctx, Implementation{Name: "triage", Version: "0.10"}, cr, wr.tap(">", cw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = c.CallTool(ctx, "echo", jsontext.Value(`{"text":"hello"}`))
	_, _ = c.CallTool(ctx, "fail", nil)
	_, _ = c.CallTool(ctx, "nope", nil)
	c.Timeout = 50 * time.Millisecond
	_, _ = c.CallTool(ctx, "hang", nil)
	time.Sleep(50 * time.Millisecond) // let the cancellation arrive
	_ = c.Close()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	got := []byte(strings.Join(wr.lines, "\n") + "\n")
	golden := filepath.Join("testdata", "transcript.txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("transcript differs from %s; run go test -update:\n%s", golden, got)
	}
}
