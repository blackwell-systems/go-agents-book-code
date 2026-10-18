package agent

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"time"
)

// tag::limiter[]

// A Limiter paces model calls shared by runs going on at once, so six
// agents don't send six requests in the same instant: calls start at
// least Every apart, in the order they asked. It spreads requests out;
// it doesn't retry the ones a provider refuses (chapter 12 does).
type Limiter struct {
	Every time.Duration

	mu     sync.Mutex
	next   time.Time // when the next call may start
	paused time.Time // no call starts before this (chapter 12)
}

// end::limiter[]

// tag::wait[]

// Wait returns when the caller may start a call, or when ctx is done,
// with why. A turn given up this way is still used, which errs on the
// side of sending fewer requests. A caller whose turn comes during a
// Pause takes a new turn after it.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		if err := sleepUntil(ctx, l.reserve()); err != nil {
			return fmt.Errorf("wait for the rate limit: %w", err)
		}
		l.mu.Lock()
		held := l.paused.After(time.Now())
		l.mu.Unlock()
		if !held {
			return nil
		}
	}
}

// end::wait[]

// reserve takes the next turn and returns when it starts.
func (l *Limiter) reserve() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	at := time.Now()
	for _, t := range []time.Time{l.next, l.paused} {
		if t.After(at) {
			at = t
		}
	}
	l.next = at.Add(l.Every)
	return at
}

// sleepUntil returns at t, or with ctx's cause if ctx ends first.
func sleepUntil(ctx context.Context, at time.Time) error {
	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx) //nolint:wrapcheck // the caller wraps
	}
}

// tag::pause[]

// Pause holds every caller of Wait for d from now, those already
// waiting included, when the provider has said, with a 429 and a delay,
// that the whole account must wait (chapter 12). A pause shorter than
// one already set does nothing.
func (l *Limiter) Pause(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := time.Now().Add(d); until.After(l.paused) {
		l.paused = until
	}
}

// end::pause[]

// tag::paced[]

// Paced returns a StreamModel that waits for l before each call to m.
// Every run given the same Limiter shares its pace.
func Paced(m StreamModel, l *Limiter) StreamModel { return paced{m, l} }

type paced struct {
	m StreamModel
	l *Limiter
}

func (p paced) Stream(ctx context.Context, req Request) iter.Seq2[Delta, error] {
	return func(yield func(Delta, error) bool) {
		if err := p.l.Wait(ctx); err != nil {
			yield(Delta{}, err)
			return
		}
		for d, err := range p.m.Stream(ctx, req) {
			if !yield(d, err) {
				return
			}
		}
	}
}

// end::paced[]

// PacedModel returns a Model that waits for l before each call to m,
// for model calls that don't stream, such as a compaction summary.
func PacedModel(m Model, l *Limiter) Model { return pacedModel{m, l} }

type pacedModel struct {
	m Model
	l *Limiter
}

func (p pacedModel) Generate(ctx context.Context, req Request) (Reply, error) {
	if err := p.l.Wait(ctx); err != nil {
		return Reply{}, err
	}
	r, err := p.m.Generate(ctx, req)
	if err != nil {
		return r, fmt.Errorf("paced: %w", err)
	}
	return r, nil
}
