package evals

import (
	"context"
	"errors"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch13/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch13/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch13/replay"
)

// ErrStopped is the cause the eval runner gives when it stops its runs
// itself: on Ctrl-C, or after an error no other run would get past.
var ErrStopped = errors.New("evals: the runner stopped the run")

// tag::unscored[]

// UnscoredError reports whether a run's stop error is the provider's
// or the harness's fault rather than the agent's, so the run says
// nothing about the agent.
func UnscoredError(err error) bool {
	if _, ok := errors.AsType[*agent.RetryError](err); ok {
		return true // a provider error that outlasted the retries
	}
	if s := status(err); s == 401 || s == 402 {
		return true // the eval's key or account, which no retry fixes
	}
	return errors.Is(err, agent.ErrProviderFailed) || // <1>
		errors.Is(err, agent.ErrToolFatal) ||
		errors.Is(err, agent.ErrOutcomeUnknown) ||
		errors.Is(err, ErrStopped) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, replay.ErrNoRecording) // a replay that diverged
}

// Fatal reports whether err would stop every other run too: the key
// was refused, or the account is out of credit. A 402 for the
// in-flight budget is retried by chapter 12 and ends as a *RetryError,
// so a 402 that reaches here is the account's.
func Fatal(err error) bool {
	s := status(err)
	return s == 401 || s == 402
}

// end::unscored[]

// status returns the HTTP status of the API error in err, from either
// client, or 0.
func status(err error) int {
	if e, ok := errors.AsType[*openrouter.APIError](err); ok {
		return e.Status
	}
	if e, ok := errors.AsType[*anthropic.APIError](err); ok {
		return e.Status
	}
	return 0
}
