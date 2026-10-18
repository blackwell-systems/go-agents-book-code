package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// tag::scripted[]

// scripted is a Model that gives its replies in order, one per call:
// the same idea as chapter 1's Fake. Fake counts the assistant messages
// in the conversation to stay in step with the loop, but most calls
// here send one fresh message, so scripted counts calls instead. It is
// safe for concurrent use, and it keeps each request for tests to read.
type scripted struct {
	mu      sync.Mutex
	replies []agent.Reply
	reqs    []agent.Request
}

// Generate returns the next scripted reply.
func (s *scripted) Generate(_ context.Context,
	req agent.Request) (agent.Reply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if n := len(s.reqs); n > len(s.replies) {
		return agent.Reply{}, fmt.Errorf("reply %d: %w",
			n, agent.ErrScriptExhausted)
	}
	return s.replies[len(s.reqs)-1], nil
}

// end::scripted[]

// script returns a scripted model that answers with each text in turn.
func script(texts ...string) *scripted {
	s := &scripted{}
	for _, t := range texts {
		s.replies = append(s.replies, agent.Reply{
			Message: agent.Says(t), FinishReason: "stop"})
	}
	return s
}

// calls returns how many calls s has answered or refused.
func (s *scripted) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}
