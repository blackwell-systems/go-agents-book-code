package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// ErrNotApproved means the critic still wanted changes when Revise ran
// out of revisions.
var ErrNotApproved = errors.New("shapes: draft not approved")

// tag::critic[]

// A Verdict is a critic's judgment of a draft.
type Verdict struct {
	Pass  bool   `json:"pass"`
	Notes string `json:"notes"` // what to change; empty on a pass
}

// A Critic judges a draft. It can be code (a length limit, a schema,
// the tests), a model, or code first and a model after it, so the cheap
// checks run before the expensive one.
type Critic func(ctx context.Context, draft string) (Verdict, error)

// AllOf returns a Critic that runs critics in order and returns the
// first verdict that fails. List the cheap ones first.
func AllOf(critics ...Critic) Critic {
	return func(ctx context.Context, draft string) (Verdict, error) {
		for _, c := range critics {
			v, err := c(ctx, draft)
			if err != nil || !v.Pass {
				return v, err
			}
		}
		return Verdict{Pass: true}, nil
	}
}

// end::critic[]

// tag::revise[]

// Revise asks writer for a draft of task, has critic judge it, and
// sends the critic's notes back to writer for a new draft, until the
// critic passes one or maxRevisions revisions have been made. It
// returns the last draft it has, even with an error.
//
// Use it when a check can tell a good result from a bad one and say
// what is wrong, and one more call is cheaper than a bad result. Each
// round costs a critic call and a writer call, and the writer's calls
// grow, since each one holds the whole conversation so far. The limit
// bounds both: at most 1+maxRevisions drafts and as many critiques.
func Revise(ctx context.Context, writer agent.Model, critic Critic,
	task string, maxRevisions int) (string, error) {
	msgs := []agent.Message{{Role: "user", Content: task}}
	draft, err := complete(ctx, writer, msgs)
	if err != nil {
		return "", fmt.Errorf("draft: %w", err)
	}
	// end::revise[]
	// tag::revise-loop[]
	for n := 0; ; n++ {
		v, err := critic(ctx, draft)
		switch {
		case err != nil:
			return draft, fmt.Errorf("critique: %w", err)
		case v.Pass:
			return draft, nil
		case n == maxRevisions: // <1>
			return draft, fmt.Errorf("%w after %d revisions: %s",
				ErrNotApproved, n, v.Notes)
		}
		msgs = append(msgs,
			agent.Message{Role: "assistant", Content: draft},
			agent.Message{Role: "user", Content: revise + v.Notes})
		next, err := complete(ctx, writer, msgs)
		if err != nil {
			return draft, fmt.Errorf("revise: %w", err)
		}
		draft = next
	}
}

// end::revise-loop[]

// revise introduces the critic's notes to the writer.
const revise = "A reviewer asked for changes. Reply with the whole " +
	"revised text and nothing else. The reviewer's notes:\n"

// tag::model-critic[]

// ModelCritic returns a Critic that asks m to judge each draft against
// rubric, replying with a Verdict as JSON.
func ModelCritic(m agent.Model, rubric string) Critic {
	return func(ctx context.Context, draft string) (Verdict, error) {
		reply, err := ask(ctx, m, fmt.Sprintf(judge, rubric, draft))
		if err != nil {
			return Verdict{}, err
		}
		var v Verdict
		err = json.Unmarshal([]byte(unfence(reply)), &v)
		if err != nil {
			return Verdict{}, fmt.Errorf("read verdict: %w", err)
		}
		return v, nil
	}
}

// end::model-critic[]

// judge is the critic's prompt; the first %s is the rubric, the second
// the draft.
const judge = "Judge the draft below against this rubric:\n%s\n\n" +
	`Reply with JSON only: {"pass": true} if it meets every point, ` +
	`or {"pass": false, "notes": "..."} saying what to change.` +
	"\n\nThe draft:\n%s"

// unfence removes the Markdown code fence that models often put around
// JSON, if there is one.
func unfence(s string) string {
	s = strings.TrimSpace(s)
	s, ok := strings.CutPrefix(s, "```")
	if !ok {
		return s
	}
	s = strings.TrimPrefix(s, "json")
	return strings.TrimSpace(strings.TrimSuffix(s, "```"))
}
