package main

import (
	"context"
	"fmt"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// tag::complete[]

// complete sends one conversation to m, with no tools, and returns the
// reply's text. A reply the model didn't finish is an error, never an
// answer, as in chapter 5's hardened loop.
func complete(ctx context.Context, m agent.Model,
	msgs []agent.Message) (string, error) {
	reply, err := m.Generate(ctx, agent.Request{Messages: msgs})
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	switch reply.FinishReason {
	case "length":
		return "", agent.ErrOutputTruncated
	case "content_filter":
		return "", agent.ErrOutputFiltered
	case "error":
		return "", agent.ErrProviderFailed
	}
	return reply.Message.Content, nil
}

// ask sends one prompt to m and returns the reply's text.
func ask(ctx context.Context, m agent.Model,
	prompt string) (string, error) {
	msgs := []agent.Message{{Role: "user", Content: prompt}}
	return complete(ctx, m, msgs)
}

// end::complete[]
