// Package openrouter is a client for OpenRouter's chat completions API
// that satisfies agent.Model.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch02/agent"
)

// DefaultBaseURL is OpenRouter's API. Any server that accepts the same
// request format works.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// ErrCallTimeout is the cause recorded when a call runs past
// Client.Timeout.
var ErrCallTimeout = errors.New("openrouter: model call timed out")

var (
	errNoChoices = errors.New("openrouter: reply has no choices")
	errTooLarge  = errors.New("openrouter: response too large")
)

// maxBody is the most of a response the client reads: far more than
// any reply, but a limit on a broken server.
const maxBody = 16 << 20

// tag::client[]

// Client calls a model through OpenRouter.
type Client struct {
	BaseURL string        // DefaultBaseURL if empty
	APIKey  string        // sent as a bearer token
	Model   string        // a model ID from OpenRouter's list
	HTTP    *http.Client  // http.DefaultClient if nil
	Timeout time.Duration // per call; 2 minutes if zero
}

// end::client[]

// tag::generate[]

// Generate sends the conversation to the model and returns its reply.
func (c *Client) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout(),
		ErrCallTimeout) // <1>
	defer cancel()

	body, err := json.Marshal(c.wireRequest(req)) // <2>
	if err != nil {
		return agent.Reply{}, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return agent.Reply{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return agent.Reply{}, fmt.Errorf("send request: %w",
			callError(ctx, err)) // <3>
	}
	defer resp.Body.Close()
	return readReply(ctx, resp)
}

// end::generate[]

// tag::chat-request[]

// wireRequest turns the agent's request into the API's JSON shape.
// Tools and tool calls are left out until chapter 4.
func (c *Client) wireRequest(req agent.Request) chatRequest {
	out := chatRequest{Model: c.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, chatMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return out
}

// end::chat-request[]

// tag::read-reply[]

// readReply decodes a response into a Reply.
func readReply(ctx context.Context,
	resp *http.Response) (agent.Reply, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		err = callError(ctx, err)
		return agent.Reply{}, fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxBody {
		return agent.Reply{}, errTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		return agent.Reply{}, statusError(resp.Status, data) // <1>
	}
	var r chatResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return agent.Reply{}, fmt.Errorf("decode response: %w", err)
	}
	if r.Error != nil { // <2>
		return agent.Reply{}, r.Error
	}
	if len(r.Choices) == 0 {
		return agent.Reply{}, errNoChoices
	}
	if e := r.Choices[0].Error; e != nil { // <3>
		return agent.Reply{}, e
	}
	return toReply(r), nil
}

// end::read-reply[]

// tag::to-reply[]

// toReply copies the first choice and the usage into a Reply.
func toReply(r chatResponse) agent.Reply {
	ch := r.Choices[0]
	u := r.Usage
	return agent.Reply{
		Message: agent.Message{
			Role:    "assistant",
			Content: ch.Message.Content,
		},
		FinishReason: ch.FinishReason, // <1>
		Usage: agent.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			CachedTokens:     u.PromptDetails.CachedTokens,
			ReasoningTokens:  u.CompletionDetails.ReasoningTokens,
			Cost:             u.Cost,
		},
	}
}

// end::to-reply[]

// statusError describes a response that wasn't 200, using the API's own
// message when it sent one. Chapter 5 turns this into a typed error.
func statusError(status string, body []byte) error {
	var e struct {
		Error apiError `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("openrouter: %s: %s", status, e.Error.Message)
	}
	return fmt.Errorf("openrouter: %s", status)
}

// tag::call-error[]

// callError makes the error from a cancelled call carry both the kind
// of cancellation and its cause. The HTTP/1 transport reports only the
// cause, and the HTTP/2 transport only the kind.
func callError(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	if !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w: %w", ctx.Err(), err)
	}
	if cause := context.Cause(ctx); !errors.Is(err, cause) {
		err = fmt.Errorf("%w: %w", err, cause)
	}
	return err
}

// end::call-error[]

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return c.BaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

func (c *Client) timeout() time.Duration {
	if c.Timeout == 0 {
		return 2 * time.Minute
	}
	return c.Timeout
}
