// Package audit records administrator and sharing actions so they can be
// reviewed later.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"
)

// An Entry is one recorded action.
type Entry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`  // user ID of whoever acted
	Action string    `json:"action"` // such as "user.delete" or "list.share"
	Target string    `json:"target"` // ID of what was acted on
	Detail string    `json:"detail,omitempty"`
}

// A Filter selects entries for Query. Zero fields match every entry.
type Filter struct {
	Actor  string
	Action string
	Since  time.Time // entries at or after this time
	Limit  int       // at most this many entries; 0 means no limit
}

func (f Filter) match(e Entry) bool {
	switch {
	case f.Actor != "" && e.Actor != f.Actor:
		return false
	case f.Action != "" && e.Action != f.Action:
		return false
	case !f.Since.IsZero() && e.Time.Before(f.Since):
		return false
	}
	return true
}

// Log keeps the most recent entries in a ring of fixed size, dropping
// the oldest entry when it is full. It is safe for concurrent use.
type Log struct {
	now func() time.Time

	mu   sync.Mutex
	ring []Entry
	next int // index the next entry is written to
	full bool
}

// New returns a log that keeps the last size entries. now is its clock.
// New panics if size is less than 1.
func New(size int, now func() time.Time) *Log {
	if size < 1 {
		panic(fmt.Sprintf("audit: log size %d is less than 1", size))
	}
	return &Log{now: now, ring: make([]Entry, size)}
}

// Record adds an entry, stamped with the current time.
func (l *Log) Record(actor, action, target, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring[l.next] = Entry{
		Time:   l.now(),
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
	}
	l.next = (l.next + 1) % len(l.ring)
	if l.next == 0 {
		l.full = true
	}
}

// oldestFirst returns a copy of the kept entries, oldest first. l.mu
// must be held.
func (l *Log) oldestFirst() []Entry {
	if !l.full {
		return slices.Clone(l.ring[:l.next])
	}
	return slices.Concat(l.ring[l.next:], l.ring[:l.next])
}

// Query returns the entries that match f, newest first.
func (l *Log) Query(f Filter) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.ring)
	}
	var out []Entry
	for i := range n {
		e := l.ring[(l.next-1-i+len(l.ring))%len(l.ring)]
		if !f.match(e) {
			continue
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out
}

// WriteJSON writes every kept entry to w as one JSON object per line,
// oldest first.
func (l *Log) WriteJSON(w io.Writer) error {
	l.mu.Lock()
	entries := l.oldestFirst()
	l.mu.Unlock()
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("audit: write entry: %w", err)
		}
	}
	return nil
}
