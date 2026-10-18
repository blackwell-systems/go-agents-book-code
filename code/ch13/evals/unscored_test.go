package evals

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch13/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch13/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch13/replay"
)

// TestUnscoredError checks which stop errors are the provider's or the
// harness's, wrapped as the loop wraps them.
func TestUnscoredError(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("run agent: generate: %w", err) }
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"retries ran out", wrap(&agent.RetryError{Attempts: 3, Err: errors.New("503")}), true},
		{"out of credit", wrap(&openrouter.APIError{Status: 402}), true},
		{"bad key", wrap(&anthropic.APIError{Status: 401}), true},
		{"provider failed", wrap(agent.ErrProviderFailed), true},
		{"tool can't run", wrap(agent.ErrToolFatal), true},
		{"outcome unknown", wrap(agent.ErrOutcomeUnknown), true},
		{"runner stopped", wrap(fmt.Errorf("%w: %w", context.Canceled, ErrStopped)), true},
		{"out of turns", wrap(agent.ErrMaxTurns), false},
		{"refused", wrap(agent.ErrRefused), false},
		{"cut off", wrap(agent.ErrOutputTruncated), false},
		{"bad request", wrap(&openrouter.APIError{Status: 400}), false},
		{"no final answer", wrap(agent.ErrNoFinalAnswer), false},
		{"over budget", wrap(agent.ErrBudgetExceeded), false},
		{"replay diverged", wrap(replay.ErrNoRecording), true},
		{"no endpoint", wrap(&openrouter.APIError{Status: 404}), false},
	} {
		if got := UnscoredError(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestFatal checks which errors stop the whole eval.
func TestFatal(t *testing.T) {
	if !Fatal(fmt.Errorf("x: %w", &openrouter.APIError{Status: 402})) ||
		!Fatal(&anthropic.APIError{Status: 401}) ||
		Fatal(&openrouter.APIError{Status: 503}) || Fatal(agent.ErrMaxTurns) {
		t.Error("Fatal misjudged a status")
	}
}
