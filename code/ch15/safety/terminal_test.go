package safety

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestLineApprover checks the terminal approver: the right code
// approves, a late yes meant for an earlier request denies, a run that
// ends while waiting returns at once, and Close ends the reading
// goroutine even while a read is blocked.
func TestLineApprover(t *testing.T) {
	in, typed := io.Pipe()
	var out strings.Builder
	a := &LineApprover{In: in, Out: &out}
	r := patch("auth/auth.go", "x")
	r.Hash = "abc123def456"

	go func() { _, _ = io.WriteString(typed, "yes abc123\n") }()
	d, err := a.Decide(t.Context(), r)
	if err != nil || !d.Approved {
		t.Fatalf("right code: %+v, %v", d, err)
	}

	r2 := r
	r2.Hash = "fff000aaa111"
	go func() { _, _ = io.WriteString(typed, "yes abc123\n") }() // typed for the first
	d, err = a.Decide(t.Context(), r2)
	if err != nil || d.Approved {
		t.Fatalf("stale yes: %+v, %v", d, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.Decide(ctx, r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no answer: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-a.lines:
		if ok {
			t.Error("a line arrived after Close")
		}
	case <-time.After(2 * time.Second):
		t.Error("the reading goroutine didn't end")
	}
	if !strings.Contains(out.String(), `type "yes abc123"`) {
		t.Errorf("prompt:\n%s", out.String())
	}
}
