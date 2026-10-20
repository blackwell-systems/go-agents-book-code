package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client calls a model through an OpenAI-compatible chat completions
// API, such as OpenRouter's. It also counts what its calls cost, so a
// comparison can charge each approach for its own calls.
type Client struct {
	BaseURL string       // such as https://openrouter.ai/api/v1
	APIKey  string       // sent as a bearer token
	Model   string       // such as anthropic/claude-haiku-5.5
	HTTP    *http.Client // http.DefaultClient if nil

	Calls int   // chat and embedding calls made so far
	Spent Usage // what they used and cost
}

// Reply is one answer from the model, with what it cost.
type Reply struct {
	Message      Message
	FinishReason string // stop, tool_calls, or length
	Usage        Usage
}

// Usage is the size and price of one call.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"` // in dollars
}

// The request and response, as JSON. Message is already in the API's
// shape, so only the outer layers need types of their own.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []toolDef `json:"tools,omitzero"`
}

type toolDef struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  jsontext.Value `json:"parameters"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		FinishReason string  `json:"finish_reason"`
		Message      Message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends the conversation and the tool definitions, and returns
// the model's reply. Each call gets two minutes at most.
func (c *Client) Chat(ctx context.Context, msgs []Message,
	tools []Tool) (Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req := chatRequest{Model: c.Model, Messages: msgs}
	for _, t := range tools {
		var d toolDef
		d.Type = "function"
		d.Function.Name = t.Name
		d.Function.Description = t.Description
		d.Function.Parameters = t.Parameters
		req.Tools = append(req.Tools, d)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return Reply{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	reply, err := readReply(resp)
	if err == nil {
		c.count(reply.Usage)
	}
	return reply, err
}

// count adds one call's usage to the client's totals.
func (c *Client) count(u Usage) {
	c.Calls++
	c.Spent.PromptTokens += u.PromptTokens
	c.Spent.CompletionTokens += u.CompletionTokens
	c.Spent.Cost += u.Cost
}

// readReply decodes a response, turning every kind of failure the API
// can report into an error.
func readReply(resp *http.Response) (Reply, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("read response: %w", err)
	}
	var r chatResponse
	err = json.Unmarshal(data, &r)
	switch {
	case resp.StatusCode != http.StatusOK && r.Error != nil:
		return Reply{}, fmt.Errorf("model API: %s: %s", resp.Status,
			r.Error.Message)
	case resp.StatusCode != http.StatusOK:
		return Reply{}, fmt.Errorf("model API: %s", resp.Status)
	case err != nil:
		return Reply{}, fmt.Errorf("decode response: %w", err)
	case r.Error != nil: // a 200 that carries an error
		return Reply{}, fmt.Errorf("model API: %s", r.Error.Message)
	case len(r.Choices) == 0:
		return Reply{}, errors.New("model API: reply has no choices")
	}
	ch := r.Choices[0]
	if s := string(ch.Message.Reasoning); s == "null" || s == "[]" {
		ch.Message.Reasoning = nil // nothing worth sending back
	}
	return Reply{Message: ch.Message, FinishReason: ch.FinishReason,
		Usage: r.Usage}, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
