package main

import (
	"fmt"
	"net/http"
)

// tag::attach[]

// attach is an http.RoundTripper that adds a run's credential to every
// request a tool makes. The harness builds the tool's client with it,
// so the token never passes through the model, the tool's arguments,
// or its results.
type attach struct {
	token string
	next  http.RoundTripper
}

func (a attach) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context()) // a RoundTripper must not change its input
	r.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.next.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	return resp, nil
}

// end::attach[]
