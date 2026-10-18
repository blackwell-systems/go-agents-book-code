package agent

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// tag::policy[]

// A ContextPolicy builds the messages one request sends from the full
// conversation. The loop keeps the full conversation in
// Result.Messages and never rewrites it; a policy only decides what the
// model sees. Its state, such as how much it has trimmed, lives in a
// ContextState that the loop holds and passes back each turn, so a
// policy keeps nothing between calls and the same conversation and
// state always build the same request.
type ContextPolicy interface {
	Build(ctx context.Context, msgs []Message,
		st ContextState) (Window, error)
}

// A Window is what a policy built: the request's messages, the state
// to pass back next turn, and what building it cost, such as a summary
// call.
type Window struct {
	Messages []Message
	State    ContextState
	Usage    Usage
}

// ContextState is a context policy's memory between turns. Each field
// is an index into the full conversation or text derived from it.
type ContextState struct {
	Cut     int    // Trim: messages before this are left out
	Cleared int    // ClearResults: tool results before this are cleared
	Summary string // Compact: the latest summary of earlier turns
	Covered int    // Compact: the summary covers messages before this
}

// end::policy[]

// tag::chain[]

// Chain applies policies in order, each to the messages the one before
// it built. The state's indexes count the full conversation, so at
// most one policy in a chain may shorten it, Trim or Compact, and the
// policies before it must keep its length, such as ClearResults before
// Trim. Compact clears through its Sent field instead, so its summary
// reads whole results. CacheMarks goes last, to mark what is sent.
func Chain(ps ...ContextPolicy) ContextPolicy { return chain(ps) }

type chain []ContextPolicy

func (c chain) Build(ctx context.Context, msgs []Message,
	st ContextState) (Window, error) {
	if n := shortening(c); n > 1 {
		return Window{}, fmt.Errorf("chain: %d policies shorten the "+
			"conversation; use one, Trim or Compact", n)
	}
	w := Window{Messages: msgs, State: st}
	for i, p := range c {
		next, err := p.Build(ctx, w.Messages, w.State)
		if err != nil {
			return Window{Usage: w.Usage.Add(next.Usage)},
				fmt.Errorf("policy %d of %d: %w", i+1, len(c), err)
		}
		next.Usage = w.Usage.Add(next.Usage)
		w = next
	}
	return w, nil
}

// end::chain[]

// shortening counts the policies that shorten the conversation.
func shortening(ps []ContextPolicy) int {
	n := 0
	for _, p := range ps {
		switch p.(type) {
		case Trim, *Trim, Compact, *Compact:
			n++
		}
	}
	return n
}

// tag::turns[]

// head returns how many messages at the start of a conversation every
// request keeps: the system prompt, if any, and the task, the first
// user message. A policy never drops or changes them.
func head(msgs []Message) int {
	n := 0
	for n < len(msgs) && msgs[n].Role == "system" {
		n++
	}
	if n < len(msgs) && msgs[n].Role == "user" {
		n++
	}
	return n
}

// turnStarts returns the index of each turn after the head. A turn is
// an assistant message with the tool messages that answer it, or a
// user message on its own. Policies drop and keep whole turns, since a
// tool call without its result, or a result without its call, makes
// the API reject the request.
func turnStarts(msgs []Message, from int) []int {
	var starts []int
	for i := from; i < len(msgs); i++ {
		if msgs[i].Role != "tool" {
			starts = append(starts, i)
		}
	}
	return starts
}

// end::turns[]

// tag::estimate[]

// Estimate guesses how many tokens messages will take, before sending
// them, to decide whether to trim: about four bytes per token, plus a
// few tokens of formatting per message. It is rough; usage gives the
// real count after the call, and a policy's limits leave room for the
// difference.
func Estimate(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) + len(m.ReasoningDetails)
		for _, c := range m.ToolCalls {
			n += len(c.Name) + len(c.Arguments)
		}
	}
	return n/4 + 4*len(msgs)
}

// end::estimate[]

// note is a message the harness adds to a request to say what it left
// out. It is a user message, since only the harness writes it.
func note(format string, args ...any) Message {
	return Message{Role: "user", Content: "[" + fmt.Sprintf(format, args...) + "]"}
}

// clip cuts s to at most n bytes, ending it with "..." if it was cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n-- // don't cut a character in half
	}
	return s[:n] + "..."
}
