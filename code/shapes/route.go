package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// ErrNoRoute means the classifier named a kind that has no handler, and
// the Router has no fallback.
var ErrNoRoute = errors.New("shapes: no handler for the kind")

// tag::router[]

// A Handler deals with one kind of input. It may call a large model, a
// small one, or no model at all.
type Handler func(ctx context.Context, input string) (string, error)

// A Router sends each input to the handler for its kind. A small, cheap
// model decides the kind, so the expensive handling runs only where it
// is needed.
//
// Use a router when inputs fall into kinds that need different
// treatment: different prompts, different models, or plain code. It
// adds one short call before every input, and a misclassified input
// gets the wrong handling, so measure the classifier on labeled inputs
// before trusting it. The input reaches the classifier's prompt, so a
// crafted input can pick its own kind; give no handler powers that the
// worst input shouldn't reach.
type Router struct {
	Classifier agent.Model        // a small, fast model is enough
	Handlers   map[string]Handler // by kind: one lowercase word each
	Fallback   Handler            // for any other reply; may be nil
}

// end::router[]

// tag::route[]

// Route asks the classifier for input's kind and runs that kind's
// handler. It returns the kind with the handler's output; the kind is
// "" when the fallback handled the input.
func (r Router) Route(ctx context.Context,
	input string) (kind, out string, err error) {
	kinds := slices.Sorted(maps.Keys(r.Handlers))
	reply, err := complete(ctx, r.Classifier, []agent.Message{
		{Role: "system", Content: fmt.Sprintf(classify,
			strings.Join(kinds, ", "))},
		{Role: "user", Content: input}, // <1>
	})
	if err != nil {
		return "", "", fmt.Errorf("classify: %w", err)
	}
	kind = strings.ToLower(strings.Trim(reply, " \n.\"'`"))
	h, ok := r.Handlers[kind]
	switch {
	case !ok && r.Fallback == nil:
		return "", "", fmt.Errorf("%w: %q", ErrNoRoute, reply)
	case !ok:
		kind, h = "", r.Fallback // <2>
	}
	if out, err = h(ctx, input); err != nil {
		return kind, "", fmt.Errorf("handle %q: %w", kind, err)
	}
	return kind, out, nil
}

// end::route[]

// classify is the classifier's system prompt; %s is the list of kinds.
const classify = "Classify the message that follows. " +
	"Reply with exactly one word, one of: %s. " +
	"If none fits, reply with the word other."
