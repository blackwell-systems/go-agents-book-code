package openrouter

import (
	"bufio"
	"bytes"
	"io"
	"iter"
	"time"
)

// tag::sse[]

// sseData reads a server-sent event stream and yields the data of each
// "data:" line until "data: [DONE]". It treats each data line as a
// whole event, which is what OpenAI-compatible servers send. Comment lines, which start with a
// colon and keep a quiet connection open, and blank lines are skipped.
// A stream that ends without [DONE] yields io.ErrUnexpectedEOF.
func sseData(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), maxBody) // <1>
		for sc.Scan() {
			data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
			if !ok {
				continue // a comment, a blank line, or another field
			}
			data = bytes.TrimPrefix(data, []byte(" "))
			if string(data) == "[DONE]" {
				return
			}
			if !yield(data, nil) {
				return
			}
		}
		err := sc.Err()
		if err == nil {
			err = io.ErrUnexpectedEOF // <2>
		}
		yield(nil, err)
	}
}

// end::sse[]

// tag::idle[]

// idleReader runs an idle timer only while it waits for the network,
// so the timer fires when the stream itself has gone quiet for the
// whole timeout, not when the program is busy with what it already
// has. If the timer has already fired, Reset runs it again, which is
// harmless: cancelling twice keeps the first cause.
type idleReader struct {
	r       io.Reader
	timer   *time.Timer
	timeout time.Duration
}

func (ir *idleReader) Read(p []byte) (int, error) {
	ir.timer.Reset(ir.timeout) // <1>
	n, err := ir.r.Read(p)
	ir.timer.Stop()
	return n, err //nolint:wrapcheck // a Reader returns its errors as is
}

// end::idle[]
