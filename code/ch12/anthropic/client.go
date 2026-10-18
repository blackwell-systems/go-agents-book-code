// Package anthropic is a client for Anthropic's Messages API, the
// native API of the Claude models, that satisfies agent.Model and
// agent.StreamModel. It maps the agent's types onto a format that
// differs from chat completions in almost every detail (chapter 11).
package anthropic

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// DefaultBaseURL is Anthropic's API. OpenRouter serves the same API at
// https://openrouter.ai/api/v1, which this chapter's runs use.
const DefaultBaseURL = "https://api.anthropic.com/v1"

// DefaultMaxTokens is sent when a request leaves MaxTokens at 0. The
// Messages API requires max_tokens, and on a model that thinks the
// limit covers the thinking as well as the reply.
const DefaultMaxTokens = 16_000

// apiVersion is the Messages API version the client speaks.
const apiVersion = "2023-06-01"

// bindingBeta turns on the setting thinking.block_binding, which the
// client uses to drop thinking blocks that history edits made stale.
const bindingBeta = "thinking-binding-controls-2026-08-01"

// ErrCallTimeout is the cause recorded when a call runs past
// Client.Timeout. It reports Timeout, as a network timeout does, so a
// retry policy can treat both alike (chapter 12).
var ErrCallTimeout error = timeoutError("anthropic: model call timed out")

// A timeoutError is a cause that reports itself as a timeout.
type timeoutError string

func (e timeoutError) Error() string { return string(e) }
func (timeoutError) Timeout() bool   { return true }

// maxBody is the most of a response the client reads.
const maxBody = 16 << 20

// tag::client[]

// Client calls a Claude model through the Messages API.
type Client struct {
	BaseURL string        // DefaultBaseURL if empty
	APIKey  string        // sent in the x-api-key header
	Model   string        // such as "claude-haiku-5-5"
	HTTP    *http.Client  // http.DefaultClient if nil
	Timeout time.Duration // per Generate call; 2 minutes if zero

	// IdleTimeout is the longest a stream may go without sending a
	// byte; 30 seconds if zero (chapter 6).
	IdleTimeout time.Duration

	// Prices turns the reply's token counts into dollars, since the
	// Messages API reports no cost. Nil leaves Usage.Cost at 0.
	Prices Prices

	// Effort, if set, is output_config.effort: how much a model that
	// thinks adaptively should think, such as "high".
	Effort string
}

// end::client[]

// tag::generate[]

// Generate sends the conversation and returns the model's reply.
func (c *Client) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout(),
		ErrCallTimeout)
	defer cancel()
	resp, err := c.post(ctx, c.wireRequest(req, false))
	if err != nil {
		return agent.Reply{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return agent.Reply{}, fmt.Errorf("read response: %w",
			callError(ctx, err))
	}
	if resp.StatusCode != http.StatusOK {
		return agent.Reply{}, statusError(resp, body)
	}
	if e := bodyError(body); e != nil { // an error, though the status was 200
		return agent.Reply{}, e
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return agent.Reply{}, fmt.Errorf("decode response: %w", err)
	}
	return c.toReply(m), nil
}

// end::generate[]

// post sends a request to the Messages endpoint and returns the
// response, whatever its status.
func (c *Client) post(ctx context.Context,
	wire request) (*http.Response, error) {
	body, err := json.Marshal(wire, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL()+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.APIKey) // Anthropic's API
	if !strings.HasSuffix(c.Name(), "api.anthropic.com") {
		// OpenRouter's Messages API takes its key as a bearer token.
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("anthropic-beta", bindingBeta)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", callError(ctx, err))
	}
	return resp, nil
}

// Name is what the client records in Message.ReasoningFrom: the API and
// the host it was reached at. A thinking block is bound to the account
// that produced it, so blocks from api.anthropic.com and from
// openrouter.ai are different sources even for the same model.
func (c *Client) Name() string {
	host := c.baseURL()
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	return "anthropic@" + host
}

func (c *Client) baseURL() string { return cmp.Or(c.BaseURL, DefaultBaseURL) }

func (c *Client) timeout() time.Duration {
	return cmp.Or(c.Timeout, 2*time.Minute)
}

func (c *Client) idleTimeout() time.Duration {
	return cmp.Or(c.IdleTimeout, 30*time.Second)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// callError adds the context's error and cause to a transport error,
// so errors.Is finds context.Canceled or a timeout's cause.
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
