package openrouter

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"strconv"
	"strings"
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

	// A reply constrained to a JSON Schema (chapter 8).
	ResponseFormat *responseFormat `json:"response_format,omitzero"`

	// Settings for asking about token probabilities (chapter 3).
	Temperature *float64 `json:"temperature,omitzero"` // 0 is a setting
	Logprobs    bool     `json:"logprobs,omitzero"`
	TopLogprobs int      `json:"top_logprobs,omitzero"`
	Provider    *routing `json:"provider,omitzero"`

	// Streaming (chapter 6).
	Stream        bool           `json:"stream,omitzero"`
	StreamOptions *streamOptions `json:"stream_options,omitzero"`
}

// responseFormat asks for a reply that matches a JSON Schema.
type responseFormat struct {
	Type       string     `json:"type"` // "json_schema"
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema jsontext.Value `json:"schema"`
}

// streamOptions asks for usage in the last chunk of a stream.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
	Strict      bool           `json:"strict,omitzero"` // chapter 7
}

// end::wire-tools[]

// tag::wire-message[]

type chatMessage struct {
	Role             string         `json:"role"`
	Content          content        `json:"content,omitzero"`
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

// tag::content[]

// content is a message's text: a plain string, or, for a cache point
// with text, one text part carrying a cache_control marker, which
// OpenRouter passes on to providers that cache only where told. An
// empty cache point goes as a plain string; providers reject an empty
// text part.
type content struct {
	Text  string
	Cache bool
}

type textPart struct {
	Type         string        `json:"type"` // always "text"
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitzero"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

//nolint:wrapcheck // json/v2 wraps a marshaler's errors itself
func (c content) MarshalJSONTo(enc *jsontext.Encoder) error {
	if !c.Cache || c.Text == "" {
		return enc.WriteToken(jsontext.String(c.Text))
	}
	return json.MarshalEncode(enc, []textPart{{Type: "text", Text: c.Text,
		CacheControl: &cacheControl{Type: "ephemeral"}}})
}

// end::content[]

// IsZero reports whether there is no text, so omitzero leaves the
// content out whether or not the message is a cache point.
func (c content) IsZero() bool { return c.Text == "" }

// UnmarshalJSONFrom reads a reply's content: a string, null, or a list
// of parts, whose texts are joined.
func (c *content) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	v, err := dec.ReadValue()
	if err != nil {
		return err //nolint:wrapcheck // passed through to the decoder
	}
	switch v.Kind() {
	case 'n':
		*c = content{}
		return nil
	case '"':
		return json.Unmarshal(v, &c.Text) //nolint:wrapcheck // as above
	}
	var parts []textPart
	if err := json.Unmarshal(v, &parts); err != nil {
		return err //nolint:wrapcheck // as above
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	*c = content{Text: b.String()}
	return nil
}

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
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"` // chapter 8
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// apiError is the error object the API sends, in an error response or
// inside a 200 response when a provider fails.
type apiError struct {
	Code     jsontext.Value `json:"code"` // a number, or a string in streams
	Message  string         `json:"message"`
	Metadata struct {
		LimitSource string `json:"limit_source"`
	} `json:"metadata"`
}

// asAPIError turns an error object from inside a reply into an
// *APIError, with the status its code names, or 502 when the code
// isn't a number, so the classifier can tell a provider that failed
// with a 429 from one that refused the request (chapter 12).
func (e *apiError) asAPIError() *APIError {
	msg := e.Message
	if e.status() == 0 { // a code in words, such as "server_error"
		msg = e.String()
	}
	return &APIError{Status: cmp.Or(e.status(), http.StatusBadGateway),
		Message: msg, LimitSource: e.Metadata.LimitSource}
}

// status returns the error's code as an HTTP status, or 0 if it isn't
// a number.
func (e *apiError) status() int {
	n, err := strconv.Atoi(string(e.Code))
	if err != nil {
		return 0
	}
	return n
}

// String describes the error with its code, whichever form it took.
func (e *apiError) String() string {
	code := strings.Trim(string(e.Code), `"`)
	return strings.TrimSpace(code + " " + e.Message)
}

// end::wire-usage[]
