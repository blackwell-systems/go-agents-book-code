// Package openrouter is a client for OpenRouter's chat completions API
// that satisfies agent.Model.
package openrouter

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
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
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
	Timeout time.Duration // per Generate call; 2 minutes if zero

	// IdleTimeout is the longest a stream may go without sending a
	// byte, including the wait for its headers; 30 seconds if zero
	// (chapter 6). Stream doesn't use Timeout.
	IdleTimeout time.Duration

	// Providers, if set, are the only providers OpenRouter may send
	// requests to, such as "novita" (chapter 3).
	Providers []string
}

// end::client[]

// tag::generate[]

// Generate sends the conversation to the model and returns its reply.
func (c *Client) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	r, err := c.send(ctx, c.wireRequest(req))
	if err != nil {
		// A provider that failed partway still billed for the tokens.
		return agent.Reply{Usage: toUsage(r.Usage)}, err
	}
	return toReply(r), nil
}

// send posts a request to the API and returns the decoded response.
func (c *Client) send(ctx context.Context,
	wire chatRequest) (chatResponse, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout(),
		ErrCallTimeout) // <1>
	defer cancel()
	resp, err := c.post(ctx, wire)
	if err != nil {
		return chatResponse{}, err
	}
	defer resp.Body.Close()
	return readResponse(ctx, resp)
}

// post sends a request to the chat completions endpoint and returns the
// response, whatever its status.
func (c *Client) post(ctx context.Context,
	wire chatRequest) (*http.Response, error) {
	body, err := json.Marshal(wire, jsontext.AllowInvalidUTF8(true)) // <2>
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w",
			callError(ctx, err)) // <3>
	}
	return resp, nil
}

// end::generate[]

// tag::chat-request[]

// wireRequest turns the agent's request into the API's JSON shape.
func (c *Client) wireRequest(req agent.Request) chatRequest {
	out := chatRequest{Model: c.Model, MaxTokens: req.MaxTokens}
	if len(c.Providers) > 0 {
		out.Provider = &routing{Only: c.Providers}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, chatTool{Type: "function",
			Function: chatFunction{t.Name, t.Description, t.Parameters,
				t.Strict}})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, wireMessage(m))
	}
	return out
}

// end::chat-request[]

// tag::wire-message[]

// wireMessage converts one message. The API has no field for a failed
// tool, so a tool error goes as text the model can read.
func wireMessage(m agent.Message) chatMessage {
	out := chatMessage{
		Role:             m.Role,
		Content:          m.Content,
		ToolCallID:       m.ToolCallID,
		ReasoningDetails: m.ReasoningDetails, // <1>
	}
	if m.Role == "tool" {
		if m.IsError {
			out.Content = "Error: " + out.Content // <2>
		}
		out.Content = cmp.Or(out.Content, "(no output)") // <3>
	}
	for _, call := range m.ToolCalls {
		var tc chatToolCall
		tc.ID, tc.Type = call.ID, "function"
		tc.Function.Name = call.Name
		tc.Function.Arguments = string(call.Arguments) // <4>
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	return out
}

// end::wire-message[]

// tag::read-reply[]

// readResponse reads and decodes a response, and checks it holds a
// reply.
func readResponse(ctx context.Context,
	resp *http.Response) (chatResponse, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		err = callError(ctx, err)
		return chatResponse{}, fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxBody {
		return chatResponse{}, errTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		return chatResponse{}, statusError(resp, data) // <1>
	}
	var r chatResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return chatResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if e := r.Error; e != nil { // <2>
		return chatResponse{}, &APIError{
			Status:  cmp.Or(e.status(), http.StatusBadGateway),
			Message: e.Message,
		}
	}
	if len(r.Choices) == 0 {
		return chatResponse{}, errNoChoices
	}
	if e := r.Choices[0].Error; e != nil { // <3>
		return r, fmt.Errorf("%w: %s", agent.ErrProviderFailed, e)
	}
	return r, nil
}

// end::read-reply[]

// tag::to-reply[]

// toReply copies the first choice and the usage into a Reply.
func toReply(r chatResponse) agent.Reply {
	ch := r.Choices[0]
	u := r.Usage
	msg := agent.Message{
		Role:             "assistant",
		Content:          ch.Message.Content,
		ReasoningDetails: details(ch.Message.ReasoningDetails),
	}
	for _, tc := range ch.Message.ToolCalls {
		args := cmp.Or(tc.Function.Arguments, "{}") // <1>
		msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: jsontext.Value(args), // <2>
		})
	}
	return agent.Reply{
		Message:      msg,
		FinishReason: ch.FinishReason,
		Usage:        toUsage(u),
	}
}

// end::to-reply[]

// toUsage converts the API's usage counts.
func toUsage(u chatUsage) agent.Usage {
	return agent.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CachedTokens:     u.PromptDetails.CachedTokens,
		ReasoningTokens:  u.CompletionDetails.ReasoningTokens,
		Cost:             u.Cost,
	}
}

// details returns reasoning details worth sending back: nil for none,
// including an explicit null or an empty list.
func details(v jsontext.Value) jsontext.Value {
	if s := string(v); s == "null" || s == "[]" {
		return nil
	}
	return v
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

func (c *Client) idleTimeout() time.Duration {
	return cmp.Or(c.IdleTimeout, 30*time.Second)
}

func (c *Client) timeout() time.Duration {
	if c.Timeout == 0 {
		return 2 * time.Minute
	}
	return c.Timeout
}
