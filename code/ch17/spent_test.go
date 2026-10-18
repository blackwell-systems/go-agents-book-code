package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
)

// TestSpentSoFar: what a run spent counts its reviews and sub-agent
// calls, which its checkpoint's Usage doesn't, across two processes,
// plus its most expensive turn for a call in flight; a checkpoint that
// says more wins.
func TestSpentSoFar(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "r1.work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "spent")
	p := &printer{w: &bytes.Buffer{}}
	process := func(spend [][]float64) {
		pool := &agent.SpendPool{}
		events := func(yield func(agent.Event) bool) {
			for i, turn := range spend {
				for _, cost := range turn { // the model, a review, a sub-agent
					pool.Add(cost)
				}
				if !yield(agent.Event{Kind: agent.TurnEvent, Turn: i + 1}) {
					return
				}
			}
		}
		for range savingSpend(events, file, pool, p) {
		}
	}
	tr := &triager{dir: dir}
	if _, ok := tr.spentSoFar("r1"); ok {
		t.Fatal("a run with no record reported spending")
	}
	process([][]float64{{0.003}, {0.003, 0.002}, {0.001, 0.004}})
	process([][]float64{{0.001}}) // resumed after a crash
	got, ok := tr.spentSoFar("r1")
	if want := 0.014 + 0.005; !ok || math.Abs(got-want) > 1e-9 {
		t.Errorf("spent so far %v, %v; want %v", got, ok, want)
	}
	cp, err := durable.Open(dir, "r1")
	if err != nil {
		t.Fatal(err)
	}
	st := agent.State{Version: agent.StateVersion, RunID: "r1"}
	st.Usage.Cost = 0.05
	if err := cp.Save(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := tr.spentSoFar("r1"); math.Abs(got-(0.05+0.005)) > 1e-9 {
		t.Errorf("with a checkpoint at $0.05: %v", got)
	}
}
