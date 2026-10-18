// Package agent is the agent loop and the types it works with.
package agent

import (
	"context"
	"encoding/json/jsontext"
	"time"
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
	MaxTokens int // longest reply allowed; 0 means the provider's default

	// ResponseFormat, if set, is a JSON Schema the reply's text must
	// match, for calls that use no tools, such as a compaction summary
	// (chapter 8).
	ResponseFormat jsontext.Value
}

// end::request[]

// Message is one entry in the conversation.
type Message struct {
	Role       string     // "system", "user", "assistant", or "tool"
	Content    string     // may be empty when the message has tool calls
	ToolCalls  []ToolCall // set on assistant messages that call tools
	ToolCallID string     // set on tool messages: which call this answers
	IsError    bool       // set on tool messages that report a failure

	// ReasoningDetails is a reasoning model's record of its thinking,
	// sent back unchanged with the tool calls it led to (chapter 4).
	ReasoningDetails jsontext.Value

	// tag::reasoning-from[]

	// ReasoningFrom names the client that produced ReasoningDetails,
	// such as "anthropic@openrouter.ai" (chapter 11). A client sends
	// reasoning back only when it produced it; empty means chapter 2's
	// OpenRouter client.
	ReasoningFrom string

	// end::reasoning-from[]

	// CachePoint marks the end of a stable start of the request: a
	// provider that needs explicit markers may cache everything up to
	// and including this message (chapter 8).
	CachePoint bool
}

// end::messages[]

// tag::tools[]

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  jsontext.Value // a JSON Schema

	// Strict asks the provider to hold the arguments to the schema,
	// where it can (chapter 7).
	Strict bool
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

	// ID is the provider's ID for this generation, such as OpenRouter's
	// "gen-...", kept even when a stream failed, so its cost can be
	// looked up afterward (chapter 12).
	ID string
}

// Usage is what one model call consumed.
type Usage struct {
	PromptTokens     int     // everything sent, counted in tokens
	CompletionTokens int     // everything the model wrote
	CachedTokens     int     // prompt tokens served from the cache
	ReasoningTokens  int     // completion tokens spent on reasoning
	Cost             float64 // in dollars, as the provider reported it

	// CacheWriteTokens are prompt tokens written to the cache, which
	// some providers bill above the usual input price (chapter 8).
	CacheWriteTokens int

	// CostEstimated says Cost was computed from a price table rather
	// than reported by the provider (chapter 12).
	CostEstimated bool
}

// Add returns the sum of two Usages.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + v.PromptTokens,
		CompletionTokens: u.CompletionTokens + v.CompletionTokens,
		CachedTokens:     u.CachedTokens + v.CachedTokens,
		ReasoningTokens:  u.ReasoningTokens + v.ReasoningTokens,
		Cost:             u.Cost + v.Cost,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
		CostEstimated:    u.CostEstimated || v.CostEstimated,
	}
}

// end::reply[]

// tag::tool[]

// Tool is the harness's side of a tool: the definition the model sees,
// and the Go function that runs it. The model never sees Run.
type Tool struct {
	Def ToolDef
	Run func(ctx context.Context, args jsontext.Value) (string, error)

	// Safety flags (chapter 7). A tool with neither flag set may change
	// something outside the run.
	ReadOnly   bool // it only looks things up
	Idempotent bool // running it twice has the same effect as once

	// Final marks the tool that ends the run: once a call to it
	// succeeds, its arguments become Result.Final (chapter 7).
	Final bool

	// Timeout, if set, bounds one call to Run; a wrapper applies it
	// (chapter 12). Zero means the tool's own limits only.
	Timeout time.Duration
}

// end::tool[]

// tag::limits[]

// Limits holds the settings that bound a run.
type Limits struct {
	MaxTurns int
	MaxCost  float64 // in dollars; 0 means no cost budget (chapter 5)

	// Context, if set, builds each request's messages from the full
	// conversation: trimming, clearing, or summarizing old turns. Nil
	// sends the whole conversation, as before (chapter 8).
	Context ContextPolicy

	// Spend, if set, is a spending pool shared with other runs: the run
	// stops when the pool's limit is reached, whoever spent it, and adds
	// its own calls' cost to it (chapter 9).
	Spend *SpendPool
}

// Result is what a run produced, whether or not it ended in an error.
type Result struct {
	Messages []Message
	Usage    Usage // the run's total, over every model call (chapter 5)

	// Final holds the arguments of the final tool's successful call,
	// the run's structured answer (chapter 7).
	Final jsontext.Value

	// Context is the context policy's state when the run ended, such as
	// its latest summary (chapter 8).
	Context ContextState

	// Turns counts the model calls the run made, and Duration how long
	// it ran (chapter 12).
	Turns    int
	Duration time.Duration
}

// end::limits[]
