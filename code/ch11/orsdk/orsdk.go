// Package orsdk puts OpenRouter's official Go SDK behind agent.Model
// (chapter 11). It speaks the same API as chapter 2's hand-written
// client; what it shows is the shape of an adapter around an SDK: the
// SDK's retries turned off, its types mapped onto the agent's, and its
// errors translated into the book's error tree.
package orsdk

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/sdkerrors"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
	"github.com/OpenRouterTeam/go-sdk/retry"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	or "github.com/blackwell-systems/go-agents-book/code/ch11/openrouter"
)

// tag::client[]

// Client calls a model through OpenRouter's Go SDK.
type Client struct {
	sdk   *openrouter.OpenRouter
	model string

	// Timeout limits each Generate call; 2 minutes if zero, as chapter
	// 2's client.
	Timeout time.Duration
}

// New returns a client for model. httpClient may be nil; tests pass
// one whose transport replays saved calls.
func New(apiKey, model, baseURL string,
	httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	opts := []openrouter.SDKOption{
		openrouter.WithSecurity(apiKey),
		openrouter.WithClient(statusClient{httpClient}), // <1>
		openrouter.WithRetryConfig(
			retry.Config{Strategy: "none"}), // <2>
	}
	if baseURL != "" {
		opts = append(opts, openrouter.WithServerURL(baseURL))
	}
	return &Client{sdk: openrouter.New(opts...), model: model}
}

// end::client[]

// tag::generate[]

// Generate sends the conversation through the SDK and maps its result
// onto the agent's reply.
func (c *Client) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	ctx, cancel := context.WithTimeoutCause(ctx,
		cmp.Or(c.Timeout, 2*time.Minute), or.ErrCallTimeout)
	defer cancel()
	chat, err := c.chatRequest(req)
	if err != nil {
		return agent.Reply{}, err
	}
	var seen response
	ctx = context.WithValue(ctx, responseKey{}, &seen)
	res, err := c.sdk.Chat.Send(ctx, chat, nil)
	if err != nil {
		return agent.Reply{}, apiError(ctx, err, seen) // <1>
	}
	if res.ChatResult == nil || len(res.ChatResult.Choices) == 0 {
		return agent.Reply{}, &or.APIError{ // as chapter 2's client
			Status: cmp.Or(seen.errStatus, http.StatusBadGateway),
			Message: cmp.Or(seen.errMessage,
				"the reply has no choices")}
	}
	return toReply(*res.ChatResult)
}

// end::generate[]

// tag::status-client[]

// statusClient records each response's status and Retry-After header
// in the call's context. The SDK reports a failed chat call as one of
// fourteen error types, none of which holds the HTTP status, so the
// adapter reads the status from the response itself.
type statusClient struct{ next *http.Client }

type response struct {
	status     int
	retryAfter time.Duration
	errStatus  int    // an error object in a 200 reply, if any
	errMessage string // its message
}

type responseKey struct{}

func (s statusClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := s.next.Do(req)
	if seen, ok := req.Context().Value(responseKey{}).(*response); ok &&
		resp != nil {
		seen.status = resp.StatusCode
		wait := resp.Header.Get("Retry-After")
		if n, err := strconv.Atoi(wait); err == nil {
			seen.retryAfter = time.Duration(n) * time.Second
		}
		if resp.StatusCode == http.StatusOK {
			seen.peek(resp)
		}
	}
	return resp, err //nolint:wrapcheck // a transport passes errors on
}

// end::status-client[]

// peek reads a 200 response's body for an error object, which OpenRouter
// sends when a provider fails after the status was sent, and puts the
// body back for the SDK to read.
func (seen *response) peek(resp *http.Response) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return // the SDK will read the same failure
	}
	var e struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		seen.errStatus, seen.errMessage = e.Error.Code, e.Error.Message
	}
}

// tag::api-error[]

// apiError translates an SDK error into the book's error tree: a
// response that wasn't 200 becomes the same *APIError the hand-written
// client returns, so Retryable and chapter 12's retries work alike.
func apiError(ctx context.Context, err error, seen response) error {
	if done := ended(ctx, err); done != nil { // <1>
		return done
	}
	if seen.status == 0 || seen.status == http.StatusOK {
		return fmt.Errorf("openrouter sdk: %w", err)
	}
	msg := err.Error()
	var plain *sdkerrors.APIError
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	switch {
	case errors.As(err, &plain):
		msg = plain.Message
	case json.Unmarshal([]byte(msg), &body) == nil &&
		body.Error.Message != "":
		msg = body.Error.Message // typed errors print the API's JSON
	}
	return &or.APIError{Status: seen.status, Message: msg,
		RetryAfter: seen.retryAfter}
}

// end::api-error[]

// ended returns the error for a call that its context ended, or nil.
// With the SDK's retries on, the context can end while the SDK waits
// to retry a 503, and then the call failed because of the context, not
// the status it last saw. The error carries the kind of ending and its
// cause, as chapter 2's client's does.
func ended(ctx context.Context, err error) error {
	if ctx.Err() == nil || !errors.Is(err, ctx.Err()) &&
		!errors.Is(err, context.Cause(ctx)) {
		return nil
	}
	if !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w: %w", ctx.Err(), err)
	}
	if cause := context.Cause(ctx); !errors.Is(err, cause) {
		err = fmt.Errorf("%w: %w", err, cause)
	}
	return fmt.Errorf("openrouter sdk: %w", err)
}

// tag::chat-request[]

// chatRequest maps the agent's request onto the SDK's types. Each
// message is a union, built by its own constructor.
func (c *Client) chatRequest(req agent.Request) (components.ChatRequest,
	error) {
	out := components.ChatRequest{Model: &c.model}
	if req.MaxTokens > 0 {
		n := int64(req.MaxTokens)
		out.MaxTokens = optionalnullable.From(&n)
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, chatMessage(m))
	}
	for _, t := range req.Tools {
		tool, err := chatTool(t)
		if err != nil {
			return out, err
		}
		out.Tools = append(out.Tools, tool)
	}
	if req.ResponseFormat != nil {
		f, err := responseFormat(req.ResponseFormat)
		if err != nil {
			return out, err
		}
		out.ResponseFormat = f
	}
	return out, nil
}

// end::chat-request[]
