package openrouter

import (
	"encoding/json/jsontext"
	"fmt"
)

// The JSON shapes of OpenRouter's chat completions API. They mirror the
// API, not the agent's types: the API changes for its own reasons, and
// the agent's types change for the book's.

// tag::wire-request[]

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Tools     []chatTool    `json:"tools,omitzero"`
	MaxTokens int           `json:"max_tokens,omitzero"`

	// Settings for asking about token probabilities (chapter 3).
	Temperature *float64 `json:"temperature,omitzero"` // 0 is a setting
	Logprobs    bool     `json:"logprobs,omitzero"`
	TopLogprobs int      `json:"top_logprobs,omitzero"`
	Provider    *routing `json:"provider,omitzero"`
}

// routing tells OpenRouter which providers may serve a request.
type routing struct {
	Only              []string `json:"only,omitzero"`
	RequireParameters bool     `json:"require_parameters,omitzero"`
}

// end::wire-request[]

// tag::wire-tools[]

// chatTool is a tool definition. The API wraps each one in an object
// with a type, leaving room for kinds of tools other than functions.
type chatTool struct {
	Type     string       `json:"type"` // always "function"
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  jsontext.Value `json:"parameters"`
}

// end::wire-tools[]

// tag::wire-message[]

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitzero"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitzero"`
	ToolCallID       string         `json:"tool_call_id,omitzero"`
	ReasoningDetails jsontext.Value `json:"reasoning_details,omitzero"`
}

// chatToolCall is one tool call. The arguments are a string holding
// JSON, not a JSON object.
type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// end::wire-message[]

// tag::wire-response[]

type chatResponse struct {
	ID       string       `json:"id"`
	Model    string       `json:"model"`
	Provider string       `json:"provider"`
	Choices  []chatChoice `json:"choices"`
	Usage    chatUsage    `json:"usage"`
	Error    *apiError    `json:"error"`
}

// end::wire-response[]

// tag::wire-usage[]

type chatChoice struct {
	FinishReason string      `json:"finish_reason"`
	Message      chatMessage `json:"message"`
	Error        *apiError   `json:"error"` // a failure partway through
	Logprobs     *struct {
		Content []tokenLogprob `json:"content"`
	} `json:"logprobs"`
}

// tokenLogprob is one token the model wrote, with the log of its
// probability and the most likely tokens at that position.
type tokenLogprob struct {
	Token   string         `json:"token"`
	Bytes   []int          `json:"bytes"` // the token's UTF-8 bytes, if sent
	Logprob float64        `json:"logprob"`
	Top     []tokenLogprob `json:"top_logprobs"`
}

type chatUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	PromptDetails    struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// apiError is the error object the API sends, in an error response or
// inside a 200 response when a provider fails.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openrouter: provider error %d: %s",
		e.Code, e.Message)
}

// end::wire-usage[]
