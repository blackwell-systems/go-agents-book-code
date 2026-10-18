package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::late-issues[]

// lateIssues is the service's own use of the tracker: the fingerprint
// lookup, and comments for late reports, which run the agent's
// issue_comment through the same defenses: the egress check, then the
// approval gate with chapter 14's default policy, which asks a person
// about every comment.
type lateIssues struct {
	store  *tracker.Store
	getenv func(string) string
	dir    string
}

func newLateIssues(getenv func(string) string, state string,
	store *tracker.Store) (*lateIssues, error) {
	dir := filepath.Join(state, "late")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("late reports: %w", err)
	}
	return &lateIssues{store: store, getenv: getenv, dir: dir}, nil
}

// end::late-issues[]

// tag::late-tool[]

// tool builds issue_comment with its defenses for one comment: a gate
// of its own, so a denial of one comment's text is never taken as a
// denial of the next one's.
func (l *lateIssues) tool() (agent.Tool, error) {
	env, err := triage.NewEnv(l.dir, l.dir, nil, l.store)
	if err != nil {
		//nolint:wrapcheck // NewEnv says what failed
		return agent.Tool{}, err
	}
	env.Keys = true
	m := safety.NewMarker()
	secrets := append(secretValues(l.getenv), m.Boundary())
	eg := triage.NewEgress(secrets...)
	gate := &safety.Gate{Dir: l.dir}
	wares := append([]agent.ToolWare{eg.Outgoing(triage.Outgoing...),
		gate.Wrap}, triage.Defend(l.dir,
		safety.DefaultPolicy(triage.Protected), &m)...)
	return agent.WrapTools([]agent.Tool{triage.CommentTool(env)},
		wares...)[0], nil
}

// end::late-tool[]

// IsOpen reports whether an issue is open.
func (l *lateIssues) IsOpen(_ context.Context, id int) (bool, error) {
	is, err := l.store.Get(id)
	if err != nil {
		return false, nil //nolint:nilerr // no such issue: not open
	}
	return is.Status == "open", nil
}

// Open returns the open issue for a fingerprint.
func (l *lateIssues) Open(_ context.Context, fp string) (int, bool,
	error) {
	open := l.store.Search(tracker.Query{Fingerprint: fp,
		Status: "open"})
	if len(open) == 0 {
		return 0, false, nil
	}
	return open[0].ID, true, nil
}

// tag::late-comment[]

// Comment posts body on issue id with key, once ap approves; until
// then it returns the gate's *agent.ApprovalPending. A denial, or text
// the egress check refuses, is service.ErrDeclined.
func (l *lateIssues) Comment(ctx context.Context, ap agent.Approver,
	id int, key, body string) error {
	args, err := json.Marshal(map[string]any{"id": id, "body": body})
	if err != nil {
		return fmt.Errorf("late comment: %w", err)
	}
	call := agent.ToolCall{ID: key, Name: "issue_comment",
		Arguments: args}
	tool, err := l.tool()
	if err != nil {
		return err
	}
	ctx = agent.WithApprover(agent.WithCall(agent.WithKey(ctx,
		agent.Key("late", key)), call), ap)
	_, err = tool.Run(ctx, args)
	switch {
	case errors.Is(err, agent.ErrDenied),
		errors.Is(err, safety.ErrNotSent):
		return fmt.Errorf("%w: %w", service.ErrDeclined, err)
	case err != nil:
		return err //nolint:wrapcheck // a pause, or a failure to retry
	}
	return nil
}

// end::late-comment[]
