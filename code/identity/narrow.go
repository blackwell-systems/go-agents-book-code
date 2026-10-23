package main

import (
	"fmt"
	"time"
)

// Grant is what a parent asks for when it starts a sub-agent.
type Grant struct {
	ID     string        // the child credential's ID
	Agent  string        // the sub-agent's name
	Scopes []string      // what the sub-agent may do
	TTL    time.Duration // how long it may do it
}

// tag::narrow[]

// Narrow derives a sub-agent's claims from its parent's: the same user
// and run, its own ID and agent name, only scopes the parent holds,
// and an expiry no later than the parent's. Only the harness, which
// holds the Issuer, can turn the result into a token.
func Narrow(parent Claims, g Grant, now time.Time) (Claims, error) {
	for _, s := range g.Scopes {
		if !parent.Has(s) {
			return Claims{}, fmt.Errorf("%w: parent lacks %s", ErrScope, s)
		}
	}
	child := parent
	child.ID, child.Agent, child.Scopes = g.ID, g.Agent, g.Scopes
	child.Expires = now.Add(g.TTL)
	if child.Expires.After(parent.Expires) {
		child.Expires = parent.Expires
	}
	return child, nil
}

// end::narrow[]
