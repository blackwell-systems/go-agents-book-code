package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
)

// tag::tool-timeout[]

// ErrToolTimeout is the cause set when a tool runs past its Timeout. It
// is in the ErrTool family, so it reaches the model as an error result
// and never leaves the loop, and it reports itself as a timeout, so the
// classifier counts it worth a retry for a tool that is safe to retry.
var ErrToolTimeout error = &toolTimeout{}

type toolTimeout struct{}

func (*toolTimeout) Error() string {
	return "agent: the tool timed out"
}
func (*toolTimeout) Timeout() bool   { return true }
func (*toolTimeout) Is(t error) bool { return t == ErrTool }

// end::tool-timeout[]

// tag::outcome-unknown[]

// ErrOutcomeUnknown means a tool that may change something outside the
// run stopped without saying whether it did, such as one that timed
// out after it sent its request: running it again could do it twice,
// so the run stops instead (chapter 15 resolves it). It stands alone:
// the cause is in the message, never in the chain, since ErrTool
// errors never leave the loop.
var ErrOutcomeUnknown = errors.New("agent: a tool's outcome is unknown")

// end::outcome-unknown[]

// tag::timeouts[]

// Timeouts is a ToolWare that applies each tool's Timeout. A timeout
// the tool reports itself, such as an MCP client's own deadline, counts
// the same. A tool that ignores its context can't be stopped: Timeouts
// waits for it, and a result that arrives late still counts.
func Timeouts(t Tool) Tool {
	if t.Timeout <= 0 {
		return t
	}
	run := t.Run
	t.Run = func(ctx context.Context,
		args jsontext.Value) (string, error) {
		tctx, cancel := context.WithTimeoutCause(ctx, t.Timeout,
			ErrToolTimeout)
		defer cancel()
		out, err := run(tctx, args)
		if err == nil || ctx.Err() != nil || // <1>
			!errors.Is(context.Cause(tctx), ErrToolTimeout) &&
				!timedOut(err) {
			return out, err
		}
		return "", timeoutError(t, err)
	}
	return t
}

// end::timeouts[]

// tag::timeout-error[]

// timeoutError is the error for a call to t that ran out of time. For a
// tool safe to run again it is ErrToolTimeout, which the model reads;
// for one that may have changed something it is ErrOutcomeUnknown,
// which stops the run.
func timeoutError(t Tool, err error) error {
	if !t.ReadOnly && !t.Idempotent { // <1>
		//nolint:errorlint // err stays out of the chain: ErrTool
		// errors never leave the loop
		return fmt.Errorf("%w: %s timed out after %v (%v)",
			ErrOutcomeUnknown, t.Def.Name, t.Timeout, err)
	}
	return &toolError{ErrToolTimeout, fmt.Sprintf(
		"%s timed out after %v; try again, or another way",
		t.Def.Name, t.Timeout)}
}

// end::timeout-error[]

// tag::retry-tools[]

// RetryTools returns a ToolWare that retries a tool's failed calls as p
// says, but only for a tool marked ReadOnly or Idempotent, which is
// safe to run twice, or a Keyed tool whose call has a key, which the
// retries resend (chapter 15); every other tool runs once, as before.
func RetryTools(p Policy) ToolWare {
	return func(t Tool) Tool {
		if !t.ReadOnly && !t.Idempotent && !t.Keyed {
			return t
		}
		run := t.Run
		t.Run = func(ctx context.Context,
			args jsontext.Value) (string, error) {
			if _, keyed := KeyFrom(ctx); !t.ReadOnly && !t.Idempotent &&
				!keyed {
				return run(ctx, args) // no key: once
			}
			var out string
			err := Do(ctx, p, func(ctx context.Context) error {
				var err error
				out, err = run(ctx, args)
				return err
			})
			return out, gaveUp(t, err)
		}
		return t
	}
}

// gaveUp is the error for a keyed call whose retries ran out: each try
// may have reached the other side, so its outcome is unknown, and the
// run stops for the ladder to repeat it with the same key. The model,
// told to try again, would make a new call with a new key (chapter 15).
func gaveUp(t Tool, err error) error {
	if _, ok := errors.AsType[*RetryError](err); !ok || !t.Keyed ||
		t.ReadOnly || t.Idempotent {
		return err
	}
	//nolint:errorlint // err stays out of the chain, as in timeoutError
	return fmt.Errorf("%w: %s gave up after retries (%v)",
		ErrOutcomeUnknown, t.Def.Name, err)
}

// end::retry-tools[]
