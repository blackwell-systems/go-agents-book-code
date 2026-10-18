// Package mcp is the Model Context Protocol over standard input and
// output, written by hand: newline-delimited JSON-RPC 2.0 messages, a
// server that offers tools, and a client that calls them (chapter 10).
//
// It speaks protocol version 2026-07-28, in which every request carries
// its protocol version and the client's capabilities, so neither side
// keeps a session. As a client it also falls back to the initialize
// handshake of earlier versions, which most servers still speak.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Version is the protocol version this package speaks.
const Version = "2026-07-28"

// tag::message[]

// A message is one JSON-RPC 2.0 message: a request has an ID and a
// method, a notification has a method and no ID, and a response has the
// ID of the request it answers and either a result or an error.
type message struct {
	JSONRPC string         `json:"jsonrpc"` // always "2.0"
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *Error         `json:"error,omitzero"`
}

// end::message[]

// tag::error[]

// An Error is a JSON-RPC error: the request itself was wrong, or the
// server couldn't handle it. A tool that ran and failed isn't one; its
// result says so instead, with isError set.
type Error struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data,omitzero"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("mcp: %s (code %d)", e.Message, e.Code)
}

// Error codes: JSON-RPC's own, and the one this package uses of those
// MCP defines.
const (
	CodeParse              = -32700 // not JSON
	CodeInvalidRequest     = -32600 // JSON, but not a request
	CodeMethodNotFound     = -32601
	CodeInvalidParams      = -32602 // including an unknown tool
	CodeInternal           = -32603
	CodeUnsupportedVersion = -32022 // data: {supported, requested}
)

// end::error[]

// maxMessage is the longest message either side reads: a guard against
// a peer that never sends a newline.
const maxMessage = 16 << 20

// errTooLong means a message passed maxMessage. The stream can't be
// read past it, since the end of the message was never found.
var errTooLong = errors.New("mcp: message longer than 16 MiB")

// tag::framing[]

// A conn reads and writes messages, one per line. encoding/json/v2
// writes no newline inside a value unless asked to, and escapes those
// in strings, so a message can't break the framing. Several goroutines
// send, so writes take a lock.
type conn struct {
	r *bufio.Reader

	mu sync.Mutex
	w  io.Writer
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{r: bufio.NewReader(r), w: w}
}

// send writes m as one line.
func (c *conn) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// end::framing[]

// tag::receive[]

// receive reads the next line and decodes it. Members the struct
// doesn't name are ignored, not rejected: the protocol lets either side
// add fields, such as _meta. A line that isn't JSON comes back as
// *Error with CodeParse, and JSON that isn't one JSON-RPC 2.0 message,
// such as a batch, with CodeInvalidRequest, so a server can answer and
// read on.
func (c *conn) receive() (message, error) {
	line, err := readLine(c.r)
	if err != nil {
		return message{}, err
	}
	if !jsontext.Value(line).IsValid() {
		return message{}, &Error{Code: CodeParse, Message: "not JSON"}
	}
	var m message
	err = json.Unmarshal(line, &m)
	if err != nil || m.JSONRPC != "2.0" {
		return message{}, &Error{Code: CodeInvalidRequest,
			Message: "not a JSON-RPC 2.0 message"}
	}
	return m, nil
}

// end::receive[]

// validID reports whether id is one MCP allows: a string or an integer,
// never null.
func validID(id jsontext.Value) bool {
	switch id.Kind() {
	case '"':
		return true
	case '0':
		_, err := strconv.ParseInt(string(id), 10, 64)
		return err == nil
	}
	return false
}

// readLine reads one line, without its newline. A last line without a
// newline is returned too; after it comes io.EOF.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		switch {
		case len(line) > maxMessage:
			return nil, errTooLong
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		case err != nil:
			return nil, err //nolint:wrapcheck // io.EOF is returned as is
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(line)) == 0 {
			line = line[:0]
			continue // a blank line isn't a message
		}
		return line, nil
	}
}

// rawJSON encodes v as a JSON value, for a message's params or result.
func rawJSON(v any) (jsontext.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return b, nil
}

// orJSON is v, or def when v is empty.
func orJSON(v jsontext.Value, def string) jsontext.Value {
	if len(v) == 0 {
		return jsontext.Value(def)
	}
	return v
}
