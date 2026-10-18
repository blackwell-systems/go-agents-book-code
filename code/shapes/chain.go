package main

import (
	"context"
	"fmt"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// tag::step[]

// A Step is one call in a chain: the prompt it sends, built from the
// last step's output, and a check its reply must pass before the next
// step sees it.
type Step struct {
	Name   string
	Prompt func(in string) string
	Check  func(out string) error // nil means no check
}

// end::step[]

// tag::chain[]

// Chain runs steps in order, each with the last one's output, starting
// from input, and returns the last step's output. It stops at the first
// call that fails or reply that fails its check, so a bad draft never
// reaches the next step.
//
// Use a chain when the task has steps you know in advance. It costs one
// call per step, made one after another, so its latency is the sum of
// the steps; each step's prompt holds only the last output, which keeps
// the calls small and each one easy to test on its own.
func Chain(ctx context.Context, m agent.Model, input string,
	steps []Step) (string, error) {
	out := input
	for _, s := range steps {
		reply, err := ask(ctx, m, s.Prompt(out))
		if err != nil {
			return "", fmt.Errorf("step %s: %w", s.Name, err)
		}
		if s.Check != nil {
			if err := s.Check(reply); err != nil { // <1>
				return "", fmt.Errorf("step %s: %w", s.Name, err)
			}
		}
		out = reply
	}
	return out, nil
}

// end::chain[]
