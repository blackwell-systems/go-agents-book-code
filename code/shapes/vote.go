package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

var (
	// ErrNoAgreement means a vote ended without any answer reaching its
	// quorum.
	ErrNoAgreement = errors.New("shapes: no answer reached the quorum")

	// errDecided is the cause given to the calls still running when a
	// vote is decided; they are cancelled, since their answers can't
	// change the result.
	errDecided = errors.New("shapes: the vote is decided")
)

// tag::vote[]

// Vote asks every voter the same question at once and returns the
// first answer that quorum of them give. Answers are compared as
// strings after trimming space, so ask for a word or a fixed label.
//
// Use a vote when one call's answer isn't reliable enough and a wrong
// answer costs more than several calls: a yes-or-no judgment, a label,
// a function name. Voters can be one model listed several times or
// different models. A vote costs one call per voter; the calls run at
// once, so it takes about as long as the slowest call it waits for.
// The voters must be able to disagree: one model at temperature 0
// answers the same every time, and the vote buys nothing.
func Vote(ctx context.Context, voters []agent.Model, prompt string,
	quorum int) (string, error) {
	if quorum < 1 || quorum > len(voters) {
		panic("shapes: Vote needs 1 <= quorum <= len(voters)")
	}
	var wg sync.WaitGroup
	defer wg.Wait() // <1>
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errDecided)                  // <2>
	results := make(chan result, len(voters)) // <3>
	for _, m := range voters {
		wg.Go(func() {
			answer, err := ask(ctx, m, prompt)
			results <- result{strings.TrimSpace(answer), err}
		})
	}
	return tally(results, len(voters), quorum)
}

// end::vote[]

// tag::tally[]

// A result is one voter's answer, or why it has none.
type result struct {
	answer string
	err    error
}

// tally reads n results and returns the first answer to reach quorum.
// A failed call counts as no vote; if no answer reaches the quorum, the
// error holds every failure.
func tally(results <-chan result, n, quorum int) (string, error) {
	votes := map[string]int{}
	var errs []error
	for range n {
		r := <-results
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		votes[r.answer]++
		if votes[r.answer] >= quorum {
			return r.answer, nil
		}
	}
	err := fmt.Errorf("%w: %d answers, %d failed calls",
		ErrNoAgreement, n-len(errs), len(errs))
	return "", errors.Join(err, errors.Join(errs...))
}

// end::tally[]
