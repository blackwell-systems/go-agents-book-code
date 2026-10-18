package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"
)

// tag::policy[]

// A Policy says how hard Do tries. The zero Policy makes four attempts
// within five minutes, longer than one call's own two-minute deadline,
// with full-jitter backoff, and no shared budget or breaker.
type Policy struct {
	Attempts int           // tries, the first included; 0 means 4
	Within   time.Duration // start no retry after this; 0 means 5m
	Each     time.Duration // one attempt's deadline; 0 means none
	MaxWait  time.Duration // longest server-asked wait; 0 means 1m

	Backoff     Backoff
	RetryBudget *RetryBudget // shared with other runs, or nil
	Breaker     *Breaker     // shared with other runs, or nil

	// Wait, if set, runs before each attempt and before the attempt's
	// clock starts, such as chapter 9's shared limiter, so time spent
	// queueing never counts against Each.
	Wait func(context.Context) error

	// OnRetry, if set, is told about each retry before its wait, for
	// logs and traces.
	OnRetry func(n int, err error, d Decision, wait time.Duration)
}

// end::policy[]

// tag::retry-error[]

// A RetryError is the last error of a call that was retried until the
// policy gave up. It reports itself as not retryable, so a layer
// outside, another wrapper or the caller's own loop, doesn't retry the
// whole series again and multiply the attempts.
type RetryError struct {
	Attempts int
	Err      error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("gave up after %d attempts: %v",
		e.Attempts, e.Err)
}

// Unwrap returns the last attempt's error.
func (e *RetryError) Unwrap() error { return e.Err }

// Retryable is always false: the retries already happened.
func (e *RetryError) Retryable() bool { return false }

// end::retry-error[]

// tag::do[]

// Do calls op until it succeeds, fails in a way not worth retrying, or
// the policy gives up. op must return once its context is done.
func Do(ctx context.Context, p Policy,
	op func(context.Context) error) error {
	start := time.Now()
	var last error
	for n := 1; ; n++ {
		err := p.try(context.WithValue(ctx, attemptKey{}, n), op) // <1>
		if open, ok := errors.AsType[*OpenError](err); ok {
			err := p.waitOpen(ctx, open, start, last, n)
			if err != nil {
				return err
			}
			n-- // <2>
			continue
		}
		d := Classify(ctx, err)
		if err == nil || !d.Retry {
			return err
		}
		last = err
		wait, err := p.wait(n, d, time.Since(start), err) // <3>
		if err != nil {
			return err
		}
		if err := sleep(ctx, wait, last); err != nil {
			return err
		}
	}
}

// end::do[]

type attemptKey struct{}

// AttemptFrom returns which attempt of a call made by Do ctx belongs
// to, from 1, or 0 for a call made outside Do.
func AttemptFrom(ctx context.Context) int {
	n, _ := ctx.Value(attemptKey{}).(int)
	return n
}

// tag::wait-open[]

// waitOpen waits out an open breaker when it will close before the
// policy's time runs out; otherwise it gives up, with the last
// attempt's error if there was one.
func (p Policy) waitOpen(ctx context.Context, open *OpenError,
	start time.Time, last error, n int) error {
	if time.Since(start)+open.For <= cmp.Or(p.Within, 5*time.Minute) {
		return sleep(ctx, open.For, open)
	}
	var err error = open
	if last != nil {
		err = fmt.Errorf("%w; last attempt: %w", open, last)
	}
	return &RetryError{Attempts: n - 1, Err: err}
}

// end::wait-open[]

// tag::try[]

// try makes one attempt: past the breaker, through the Wait hook, then
// op under the attempt's own deadline, and tells the budget and the
// breaker how it went.
func (p Policy) try(ctx context.Context,
	op func(context.Context) error) (err error) {
	probe, err := p.Breaker.Allow()
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			p.Breaker.Release(probe) // a panic, or Wait failed
		}
	}()
	if p.Wait != nil {
		if err := p.Wait(ctx); err != nil {
			return fmt.Errorf("wait before the attempt: %w", err)
		}
	}
	err = attempt(ctx, p.Each, op)
	p.record(ctx, err, probe)
	done = true
	return err
}

// end::try[]

// tag::record[]

// record tells the shared budget and breaker how a call went. An
// answer, even a refusal such as a 400, says the provider is up, but
// only a success earns the budget a token; a failure worth retrying
// counts against both; a call the caller cancelled says nothing.
func (p Policy) record(ctx context.Context, err error, probe bool) {
	switch d := Classify(ctx, err); {
	case err == nil:
		p.RetryBudget.Record(true)
		p.Breaker.Record(true)
	case ctx.Err() != nil:
		p.Breaker.Release(probe)
	case d.Retry:
		p.RetryBudget.Record(false)
		p.Breaker.Record(false)
	default:
		p.Breaker.Record(true)
	}
}

// end::record[]

// tag::wait[]

// wait returns how long to wait before retry n, after err, or a
// *RetryError saying why to give up.
func (p Policy) wait(n int, d Decision, spent time.Duration,
	err error) (time.Duration, error) {
	give := func(why string) (time.Duration, error) {
		return 0, &RetryError{n, fmt.Errorf("%s: %w", why, err)}
	}
	if n >= cmp.Or(p.Attempts, 4) {
		return give("no attempts left")
	}
	if !p.RetryBudget.Allow() {
		return give("retry budget spent")
	}
	wait := p.Backoff.Delay(n)
	if d.After > 0 { // <1>
		if d.After > cmp.Or(p.MaxWait, time.Minute) {
			return give(fmt.Sprintf("asked to wait %v", d.After))
		}
		wait = d.After + p.Backoff.Delay(1)
	}
	if spent+wait > cmp.Or(p.Within, 5*time.Minute) {
		return give("out of time")
	}
	if p.OnRetry != nil {
		p.OnRetry(n, err, d, wait)
	}
	return wait, nil
}

// end::wait[]

// tag::attempt[]

// attempt runs op once under its own deadline. When that deadline is
// what ended it, the error says so, whatever op wrapped.
func attempt(ctx context.Context, each time.Duration,
	op func(context.Context) error) error {
	if each <= 0 {
		return op(ctx)
	}
	actx, cancel := context.WithTimeoutCause(ctx, each,
		ErrAttemptTimeout)
	defer cancel()
	err := op(actx)
	if err != nil && ctx.Err() == nil && // <1>
		errors.Is(context.Cause(actx), ErrAttemptTimeout) {
		return fmt.Errorf("%w: %w", ErrAttemptTimeout, err) // <2>
	}
	return err
}

// end::attempt[]

// sleep waits d, or until ctx is done; then it returns why it stopped,
// with the error that led to the wait.
func sleep(ctx context.Context, d time.Duration, last error) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		err := ctx.Err()
		if cause := context.Cause(ctx); !errors.Is(err, cause) {
			err = fmt.Errorf("%w: %w", err, cause)
		}
		return fmt.Errorf("stopped waiting to retry: %w; last attempt: %w",
			err, last)
	}
}
