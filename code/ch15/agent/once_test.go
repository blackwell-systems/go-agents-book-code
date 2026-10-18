package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	k := Key("run-1", "call_1")
	if k != Key("run-1", "call_1") {
		t.Error("Key is not stable")
	}
	if len(k) != 32 {
		t.Errorf("Key length %d, want 32", len(k))
	}
	for _, other := range [][2]string{
		{"run-2", "call_1"}, {"run-1", "call_2"},
		{"run-1c", "all_1"}, {"run-", "1call_1"},
	} {
		if Key(other[0], other[1]) == k {
			t.Errorf("Key%v = Key(run-1, call_1)", other)
		}
	}
	ctx := WithKey(t.Context(), k)
	if got, ok := KeyFrom(ctx); !ok || got != k {
		t.Errorf("KeyFrom = %q, %v", got, ok)
	}
	if _, ok := KeyFrom(t.Context()); ok {
		t.Error("KeyFrom found a key in a bare context")
	}
}

func TestResolve(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "issue_file",
		Arguments: jsontext.Value(`{"title":"Panic"}`)}
	a := Attempt{CallID: "c1", Tool: "issue_file", Key: Key("r", "c1")}
	plain := Tool{}
	found := func(context.Context, ToolCall, string) (string, bool, error) {
		return "filed #3", true, nil
	}
	missing := func(context.Context, ToolCall, string) (string, bool, error) {
		return "", false, nil
	}
	lagging := func(context.Context, ToolCall, string) (string, bool, error) {
		return "", false, errors.New("search index is behind")
	}
	person := func(approve bool) Approver {
		return ApproverFunc(func(_ context.Context,
			r ApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Approved: approve,
				Hash: r.Hash, By: "on-call", Reason: "it's #3"}, nil
		})
	}
	stale := ApproverFunc(func(context.Context,
		ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Approved: true, Hash: "old"}, nil
	})
	later := ApproverFunc(func(context.Context,
		ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{}, ErrNoDecision
	})
	tests := []struct {
		name    string
		tool    Tool
		look    func(context.Context, ToolCall, string) (string, bool, error)
		ap      Approver
		rung    Rung
		run     bool
		answer  string
		pending bool
	}{
		{"idempotent", Tool{Idempotent: true}, nil, nil, Retry,
			true, "", false},
		{"keyed, key sent", Tool{Keyed: true}, nil, nil, Retry,
			true, "", false},
		{"found", plain, found, nil, Checked, false, "filed #3", false},
		{"not found", plain, missing, nil, Checked, true, "", false},
		{"lagging, approved", plain, lagging, person(true), Asked,
			true, "", false},
		{"no lookup, denied", plain, nil, person(false), Asked, false,
			"not run again: on-call checked and said: it's #3", false},
		{"no approver", plain, nil, nil, 0, false, "", true},
		{"decision later", plain, nil, later, 0, false, "", true},
		{"stale decision", plain, nil, stale, 0, false, "", true},
	}
	for _, tt := range tests {
		tool := tt.tool
		tool.Lookup = tt.look
		res, err := Resolve(t.Context(), tool, "r", call, a, tt.ap)
		p, paused := errors.AsType[*ApprovalPending](err)
		switch {
		case paused != tt.pending || !paused && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case paused && p.Requests[0].Call.ID != "c1":
			t.Errorf("%s: pending %+v", tt.name, p.Requests)
		case res.Rung != tt.rung || res.Run != tt.run ||
			res.Answer.Content != tt.answer:
			t.Errorf("%s: %+v", tt.name, res)
		case !tt.run && tt.answer != "" && res.Answer.ToolCallID != "c1":
			t.Errorf("%s: answer for %q", tt.name, res.Answer.ToolCallID)
		}
	}
}

// TestResolveKeyNotSent checks that a keyed tool whose call went out
// without a key, as before keys were turned on, isn't retried: the
// other side never saw a key to recognize the repeat by.
func TestResolveKeyNotSent(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "issue_file"}
	_, err := Resolve(t.Context(), Tool{Keyed: true}, "r", call,
		Attempt{CallID: "c1"}, nil)
	if _, ok := errors.AsType[*ApprovalPending](err); !ok {
		t.Errorf("keyed tool, no key sent: %v, want a question", err)
	}
}

// TestResolveStableHash checks that a person's decision still matches
// when the lookup's error changes between restarts.
func TestResolveStableHash(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "issue_file"}
	a := Attempt{CallID: "c1", Key: Key("r", "c1")}
	n := 0
	tool := Tool{Lookup: func(context.Context, ToolCall, string) (string,
		bool, error) {
		n++
		return "", false, fmt.Errorf("search timed out (attempt %d)", n)
	}}
	var hashes []string
	for range 2 {
		_, err := Resolve(t.Context(), tool, "r", call, a, nil)
		p, ok := errors.AsType[*ApprovalPending](err)
		if !ok {
			t.Fatalf("lookup failed: %v", err)
		}
		hashes = append(hashes, p.Requests[0].Hash)
	}
	if hashes[0] != hashes[1] {
		t.Error("the request's hash changed with the lookup's error")
	}
}

// TestResolveKeyExpired checks that a keyed call whose key the other
// side may no longer hold goes to a person rather than being retried.
func TestResolveKeyExpired(t *testing.T) {
	call := ToolCall{ID: "c1", Name: "issue_file"}
	tool := Tool{Keyed: true, KeyTTL: 24 * time.Hour}
	for _, tt := range []struct {
		age   time.Duration
		retry bool
	}{{time.Hour, true}, {48 * time.Hour, false}} {
		a := Attempt{CallID: "c1", Key: Key("r", "c1"),
			Started: time.Now().Add(-tt.age)}
		res, err := Resolve(t.Context(), tool, "r", call, a, nil)
		_, asked := errors.AsType[*ApprovalPending](err)
		if res.Run != tt.retry || asked == tt.retry {
			t.Errorf("key %v old: run %v, asked %v", tt.age, res.Run,
				asked)
		}
	}
}
