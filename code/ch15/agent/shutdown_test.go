package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// TestGrace checks that a side-effecting call outlives a shutdown by
// the grace period, and no longer, and that any other cancellation
// reaches it at once.
func TestGrace(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		takes time.Duration // how long the tool needs
		ran   bool          // whether it finished
		after time.Duration // when it returned, after the stop
	}{
		{"shutdown, quick tool", ErrShutdown, 2 * time.Second, true,
			2 * time.Second},
		{"shutdown, slow tool", ErrShutdown, time.Minute, false,
			5 * time.Second},
		{"other cause", ErrToolTimeout, 2 * time.Second, false, 0},
	}
	for _, tt := range tests {
		synctest.Test(t, func(t *testing.T) {
			tool := Grace(5 * time.Second)(Tool{
				Run: func(ctx context.Context, _ jsontext.Value) (string,
					error) {
					select {
					case <-time.After(tt.takes):
						return "filed", nil
					case <-ctx.Done():
						return "", context.Cause(ctx)
					}
				}})
			ctx, stop := context.WithCancelCause(t.Context())
			go func() { stop(tt.cause) }()
			start := time.Now()
			out, err := tool.Run(ctx, nil)
			if (out == "filed") != tt.ran ||
				time.Since(start) != tt.after {
				t.Errorf("%s: %q, %v after %v", tt.name, out, err,
					time.Since(start))
			}
			if !tt.ran && !errors.Is(err, tt.cause) {
				t.Errorf("%s: %v, want %v", tt.name, err, tt.cause)
			}
		})
	}
	read := Grace(time.Second)(Tool{ReadOnly: true, Run: func(
		context.Context, jsontext.Value) (string, error) {
		return "", nil
	}})
	if read.Run == nil {
		t.Fatal("read-only tool lost its Run")
	}
}
