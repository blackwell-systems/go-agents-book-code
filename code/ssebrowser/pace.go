package main

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// paced slows a StreamModel down to a speed a person can watch, for
// the demo: it waits before each reply, as a model does while it reads
// the prompt, and passes text on a word at a time.
type paced struct {
	m     agent.StreamModel
	think time.Duration // before each reply
	word  time.Duration // before each word
}

func (p paced) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		if err := sleep(ctx, p.think); err != nil {
			yield(agent.Delta{}, err)
			return
		}
		for d, err := range p.m.Stream(ctx, req) {
			if err != nil || d.Text == "" {
				if !yield(d, err) {
					return
				}
				continue
			}
			for w := range strings.SplitAfterSeq(d.Text, " ") {
				if err := sleep(ctx, p.word); err != nil {
					yield(agent.Delta{}, err)
					return
				}
				d.Text = w // the first piece keeps d's other fields
				if !yield(d, nil) {
					return
				}
				d = agent.Delta{}
			}
		}
	}
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("pause: %w", context.Cause(ctx))
	}
}
