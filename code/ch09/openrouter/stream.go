package openrouter

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch09/agent"
)

// ErrStreamIdle is the cause recorded when a stream sends nothing for
// longer than Client.IdleTimeout.
var ErrStreamIdle = errors.New("openrouter: stream went quiet")

// tag::chunk[]

// chatChunk is one chunk of a stream: a piece of the reply in choices,
// the usage in a last chunk with no choices, or an error.
type chatChunk struct {
	Choices []chunkChoice `json:"choices"`
	Usage   *chatUsage    `json:"usage"`
	Error   *apiError     `json:"error"`
}

type chunkChoice struct {
	Delta struct {
		Content          string         `json:"content"`
		ToolCalls        []chunkCall    `json:"tool_calls"`
		ReasoningDetails jsontext.Value `json:"reasoning_details"`
	} `json:"delta"`
	FinishReason string    `json:"finish_reason"`
	Error        *apiError `json:"error"`
}

// chunkCall is a piece of one tool call, identified by its index.
type chunkCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// end::chunk[]

// tag::stream[]

// Stream sends the conversation and yields the reply in pieces as the
// model writes it. A stream that goes quiet for IdleTimeout is
// cancelled with ErrStreamIdle as the cause.
func (c *Client) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		idle := time.AfterFunc(c.idleTimeout(), func() {
			cancel(ErrStreamIdle) // <1>
		})
		defer idle.Stop()

		wire := c.wireRequest(req)
		wire.Stream = true
		wire.StreamOptions = &streamOptions{IncludeUsage: true}
		resp, err := c.post(ctx, wire) // the wait for headers counts
		idle.Stop()                    // from here, idleReader arms it
		if err != nil {
			yield(agent.Delta{}, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK { // <2>
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			yield(agent.Delta{}, statusError(resp, body))
			return
		}
		body := &idleReader{resp.Body, idle, c.idleTimeout()}
		readStream(ctx, body, yield)
	}
}

// end::stream[]

// tag::read-stream[]

// readStream turns the chunks of a stream into Deltas.
func readStream(ctx context.Context, body io.Reader,
	yield func(agent.Delta, error) bool) {
	finished := false
	for data, err := range sseData(body) {
		if err != nil {
			switch {
			case finished && (errors.Is(err, io.ErrUnexpectedEOF) ||
				errors.Is(context.Cause(ctx), ErrStreamIdle)):
				return // done; only [DONE] and maybe usage are missing
			case ctx.Err() != nil:
				err = fmt.Errorf("read stream: %w", callError(ctx, err))
			case errors.Is(err, io.ErrUnexpectedEOF):
				err = agent.ErrIncompleteResponse // <1>
			case errors.Is(err, bufio.ErrTooLong):
				err = errTooLarge
			default:
				err = fmt.Errorf("read stream: %w", err)
			}
			yield(agent.Delta{}, err)
			return
		}
		var ch chatChunk
		if err := json.Unmarshal(data, &ch); err != nil {
			yield(agent.Delta{}, fmt.Errorf("decode chunk: %w", err))
			return
		}
		for _, d := range deltas(ch) {
			if d.err != nil {
				yield(agent.Delta{}, d.err) // <2>
				return
			}
			finished = finished || d.FinishReason != ""
			if !yield(d.Delta, nil) {
				return
			}
		}
	}
}

// end::read-stream[]

// tag::deltas[]

// A delta is a Delta, or the error a chunk carried in its place.
type delta struct {
	agent.Delta
	err error
}

// deltas splits one chunk into the Deltas it holds.
func deltas(ch chatChunk) []delta {
	var out []delta
	if e := ch.Error; e != nil {
		return []delta{{err: fmt.Errorf("%w: %s",
			agent.ErrProviderFailed, e)}}
	}
	for _, choice := range ch.Choices {
		if e := choice.Error; e != nil {
			return []delta{{err: fmt.Errorf("%w: %s",
				agent.ErrProviderFailed, e)}}
		}
		if t := choice.Delta.Content; t != "" {
			out = append(out, delta{Text: t})
		}
		if r := details(choice.Delta.ReasoningDetails); r != nil {
			out = append(out, delta{ReasoningDetails: r})
		}
		for _, tc := range choice.Delta.ToolCalls {
			out = append(out, delta{Call: &agent.CallDelta{
				Index: tc.Index, ID: tc.ID,
				Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			}})
		}
		if r := choice.FinishReason; r != "" {
			out = append(out, delta{FinishReason: r})
		}
	}
	if u := ch.Usage; u != nil {
		usage := toUsage(*u)
		out = append(out, delta{Usage: &usage})
	}
	return out
}

// end::deltas[]
