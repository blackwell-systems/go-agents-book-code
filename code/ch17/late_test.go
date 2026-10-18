package main

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// TestLateIssues: a late-report comment goes through the egress check
// and the approval gate: it waits for a decision, is posted once on
// approval, and text that would leak a key is refused before anyone
// is asked.
func TestLateIssues(t *testing.T) {
	state := t.TempDir()
	store, err := tracker.Open(filepath.Join(state, "issues.json"),
		taskrun.Now)
	if err != nil {
		t.Fatal(err)
	}
	is, err := store.Create("panic", "body", nil, "fp_aaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	key := "sk-or-v1-0123456789abcdef0123456789"
	getenv := func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			return key
		}
		return ""
	}
	l, err := newLateIssues(getenv, state, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if id, ok, _ := l.Open(ctx, "fp_aaaaaa"); !ok || id != is.ID {
		t.Fatalf("Open = %d, %v", id, ok)
	}

	body := "Seen 2 more times:\n- report cr_1\n- report cr_2\n"
	err = l.Comment(ctx, nil, is.ID, "run-1/late/0", body)
	pending, ok := errors.AsType[*agent.ApprovalPending](err)
	if !ok || len(pending.Requests) != 1 {
		t.Fatalf("Comment with no decision: %v, want a pause", err)
	}
	approve := agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return agent.ApprovalDecision{Approved: true, Hash: r.Hash,
			By: "ana"}, nil
	})
	for range 2 { // the second is a retry with the same key
		if err := l.Comment(ctx, approve, is.ID, "run-1/late/0",
			body); err != nil {
			t.Fatalf("approved comment: %v", err)
		}
	}
	got, _ := store.Get(is.ID)
	if len(got.Comments) != 1 ||
		!strings.Contains(got.Comments[0].Body, "Seen 2 more times") {
		t.Errorf("comments %+v, want one", got.Comments)
	}

	err = l.Comment(ctx, approve, is.ID, "run-1/late/1",
		"crash with key "+key)
	if err == nil {
		t.Error("a comment holding the API key went out")
	}
}

// TestEgressSecrets: no secret the service holds can leave in a
// comment: the model keys, the ingest secret, each reviewer's token,
// and the audit key.
func TestEgressSecrets(t *testing.T) {
	env := map[string]string{
		"OPENROUTER_API_KEY":   "sk-or-v1-0123456789abcdef0123456789",
		"TRIAGE_INGEST_SECRET": "ingest-secret-0123456789abcdef0123",
		"TRIAGE_REVIEWERS": "ana review-token-ana-0123456789abcdef0\n" +
			"bo review-token-bo-0123456789abcdef012\n",
		"AUDIT_KEY": "audit-key-0123456789abcdef0123456789",
	}
	getenv := func(k string) string { return env[k] }
	state := t.TempDir()
	store, err := tracker.Open(filepath.Join(state, "issues.json"),
		taskrun.Now)
	if err != nil {
		t.Fatal(err)
	}
	is, _ := store.Create("panic", "body", nil, "fp_aaaaaa")
	l, err := newLateIssues(getenv, state, store)
	if err != nil {
		t.Fatal(err)
	}
	approve := agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		return agent.ApprovalDecision{Approved: true, Hash: r.Hash}, nil
	})
	for i, secret := range []string{env["OPENROUTER_API_KEY"],
		env["TRIAGE_INGEST_SECRET"], "review-token-ana-0123456789abcdef0",
		"review-token-bo-0123456789abcdef012", env["AUDIT_KEY"]} {
		err := l.Comment(t.Context(), approve, is.ID,
			"run-1/late/"+strconv.Itoa(i), "seen again: "+secret)
		if !errors.Is(err, service.ErrDeclined) {
			t.Errorf("%.12s...: %v, want a refusal", secret, err)
		}
	}
	got, _ := store.Get(is.ID)
	if len(got.Comments) != 0 {
		t.Errorf("%d comments went out", len(got.Comments))
	}
}
