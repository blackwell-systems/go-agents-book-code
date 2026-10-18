package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// DefaultMaxAttempts is how many attempts a Queue makes before giving up
// on a delivery, unless MaxAttempts says otherwise.
const DefaultMaxAttempts = 8

const (
	baseDelay = 30 * time.Second
	maxDelay  = time.Hour
)

// ErrUnknownDelivery means Record was given a delivery the queue does
// not hold.
var ErrUnknownDelivery = errors.New("webhook: unknown delivery")

// A Delivery is one event on its way to one URL.
type Delivery struct {
	ID         string
	URL        string
	Event      string
	Payload    []byte // JSON request body
	Attempts   int
	LastStatus int // HTTP status of the last attempt; 0 if no response arrived
	LastError  string
	Next       time.Time // when the next attempt is due
	Done       bool      // delivered, or given up on
	Created    time.Time
}

// A Queue holds deliveries and decides when each is next tried. The zero
// value is ready to use. Due and Add return copies; after each attempt,
// pass the copy back to Record.
type Queue struct {
	MaxAttempts int              // 0 means DefaultMaxAttempts
	Now         func() time.Time // nil means time.Now
	Rand        func() float64   // returns values in [0, 1); nil means math/rand/v2
	mu          sync.Mutex
	seq         int
	order       []*Delivery
	byID        map[string]*Delivery
}

// Add queues payload, encoded as JSON, for delivery to url as event. The
// delivery is due immediately.
func (q *Queue) Add(url, event string, payload any) (*Delivery, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webhook: encode %s payload: %w", event, err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.byID == nil {
		q.byID = make(map[string]*Delivery)
	}
	q.seq++
	now := q.now()
	d := &Delivery{
		ID:      fmt.Sprintf("whd_%06d", q.seq),
		URL:     url,
		Event:   event,
		Payload: b,
		Next:    now,
		Created: now,
	}
	q.order = append(q.order, d)
	q.byID[d.ID] = d
	c := *d
	return &c, nil
}

// Due returns copies of the deliveries that are not done and whose next
// attempt is at or before now, oldest first.
func (q *Queue) Due(now time.Time) []*Delivery {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []*Delivery
	for _, d := range q.order {
		if !d.Done && !d.Next.After(now) {
			c := *d
			out = append(out, &c)
		}
	}
	return out
}

// Record stores the outcome of an attempt at d, where err is what the
// attempt returned. A nil err or one wrapping ErrPermanent finishes the
// delivery; otherwise the next attempt is scheduled with exponential
// backoff and full jitter, until MaxAttempts is reached. Record updates
// d to match the queue's copy.
func (q *Queue) Record(d *Delivery, err error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.byID[d.ID]
	if !ok {
		return fmt.Errorf("webhook: record %s: %w", d.ID, ErrUnknownDelivery)
	}
	s.Attempts++
	s.LastStatus = d.LastStatus
	s.LastError = ""
	if err != nil {
		s.LastError = err.Error()
	}
	switch {
	case err == nil, errors.Is(err, ErrPermanent):
		s.Done = true
	case s.Attempts >= q.maxAttempts():
		s.Done = true
	default:
		s.Next = q.now().Add(q.backoff(s.Attempts))
	}
	*d = *s
	return nil
}

// Stats counts deliveries still pending, given up on, and delivered.
func (q *Queue) Stats() (pending, failed, done int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, d := range q.order {
		switch {
		case !d.Done:
			pending++
		case d.LastError != "":
			failed++
		default:
			done++
		}
	}
	return pending, failed, done
}

// backoff returns the wait before the attempt after the given one:
// a random duration up to baseDelay doubled per attempt, capped at
// maxDelay.
func (q *Queue) backoff(attempt int) time.Duration {
	d := baseDelay
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	d = min(d, maxDelay)
	return time.Duration(q.rand() * float64(d))
}

func (q *Queue) maxAttempts() int {
	if q.MaxAttempts > 0 {
		return q.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

func (q *Queue) rand() float64 {
	if q.Rand != nil {
		return q.Rand()
	}
	return rand.Float64()
}
