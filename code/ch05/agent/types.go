// Package agent is the agent loop and the types it works with.
package agent

import (
	"context"
	"encoding/json/jsontext"
)

// tag::model[]

// Model is anything that can produce the next reply in a conversation:
// a scripted fake, or a real model such as the OpenRouter client.
type Model interface {
	Generate(ctx context.Context, req Request) (Reply, error)
}

// end::model[]

// tag::messages[]

// tag::request[]

// Request is what the harness sends the model on each turn.
type Request struct {
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int // longest reply; 0 means the provider's default
}

// end::request[]

// Message is one entry in the conversation.
type Message struct {
	Role       string     // "system", "user", "assistant", or "tool"
	Content    string     // empty when the message has tool calls
	ToolCalls  []ToolCall // set on assistant messages that call tools
	ToolCallID string     // on tool messages: the call this answers
	IsError    bool       // set on tool messages that report a failure

	// ReasoningDetails is a reasoning model's record of its thinking,
	// sent back unchanged with the tool calls it led to (chapter 4).
	ReasoningDetails jsontext.Value
}

// end::messages[]

// tag::tools[]

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  jsontext.Value // a JSON Schema
}

// ToolCall is the model asking the harness to run a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments jsontext.Value // JSON the model wrote; may be invalid
}

// end::tools[]

// tag::reply[]

// Reply is the model's answer to one Request.
type Reply struct {
	Message      Message // the assistant's text, tool calls, or both
	FinishReason string  // why the model stopped: "stop", "length", ...
	Usage        Usage   // tokens counted for this call, and its cost
}

// Usage is what one model call consumed.
type Usage struct {
	PromptTokens     int     // everything sent, counted in tokens
	CompletionTokens int     // everything the model wrote
	CachedTokens     int     // prompt tokens served from the cache
	ReasoningTokens  int     // completion tokens spent on reasoning
	Cost             float64 // in dollars, as the provider reported it
}

// Add returns the sum of two Usages.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + v.PromptTokens,
		CompletionTokens: u.CompletionTokens + v.CompletionTokens,
		CachedTokens:     u.CachedTokens + v.CachedTokens,
		ReasoningTokens:  u.ReasoningTokens + v.ReasoningTokens,
		Cost:             u.Cost + v.Cost,
	}
}

// end::reply[]

// tag::tool[]

// Tool is the harness's side of a tool: the definition the model sees,
// and the Go function that runs it. The model never sees Run.
type Tool struct {
	Def ToolDef
	Run func(ctx context.Context, args jsontext.Value) (string, error)
}

// end::tool[]

// tag::limits[]

// Limits holds the settings that bound a run.
type Limits struct {
	MaxTurns int
	MaxCost  float64 // in dollars; 0 means no cost budget (chapter 5)
}

// Result is what a run produced, whether or not it ended in an error.
type Result struct {
	Messages []Message
	Usage    Usage // the run's total, over every model call (chapter 5)
}

// end::limits[]
