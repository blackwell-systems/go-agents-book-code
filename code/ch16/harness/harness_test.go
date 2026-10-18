package harness

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

func TestRun(t *testing.T) {
	tests := []struct {
		script   string
		finishes bool // a diagnosis is submitted
		calls    int  // model calls, failed attempts included
		pauses   int
	}{
		{"diagnose", true, 3, 1},
		{"tool-error", true, 4, 1},
		{"retry", true, 4, 1},
	}
	for _, tt := range tests {
		t.Run(tt.script, func(t *testing.T) {
			c, w := task.Replay()
			env := &task.Env{}
			ctx := replay.WithScript(context.Background(), tt.script)
			out, err := Run(ctx, c, env)
			if got := env.Report().Diagnosis != nil; got != tt.finishes {
				t.Errorf("diagnosis submitted: %v, want %v (err %v)",
					got, tt.finishes, err)
			}
			switch {
			case tt.finishes && err != nil:
				t.Errorf("Run: %v", err)
			case !tt.finishes && err == nil:
				t.Error("Run returned no error; want the run to end with one")
			case !tt.finishes && tt.script == "tool-error" &&
				!errors.Is(err, task.ErrNoPackage):
				t.Errorf("Run: %v, want it to wrap ErrNoPackage", err)
			}
			if n := len(w.Bodies()); n != tt.calls {
				t.Errorf("%d model calls, want %d", n, tt.calls)
			}
			if out.Pauses != tt.pauses {
				t.Errorf("%d pauses, want %d", out.Pauses, tt.pauses)
			}
		})
	}
}

// TestDenied checks that an issue the person refuses isn't filed, and
// that the model is told so.
func TestDenied(t *testing.T) {
	c, w := task.Replay()
	env := &task.Env{Approve: func(task.IssueArgs) bool { return false }}
	ctx := replay.WithScript(context.Background(), "diagnose")
	if _, err := Run(ctx, c, env); err != nil {
		t.Logf("Run: %v (the script goes on as if approved)", err)
	}
	r := env.Report()
	if r.Asked != 1 || r.Issues != 0 {
		t.Errorf("asked %d, filed %d; want 1 and 0", r.Asked, r.Issues)
	}
	bodies := w.Bodies()
	if len(bodies) < 3 || !bytes.Contains(bodies[2], []byte(task.Denied)) {
		t.Errorf("the third request doesn't tell the model of the denial")
	}
}
