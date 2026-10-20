package main

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Message is one entry in the conversation. Its JSON tags match the
// chat completions API, so the client sends and receives it as is.
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content,omitzero"`
	ToolCalls  []ToolCall `json:"tool_calls,omitzero"`
	ToolCallID string     `json:"tool_call_id,omitzero"`
	// Reasoning some models return, sent back unchanged.
	Reasoning jsontext.Value `json:"reasoning_details,omitzero"`
}

// ToolCall is the model asking to run one tool.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON, in a string
	} `json:"function"`
}

// Tool is something the agent can do: a definition the model reads,
// and a Go function the model never sees.
type Tool struct {
	Name        string
	Description string
	Parameters  jsontext.Value // a JSON Schema for the arguments
	Run         func(ctx context.Context, args string) (string, error)
}

// ErrMaxTurns means the run used every turn without an answer.
var ErrMaxTurns = errors.New("firstagent: turn limit reached")

// errCutOff means the model ran out of room partway through a reply.
var errCutOff = errors.New("firstagent: reply cut off")

// Run sends the conversation to the model, runs the tools it asks for,
// and repeats until the model answers or maxTurns runs out. It returns
// the answer and the whole conversation, and reports each step to log.
func Run(ctx context.Context, c *Client, tools []Tool, msgs []Message,
	maxTurns int, log io.Writer) (string, []Message, error) {
	var calls int
	var spent Usage
	defer func() { // what the run cost, however it ended
		fmt.Fprintf(log, "%d model calls, %d tokens in, %d out, $%.4f\n",
			calls, spent.PromptTokens, spent.CompletionTokens, spent.Cost)
	}()
	for turn := range maxTurns {
		reply, err := c.Chat(ctx, msgs, tools)
		if err != nil {
			return "", msgs, fmt.Errorf("turn %d: %w", turn+1, err)
		}
		calls++
		spent.PromptTokens += reply.Usage.PromptTokens
		spent.CompletionTokens += reply.Usage.CompletionTokens
		spent.Cost += reply.Usage.Cost
		if reply.FinishReason == "length" {
			return "", msgs, errCutOff
		}
		msgs = append(msgs, reply.Message) // the reply first, then results
		if len(reply.Message.ToolCalls) == 0 {
			return reply.Message.Content, msgs, nil // no tools: the answer
		}
		for _, call := range reply.Message.ToolCalls {
			fmt.Fprintf(log, "turn %d: %s %s\n", turn+1,
				call.Function.Name, call.Function.Arguments)
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    runTool(ctx, tools, call),
			})
		}
	}
	return "", msgs, ErrMaxTurns
}

// runTool runs one call and returns what to tell the model. A failure
// becomes text the model can read and act on, so the run goes on.
func runTool(ctx context.Context, tools []Tool, call ToolCall) string {
	var names []string
	for _, t := range tools {
		if t.Name == call.Function.Name {
			out, err := t.Run(ctx, call.Function.Arguments)
			if err != nil {
				return "Error: " + err.Error()
			}
			if out == "" {
				return "(no output)"
			}
			return out
		}
		names = append(names, t.Name)
	}
	return fmt.Sprintf("Error: no tool named %q. The tools are: %s.",
		call.Function.Name, strings.Join(names, ", "))
}
