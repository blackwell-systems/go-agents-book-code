package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// ErrStreamIdle is the cause recorded when a stream sends nothing for
// longer than Client.IdleTimeout.
var ErrStreamIdle error = timeoutError("anthropic: stream went quiet")

// tag::event[]

// event is one event of a stream. Its type says which fields it uses:
// message_start (Message), content_block_start (Index, Block),
// content_block_delta (Index, Delta), content_block_stop (Index),
// message_delta (Delta, Usage), message_stop, ping, and error.
type event struct {
	Type    string         `json:"type"`
	Message *message       `json:"message"`
	Index   int            `json:"index"`
	Block   jsontext.Value `json:"content_block"`
	Delta   struct {
		Type        string `json:"type"`
		Text        string `json:"text"`         // text_delta
		PartialJSON string `json:"partial_json"` // input_json_delta
		Thinking    string `json:"thinking"`     // thinking_delta
		Signature   string `json:"signature"`    // signature_delta
		StopReason  string `json:"stop_reason"`  // message_delta
	} `json:"delta"`
	Usage *usage    `json:"usage"`
	Error *struct { // error
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// end::event[]

// tag::stream[]

// Stream sends the conversation and yields the reply in pieces as the
// model writes it, the same Deltas the OpenRouter client yields.
func (c *Client) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		idle := time.AfterFunc(c.idleTimeout(), func() {
			cancel(ErrStreamIdle)
		})
		defer idle.Stop()
		resp, err := c.post(ctx, c.wireRequest(req, true))
		idle.Stop()
		if err != nil {
			yield(agent.Delta{}, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			yield(agent.Delta{}, statusError(resp, body))
			return
		}
		r := &idleReader{resp.Body, idle, c.idleTimeout()}
		c.readStream(ctx, r, yield)
	}
}

// end::stream[]

// tag::read-stream[]

// readStream turns the events of a stream into Deltas. A stream ends
// with message_stop; one that ends before it is incomplete.
func (c *Client) readStream(ctx context.Context, body io.Reader,
	yield func(agent.Delta, error) bool) {
	s := streamState{client: c, calls: map[int]int{},
		blocks: map[int]*partial{}}
	for data, err := range eventData(body) {
		if err != nil {
			yield(s.billed(), streamError(ctx, err))
			return
		}
		var ev event
		if err := json.Unmarshal(data, &ev); err != nil {
			yield(agent.Delta{}, fmt.Errorf("decode event: %w", err))
			return
		}
		if ev.Type == "message_stop" {
			return
		}
		d, err := s.next(ev)
		if err != nil {
			yield(s.billed(), err)
			return
		}
		if d != nil && !yield(*d, nil) {
			return
		}
	}
}

// end::read-stream[]

func streamError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("read stream: %w", callError(ctx, err))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: %w", agent.ErrIncompleteResponse, err)
	}
	return fmt.Errorf("read stream: %w", err)
}

// tag::stream-state[]

// streamState is what a stream has said so far that later events need:
// which content blocks are tool calls, and every block as it is built,
// since the reply's content goes back to the API exactly as it came.
type streamState struct {
	client *Client
	calls  map[int]int      // content block index to tool call number
	blocks map[int]*partial // content blocks being written, by index
	usage  usage
	id     string // the message's ID, from message_start (chapter 12)
}

// billed is the Delta a failed stream ends with: the message's ID and
// what message_start counted, which the API bills for the prompt even
// though the reply never finished (chapter 12).
func (s *streamState) billed() agent.Delta {
	if s.id == "" {
		return agent.Delta{}
	}
	u := s.client.toUsage(s.usage)
	return agent.Delta{ID: s.id, Usage: &u}
}

// A partial is one content block being written: its start, as the API
// sent it, and the pieces that came after.
type partial struct {
	start map[string]jsontext.Value
	text  strings.Builder // text, thinking, or a call's input
	sig   string          // a thinking block's signature
}

// end::stream-state[]

// tag::next[]

// next turns one event into the Delta it carries, if any.
func (s *streamState) next(ev event) (*agent.Delta, error) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			s.usage, s.id = ev.Message.Usage, ev.Message.ID
			return &agent.Delta{ID: s.id}, nil
		}
	case "content_block_start":
		return s.start(ev)
	case "content_block_delta":
		return s.delta(ev), nil
	case "content_block_stop":
		return s.stop(ev)
	case "message_delta":
		return s.end(ev), nil
	case "error":
		return nil, s.failed(ev)
	}
	return nil, nil // ping, or an event this client doesn't use
}

// end::next[]

// tag::start[]

// start opens a content block and keeps its start; a tool call gets
// the next call number and yields its ID and name.
func (s *streamState) start(ev event) (*agent.Delta, error) {
	p := &partial{}
	if err := json.Unmarshal(ev.Block, &p.start); err != nil {
		return nil, fmt.Errorf("decode block: %w", err)
	}
	s.blocks[ev.Index] = p
	var b struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Sig  string `json:"signature"` // some servers send it here
	}
	if err := json.Unmarshal(ev.Block, &b); err != nil {
		return nil, fmt.Errorf("decode block: %w", err)
	}
	p.sig = b.Sig
	if b.Type != "tool_use" {
		return nil, nil
	}
	n := len(s.calls)
	s.calls[ev.Index] = n
	return &agent.Delta{Call: &agent.CallDelta{Index: n,
		ID: b.ID, Name: b.Name}}, nil
}

// end::start[]

// tag::delta[]

// delta adds a piece to its block, and yields the pieces of text and of
// a call's arguments as they come.
func (s *streamState) delta(ev event) *agent.Delta {
	p, d := s.blocks[ev.Index], ev.Delta
	if p == nil {
		return nil
	}
	switch d.Type {
	case "text_delta":
		p.text.WriteString(d.Text)
		return &agent.Delta{Text: d.Text}
	case "input_json_delta":
		n, ok := s.calls[ev.Index]
		if !ok || d.PartialJSON == "" {
			return nil
		}
		p.text.WriteString(d.PartialJSON)
		return &agent.Delta{Call: &agent.CallDelta{Index: n,
			Arguments: d.PartialJSON}}
	case "thinking_delta":
		p.text.WriteString(d.Thinking)
	case "signature_delta":
		p.sig = d.Signature
	}
	return nil
}

// end::delta[]

// tag::stop[]

// stop closes a content block and yields it whole in ReasoningDetails,
// which the assembler joins in order (chapter 6); a call that received
// no arguments gets "{}".
func (s *streamState) stop(ev event) (*agent.Delta, error) {
	p := s.blocks[ev.Index]
	if p == nil {
		return nil, nil
	}
	delete(s.blocks, ev.Index)
	var out agent.Delta
	switch blockOf(p) {
	case "text":
		p.start["text"] = quote(p.text.String())
	case "thinking":
		p.start["thinking"] = quote(p.text.String())
		p.start["signature"] = quote(p.sig)
	case "tool_use":
		input := jsontext.Value(p.text.String())
		if len(input) == 0 {
			input = jsontext.Value(`{}`)
			out.Call = &agent.CallDelta{Index: s.calls[ev.Index],
				Arguments: "{}"}
		}
		p.start["input"] = input
	}
	return &out, s.whole(p.start, &out)
}

// end::stop[]

// whole puts a finished block in d as a one-element array of blocks,
// marked with the client that received it.
func (s *streamState) whole(block map[string]jsontext.Value,
	d *agent.Delta) error {
	b, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("encode block: %w", err)
	}
	d.ReasoningDetails = append(append([]byte{'['}, b...), ']')
	d.ReasoningFrom = s.client.Name()
	return nil
}

func blockOf(p *partial) string {
	var t string
	_ = json.Unmarshal(p.start["type"], &t)
	return t
}

func quote(text string) jsontext.Value {
	b, _ := jsontext.AppendQuote(nil, text) // invalid UTF-8 is replaced
	return b
}

// end marks the end of the reply: its stop reason, when this event has
// one, and its usage, which the API gives as running totals.
func (s *streamState) end(ev event) *agent.Delta {
	if u := ev.Usage; u != nil {
		s.usage.OutputTokens = u.OutputTokens
		s.usage.Details = u.Details
		s.usage.Cost = u.Cost
		if u.InputTokens > 0 { // some servers repeat the input counts
			s.usage.InputTokens = u.InputTokens
			s.usage.CacheRead = u.CacheRead
			s.usage.CacheWrite = u.CacheWrite
		}
	}
	use := s.client.toUsage(s.usage)
	d := &agent.Delta{Usage: &use}
	if r := ev.Delta.StopReason; r != "" { // a usage-only event has none
		d.FinishReason = finishReason(r)
	}
	return d
}

// failed turns an error event into an APIError, so an overloaded API
// partway through a stream is as retryable as one that refused at once.
func (s *streamState) failed(ev event) error {
	e := &APIError{Type: "error", Message: "error event"}
	if ev.Error != nil {
		e.Type, e.Message = ev.Error.Type, ev.Error.Message
	}
	e.Status = typeStatus[e.Type]
	if e.Status == 0 {
		e.Status = http.StatusInternalServerError
	}
	return e
}

// tag::event-data[]

// eventData reads a server-sent event stream and yields the data of
// each event. The Messages API names each event in an "event:" line
// and repeats the name as the data's type, so the data is enough. A
// stream that ends without message_stop yields io.ErrUnexpectedEOF;
// readStream stops at message_stop and never reads that far.
func eventData(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), maxBody)
		for sc.Scan() {
			data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
			data = bytes.TrimSpace(data)
			if !ok || len(data) == 0 || data[0] != '{' {
				continue // an event line, a blank line, or [DONE]
			}
			if !yield(data, nil) {
				return
			}
		}
		err := sc.Err()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		yield(nil, err)
	}
}

// end::event-data[]

// idleReader runs an idle timer only while it waits for the network,
// as chapter 6's does.
type idleReader struct {
	r       io.Reader
	timer   *time.Timer
	timeout time.Duration
}

func (ir *idleReader) Read(p []byte) (int, error) {
	ir.timer.Reset(ir.timeout)
	n, err := ir.r.Read(p)
	ir.timer.Stop()
	return n, err //nolint:wrapcheck // a Reader returns its errors as is
}
