// Package store keeps users and reminders in memory and writes snapshots
// of them to disk.
package store

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"example.com/remind/schedule"
)

// Errors callers check for.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// maxOccurrences caps how many times Upcoming expands one recurring
// reminder, so a rule like "every 1m" can't produce millions of entries.
const maxOccurrences = 50

// A Reminder is one reminder. Recurring reminders have a Rule; one-off
// reminders have only Due.
type Reminder struct {
	ID       string
	UserID   string
	Text     string
	Rule     schedule.Rule
	Due      time.Time // the next time it fires
	Done     bool
	Priority int  // 1 (high) to 3 (low); 0 means normal
	Deleted  bool // kept as a tombstone so sync clients see the delete
	Created  time.Time
}

// An Occurrence is one future firing of a reminder.
type Occurrence struct {
	ReminderID string
	Text       string
	At         time.Time
}

// Store holds all users and reminders.
type Store struct {
	now func() time.Time

	mu        sync.RWMutex
	users     map[string]*User // by ID
	tokens    map[string]*User // by API token
	reminders map[string]*Reminder
	byUser    map[string][]*Reminder
	seq       int
	onChange  func(*Reminder)
}

// New returns an empty store. now is its clock.
func New(now func() time.Time) *Store {
	return &Store{
		now:       now,
		users:     make(map[string]*User),
		tokens:    make(map[string]*User),
		reminders: make(map[string]*Reminder),
		byUser:    make(map[string][]*Reminder),
	}
}

// OnChange registers a function called whenever a reminder changes.
func (s *Store) OnChange(f func(*Reminder)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = f
}

func (s *Store) changed(r *Reminder) {
	if s.onChange != nil {
		s.onChange(r)
	}
}

// Create adds a reminder for a user. For a recurring reminder, Due is
// computed from its rule.
func (s *Store) Create(userID, text string, due time.Time, rule schedule.Rule) *Reminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if rule != nil {
		due = rule.Next(now)
	}
	s.seq++
	r := &Reminder{
		ID:      fmt.Sprintf("rem_%04d", s.seq),
		UserID:  userID,
		Text:    text,
		Rule:    rule,
		Due:     due,
		Created: now,
	}
	s.reminders[r.ID] = r
	s.byUser[userID] = append(s.byUser[userID], r)
	s.changed(r)
	return r
}

// Get returns a reminder by ID. Deleted reminders are not returned.
func (s *Store) Get(id string) (*Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.reminders[id]
	var err error
	switch {
	case !ok:
		err = ErrNotFound
	case r.Deleted:
		r = nil
	}
	return r, err
}

// List returns a page of a user's reminders, oldest first.
func (s *Store) List(userID string, offset, limit int) []*Reminder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Reminder
	for _, r := range s.byUser[userID] {
		if !r.Deleted {
			out = append(out, r)
		}
	}
	end := min(offset+limit, len(out))
	return out[offset:end]
}

// Count returns how many reminders a user has, not counting deleted ones.
func (s *Store) Count(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, r := range s.byUser[userID] {
		if !r.Deleted {
			n++
		}
	}
	return n
}

// Update calls f with the reminder locked, then records the change.
func (s *Store) Update(id string, f func(*Reminder) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reminders[id]
	if !ok || r.Deleted {
		return ErrNotFound
	}
	if err := f(r); err != nil {
		return err
	}
	s.changed(r)
	return nil
}

// Delete marks a reminder deleted, keeping a tombstone.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reminders[id]
	if !ok || r.Deleted {
		return ErrNotFound
	}
	r.Deleted = true
	s.changed(r)
	return nil
}

// Due returns reminders that are due at or before now and not done.
func (s *Store) Due(now time.Time) []*Reminder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Reminder
	for _, r := range s.reminders {
		if !r.Done && !r.Deleted && !r.Due.After(now) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *Reminder) int { return a.Due.Compare(b.Due) })
	return out
}

// MarkSent records that a reminder fired: a recurring reminder moves to
// its next time, and a one-off reminder is done.
func (s *Store) MarkSent(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reminders[id]
	if !ok {
		return
	}
	if r.Rule != nil {
		r.Due = r.Rule.Next(r.Due)
	} else {
		r.Done = true
	}
}

// Upcoming returns the times a user's reminders fire in [from, to),
// sorted by time. Recurring reminders appear once per occurrence.
func (s *Store) Upcoming(userID string, from, to time.Time) []Occurrence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Occurrence
	for _, r := range s.byUser[userID] {
		if r.Done || r.Deleted {
			continue
		}
		if r.Rule == nil {
			if !r.Due.Before(from) && r.Due.Before(to) {
				out = append(out, Occurrence{ReminderID: r.ID, Text: r.Text, At: r.Due})
			}
			continue
		}
		t := r.Rule.Next(from)
		for n := 0; !t.IsZero() && t.Before(to) && n < maxOccurrences; n++ {
			out = append(out, Occurrence{ReminderID: r.ID, Text: r.Text, At: t})
			t = r.Rule.Next(t)
		}
	}
	slices.SortStableFunc(out, func(a, b Occurrence) int { return cmp.Compare(a.At.UnixNano(), b.At.UnixNano()) })
	return out
}

// Snapshot writes every user and reminder to w as JSON.
func (s *Store) Snapshot(w io.Writer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type reminder struct {
		*Reminder
		Rule string `json:",omitempty"`
	}
	snap := struct {
		Users     []*User
		Reminders []reminder
	}{}
	for _, u := range s.users {
		snap.Users = append(snap.Users, u)
	}
	slices.SortFunc(snap.Users, func(a, b *User) int { return cmp.Compare(a.ID, b.ID) })
	for _, r := range s.reminders {
		rr := reminder{Reminder: r}
		if r.Rule != nil {
			rr.Rule = r.Rule.String()
		}
		snap.Reminders = append(snap.Reminders, rr)
	}
	slices.SortFunc(snap.Reminders, func(a, b reminder) int { return cmp.Compare(a.ID, b.ID) })
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	return nil
}
