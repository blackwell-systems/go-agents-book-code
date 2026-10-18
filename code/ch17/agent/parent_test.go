package agent

import (
	"errors"
	"testing"
)

// TestSpendParent: a run's pool refuses once its parent, the whole
// batch's, has reached its limit, though the run's own has room; and
// what a child spends counts in the parent.
func TestSpendParent(t *testing.T) {
	batch := &SpendPool{Limit: 0.02}
	a := &SpendPool{Limit: 1, Parent: batch}
	b := &SpendPool{Limit: 1, Parent: batch}
	a.Add(0.01)
	if err := b.check(); err != nil {
		t.Fatalf("under the batch's limit: %v", err)
	}
	b.Add(0.01)
	if err := a.check(); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("a over the batch's limit: %v", err)
	}
	if a.Spent() != 0.01 || batch.Spent() != 0.02 {
		t.Errorf("a spent %v, the batch %v", a.Spent(), batch.Spent())
	}
}
