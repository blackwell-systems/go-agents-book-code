package agent

import (
	"cmp"
	"context"
	"errors"
	"iter"
)

// tag::retrying[]

// Retrying returns a ModelWare that retries each call as p says. A
// call that fails after some of its reply went out starts again with a
// Delta whose Reset is set, so whoever is watching discards the part
// they saw. The usage of every failed attempt is added into the reply
// that finally succeeds, so the run's cost counts what was billed.
func Retrying(p Policy) ModelWare {
	return func(m StreamModel) StreamModel {
		return StreamFunc(func(ctx context.Context,
			req Request) iter.Seq2[Delta, error] {
			return func(yield func(Delta, error) bool) {
				r := retry{yield: yield}
				err := Do(ctx, p, func(ctx context.Context) error {
					return r.attempt(ctx, m, req)
				})
				if err != nil && !errors.Is(err, errStopped) {
					yield(Delta{Usage: &r.billed, ID: r.id}, err)
				}
			}
		})
	}
}

// end::retrying[]

// tag::retry-state[]

// A retry is the state of one call across its attempts.
type retry struct {
	yield  func(Delta, error) bool
	sent   bool   // a Delta from an earlier attempt went out
	billed Usage  // the usage of the attempts that failed
	id     string // the latest attempt's generation ID
}

// end::retry-state[]

// tag::retry-attempt[]

// attempt runs one attempt, passing its Deltas on.
func (r *retry) attempt(ctx context.Context, m StreamModel,
	req Request) error {
	first := true
	var used Usage // this attempt's usage, as the stream reported it
	for d, err := range m.Stream(ctx, req) {
		r.id = cmp.Or(d.ID, r.id)
		if d.Usage != nil {
			used = *d.Usage
		}
		if err != nil {
			r.billed = r.billed.Add(used) // <1>
			return err
		}
		if first && r.sent { // <2>
			if !r.yield(Delta{Reset: true}, nil) {
				return errStopped
			}
		}
		first = false
		if d.Usage != nil { // <3>
			u := d.Usage.Add(r.billed)
			d.Usage = &u
		}
		r.sent = true
		if !r.yield(d, nil) {
			return errStopped
		}
	}
	return nil
}

// end::retry-attempt[]
