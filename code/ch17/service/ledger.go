package service

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"time"
)

// tag::ledger[]

// A ledger is the spending, kept in the state directory so a restart
// doesn't reset it: chapter 9's SpendPool lives in memory. It holds
// two limits: the day's, which starts over at 00:00 UTC, and a total
// that never does, such as a whole batch's. A run reserves its budget
// when it starts, saved before the run does anything, and settles what
// it spent when it stops. A process killed mid-run leaves its holds in
// the file; the next process counts each as what the run saved it had
// spent, its reviews and sub-agents included, plus its costliest turn
// for the call in flight, but never more than the hold; with no
// record, the whole hold.
type ledger struct {
	file  string
	limit float64 // dollars a day; 0 means no limit
	total float64 // dollars in all; 0 means no limit
	now   func() time.Time

	Day   string             `json:"day"` // UTC, such as "2026-10-16"
	Spent float64            `json:"spent"`
	Total float64            `json:"total,omitzero"` // never reset
	Held  map[string]float64 `json:"held,omitzero"`  // by run
}

// end::ledger[]

// openLedger reads the ledger, counting what an earlier process held
// for runs it never settled as spent.
func openLedger(file string, limit, total float64, now func() time.Time,
	spent func(run string) (float64, bool)) (*ledger, error) {
	l := &ledger{file: file, limit: limit, total: total, now: now}
	err := readJSON(file, l)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if len(l.Held) > 0 {
		for run, held := range l.Held {
			if spent != nil {
				if cost, ok := spent(run); ok {
					held = min(held, cost)
				}
			}
			l.Spent += held
			l.Total += held
		}
		l.Held = nil
		if err := writeJSON(file, l); err != nil {
			return nil, fmt.Errorf("open ledger: %w", err)
		}
	}
	l.roll()
	return l, nil
}

// roll starts a new day's total when the date has changed. What runs
// hold stays held: they started yesterday and settle today.
func (l *ledger) roll() {
	if day := l.now().UTC().Format(time.DateOnly); day != l.Day {
		l.Day, l.Spent = day, 0
	}
}

func (l *ledger) held() float64 {
	sum := 0.0
	for _, h := range l.Held {
		sum += h
	}
	return sum
}

// tag::reserve[]

// reserve holds up to want dollars for run, or what the day has left
// if want is 0, saves the hold, and returns how much it got. False
// means the day's budget is spent and the run waits for tomorrow.
func (l *ledger) reserve(run string, want float64) (float64, bool,
	error) {
	l.roll()
	if left, limited := l.left(); limited {
		if want <= 0 || want > left {
			want = left
		}
		if want <= 0 {
			return 0, false, nil
		}
	}
	next := maps.Clone(l.Held)
	if next == nil {
		next = map[string]float64{}
	}
	next[run] = want
	saved := *l
	saved.Held = next
	if err := writeJSON(l.file, &saved); err != nil {
		return 0, false, err
	}
	l.Held = next
	return want, true, nil
}

// end::reserve[]

// settle replaces a run's hold with what it spent, saved before it
// returns.
func (l *ledger) settle(run string, spent float64) error {
	l.roll()
	delete(l.Held, run)
	l.Spent += spent
	l.Total += spent
	return writeJSON(l.file, l)
}

// canStart reports whether a run could reserve anything now.
func (l *ledger) canStart() bool {
	l.roll()
	left, limited := l.left()
	return !limited || left > 0
}

// left is what the tighter of the two limits leaves, and whether
// either limits anything.
func (l *ledger) left() (float64, bool) {
	left, limited := math.Inf(1), false
	if l.limit > 0 {
		left, limited = l.limit-l.Spent-l.held(), true
	}
	if l.total > 0 {
		left, limited = min(left, l.total-l.Total-l.held()), true
	}
	return left, limited
}
