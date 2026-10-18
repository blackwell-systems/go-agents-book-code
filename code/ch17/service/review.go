package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// The kinds of review item, as the review command shows them.
const (
	KindApproval = "approval"        // may this call run?
	KindOutcome  = "unknown outcome" // did this call take effect?
	KindWaiting  = "waiting"         // nobody: something else settles it
)

// tag::item[]

// An Item is one question waiting for a person: a call that needs
// approval, or a call whose outcome is unknown. It carries what the
// agent's request carried, and the decision once there is one.
type Item struct {
	ID       string    `json:"id"`
	Run      string    `json:"run"`
	Kind     string    `json:"kind"`
	CallID   string    `json:"call_id"`
	Tool     string    `json:"tool"`
	Args     string    `json:"args"` // as the model wrote them
	Why      string    `json:"why"`
	Evidence string    `json:"evidence"` // raw: the diff, the report
	Targets  []string  `json:"targets,omitzero"`
	Hash     string    `json:"hash"` // what a decision applies to
	Added    time.Time `json:"added"`
	Deadline time.Time `json:"deadline,omitzero"` // zero: none

	Decision *Decision `json:"decision,omitzero"`

	// Closed says the run the item belonged to ended, so nobody needs
	// to answer it; Why says what ended it.
	Closed bool `json:"closed,omitzero"`

	// Stale flags an item with no deadline that has waited longer than
	// the service's StaleAfter. Nothing is decided for the person; the
	// flag only says someone should look. Not saved: computed when the
	// queue is listed.
	Stale bool `json:"stale,omitzero"`
}

// end::item[]

// A Decision is a person's answer to an Item, kept for the audit.
type Decision struct {
	Approved bool      `json:"approved"`
	By       string    `json:"by"`
	Reason   string    `json:"reason,omitzero"`
	At       time.Time `json:"at"`
}

// Errors the review queue returns to the review command.
var (
	ErrNoItem    = errors.New("service: no such review item")
	ErrDecided   = errors.New("service: item already decided")
	ErrNoReason  = errors.New("service: a denial needs a reason")
	ErrStaleItem = errors.New("service: the item's deadline passed")
	ErrClosed    = errors.New("service: the item's run has ended")
	ErrWrongKind = errors.New("service: that answer is for the other " +
		"kind of item: approve and deny an approval; rerun or done " +
		"for an unknown outcome")
)

// A reviewQueue holds the items in the state directory, one file each.
type reviewQueue struct {
	dir   string
	wait  time.Duration // how long an approval waits for a person
	stale time.Duration // when an item without a deadline is flagged
	now   func() time.Time
	busy  func(target string) bool // whether a waiting item still waits

	mu    sync.Mutex
	items map[string]*Item
}

func openReview(dir string, wait, stale time.Duration,
	now func() time.Time) (*reviewQueue, error) {
	items, err := readAll[Item](dir)
	if err != nil {
		return nil, fmt.Errorf("open review queue: %w", err)
	}
	q := &reviewQueue{dir: dir, wait: wait, stale: stale, now: now,
		items: map[string]*Item{}}
	for _, it := range items {
		q.items[it.ID] = &it
	}
	return q, nil
}

// close marks every undecided item of a run closed, as when the run
// ended or was restarted under another ID: they leave the review list
// and the stale count.
func (q *reviewQueue) close(run, why string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, it := range q.items {
		if it.Run != run || it.Decision != nil || it.Closed {
			continue
		}
		next := *it
		next.Closed = true
		next.Why += " (closed: " + why + ")"
		if err := writeJSON(q.file(id), &next); err != nil {
			continue // stays open; harmless, and shown
		}
		*it = next
	}
}

// itemID names the item for one request of one run. It covers the
// request's hash, so a request whose evidence changed is a new item,
// and a decision on the old one never applies to it.
func itemID(run string, r agent.ApprovalRequest) string {
	h := sha256.New()
	for _, s := range []string{run, r.Call.ID, r.Hash} {
		fmt.Fprintf(h, "%d:%s", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// tag::add[]

// add records what a paused run waits for. A request already queued
// keeps its first deadline; an unknown outcome has none (chapter 15).
func (q *reviewQueue) add(run string, p *agent.ApprovalPending) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range p.Requests {
		id := itemID(run, r)
		if _, ok := q.items[id]; ok {
			continue
		}
		it := &Item{ID: id, Run: run, Kind: KindApproval,
			CallID: r.Call.ID, Tool: r.Call.Name,
			Args: string(r.Call.Arguments), Why: r.Why,
			Evidence: r.Evidence, Targets: r.Targets, Hash: r.Hash,
			Added: q.now()}
		switch r.Kind {
		case agent.KindUnknownOutcome:
			it.Kind = KindOutcome
		case agent.KindWait: // chapter 17: asks nobody
			it.Kind = KindWaiting
		default:
			it.Deadline = it.Added.Add(q.wait)
		}
		if err := writeJSON(q.file(id), it); err != nil {
			return fmt.Errorf("add review item: %w", err)
		}
		q.items[id] = it
	}
	return nil
}

// end::add[]

// tag::decide[]

// decide records a person's decision on an item. A denial needs a
// reason: the model reads it, or for an outcome, it is the result.
func (q *reviewQueue) decide(id, kind string, d Decision) (*Item,
	error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	it, ok := q.items[id]
	switch {
	case !ok, it.Kind == KindWaiting:
		return nil, ErrNoItem
	case it.Closed:
		return nil, ErrClosed
	case it.Kind != kind:
		return nil, fmt.Errorf("%w: %s is an item of kind %q",
			ErrWrongKind, id, it.Kind)
	case it.Decision != nil:
		return nil, ErrDecided
	case !d.Approved && strings.TrimSpace(d.Reason) == "":
		return nil, ErrNoReason
	case expired(it, q.now()):
		return nil, ErrStaleItem
	}
	next := *it
	next.Decision = &d
	if err := writeJSON(q.file(id), &next); err != nil {
		return nil, fmt.Errorf("decide %s: %w", id, err)
	}
	*it = next
	return &next, nil
}

// end::decide[]

func expired(it *Item, now time.Time) bool {
	return !it.Deadline.IsZero() && !now.Before(it.Deadline)
}

// tag::approver[]

// approver returns the Approver a run resumes with: the decision a
// person made, a denial once an approval's deadline has passed, and
// otherwise agent.ErrNoDecision, which pauses the run again.
func (q *reviewQueue) approver(run string) agent.Approver {
	return agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		it, ok := q.items[itemID(run, r)]
		switch {
		case !ok:
			return agent.ApprovalDecision{}, agent.ErrNoDecision
		case it.Decision != nil:
			d := it.Decision
			return agent.ApprovalDecision{Approved: d.Approved,
				Hash: r.Hash, By: d.By, Reason: d.Reason}, nil
		case expired(it, q.now()):
			return agent.ApprovalDecision{Hash: r.Hash, By: "deadline",
				Reason: "no decision by " +
					it.Deadline.Format(time.RFC3339)}, nil
		}
		return agent.ApprovalDecision{}, agent.ErrNoDecision
	})
}

// end::approver[]

// settled reports whether every item of a run is decided or past its
// deadline, so resuming the run won't just pause it again.
func (q *reviewQueue) settled(run string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, it := range q.items {
		if it.Run != run || it.Decision != nil || it.Closed ||
			expired(it, now) {
			continue
		}
		if it.Kind != KindWaiting || q.stillWaits(it) {
			return false
		}
	}
	return true
}

// stillWaits reports whether what a waiting item waits for hasn't
// settled.
func (q *reviewQueue) stillWaits(it *Item) bool {
	return q.busy != nil && len(it.Targets) > 0 && q.busy(it.Targets[0])
}

// tag::stale[]

// isStale reports whether an item without a deadline has waited longer
// than the service's StaleAfter: an unknown outcome nobody has checked.
func (q *reviewQueue) isStale(it *Item, now time.Time) bool {
	return it.Deadline.IsZero() && it.Decision == nil &&
		now.Sub(it.Added) >= q.stale
}

// end::stale[]

// waiting returns how many of a run's items wait for a decision, and
// how many of those are stale.
func (q *reviewQueue) waiting(run string) (n, stale int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for _, it := range q.items {
		if it.Run == run && it.Decision == nil && !it.Closed &&
			!expired(it, now) && it.Kind != KindWaiting {
			n++
			if q.isStale(it, now) {
				stale++
			}
		}
	}
	return n, stale
}

// open returns the items waiting for a decision, oldest first, with
// expired approvals left out: their denial is already decided.
func (q *reviewQueue) open() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	var out []Item
	for _, it := range q.items {
		if it.Decision == nil && !it.Closed && !expired(it, now) &&
			it.Kind != KindWaiting {
			c := *it
			c.Stale = q.isStale(it, now)
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Item) int {
		return a.Added.Compare(b.Added)
	})
	return out
}

func (q *reviewQueue) file(id string) string {
	return filepath.Join(q.dir, id+".json")
}
