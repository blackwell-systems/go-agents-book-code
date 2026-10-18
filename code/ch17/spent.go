package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// A spendRecord is what a group's run has spent, saved in its work
// directory after every turn. The checkpoint's Usage counts the agent's
// own model calls only; the run's pool also has its reviews and
// explain_package's sub-agent, so after a crash the ledger counts this.
type spendRecord struct {
	Spent float64 `json:"spent"` // in all, across every process
	Turn  float64 `json:"turn"`  // the most one turn has cost
}

// savingSpend passes events on and saves what pool has spent, added to
// what earlier processes saved, after every turn and when the events
// end, so a process killed mid-run leaves the record behind.
func savingSpend(events iter.Seq[agent.Event], file string,
	pool *agent.SpendPool, p *printer) iter.Seq[agent.Event] {
	return func(yield func(agent.Event) bool) {
		prior, err := readSpend(file)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			p.printf("%v; counting from zero\n", err)
		}
		last, turn := 0.0, prior.Turn
		save := func() {
			now := pool.Spent()
			turn, last = max(turn, now-last), now
			err := writeSpend(file, spendRecord{prior.Spent + now, turn})
			if err != nil {
				p.printf("save spending: %v\n", err)
			}
		}
		defer save()
		for ev := range events {
			if !yield(ev) {
				return
			}
			if ev.Kind == agent.TurnEvent {
				save()
			}
		}
	}
}

func readSpend(file string) (spendRecord, error) {
	var s spendRecord
	b, err := os.ReadFile(file)
	if err != nil { // fs.ErrNotExist: nothing saved yet
		return s, fmt.Errorf("read spending: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("read spending: %w", err)
	}
	return s, nil
}

// writeSpend replaces file atomically: a temporary file, synced and
// renamed, so a crash leaves the old record or the new one.
func writeSpend(file string, s spendRecord) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("save spending: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".spent-*.tmp")
	if err != nil {
		return fmt.Errorf("save spending: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		return fmt.Errorf("save spending: %w", err)
	}
	return nil
}
