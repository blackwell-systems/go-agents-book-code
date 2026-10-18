// Package agent is the agent loop and the types it works with.
package agent

import (
	"context"
	"encoding/json/jsontext"
)

// tag::model[]

// Model is anything that can produce the next reply in a conversation:
// the scripted fake in this chapter, a real model from chapter 2.
type Model interface {
	Generate(ctx context.Context, req Request) (Reply, error)
}

// end::model[]

// tag::messages[]

// Request is what the harness sends the model on each turn.
type Request struct {
	Messages []Message
	Tools    []ToolDef
}

// Message is one entry in the conversation.
type Message struct {
	Role       string     // "system", "user", "assistant", or "tool"
	Content    string     // empty when the message has tool calls
	ToolCalls  []ToolCall // set on assistant messages that call tools
	ToolCallID string     // on tool messages: the call this answers
	IsError    bool       // set on tool messages that report a failure
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

// Reply is the model's answer to one Request.
type Reply struct {
	Message Message // the assistant's message: text, tool calls, or both
}

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
}

// Result is what a run produced, whether or not it ended in an error.
type Result struct {
	Messages []Message
}

// end::limits[]
