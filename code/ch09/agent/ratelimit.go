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

	mu   sync.Mutex
	next time.Time // when the next call may start
}

// end::limiter[]

// tag::wait[]

// Wait returns when the caller may start a call, or when ctx is done,
// with why. A turn given up this way is still used, which errs on the
// side of sending fewer requests.
func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.Every)
	l.mu.Unlock()
	if !at.After(now) {
		return nil
	}
	t := time.NewTimer(at.Sub(now))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the rate limit: %w",
			context.Cause(ctx))
	}
}

// end::wait[]

// tag::paced[]

// Paced returns a StreamModel that waits for l before each call to m.
// Every run given the same Limiter shares its pace.
func Paced(m StreamModel, l *Limiter) StreamModel { return paced{m, l} }

type paced struct {
	m StreamModel
	l *Limiter
}

func (p paced) Stream(ctx context.Context,
	req Request) iter.Seq2[Delta, error] {
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
