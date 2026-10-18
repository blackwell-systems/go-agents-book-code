package agent

import (
	"cmp"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrOpen is what a call gets while a Breaker is open: the provider
// has failed too often in a row, and calling it again now would only
// add load.
var ErrOpen = errors.New("reliability: circuit open")

// An OpenError is ErrOpen with how long the breaker stays open.
type OpenError struct{ For time.Duration }

func (e *OpenError) Error() string {
	return fmt.Sprintf("%v for %v more", ErrOpen, e.For.Round(time.Second))
}

// Is makes errors.Is(err, ErrOpen) true for an *OpenError.
func (e *OpenError) Is(target error) bool { return target == ErrOpen }

// tag::breaker[]

// A Breaker stops calls to a provider that keeps failing. After
// Threshold failures worth retrying in a row it opens, and every call
// fails at once with an *OpenError for Cooldown. Then it lets one call
// through as a probe: a success closes it, a failure opens it again.
type Breaker struct {
	Threshold int           // failures in a row that open it; 0 means 5
	Cooldown  time.Duration // how long it stays open; 0 means 30s

	mu       sync.Mutex
	failures int
	until    time.Time // open until then; zero when closed
	probing  bool      // a probe call is out
}

// end::breaker[]

// tag::breaker-allow[]

// Allow returns nil if a call may go out now, and whether it is the
// probe, whose caller must end it with Record or Release. A nil Breaker
// allows every call.
func (b *Breaker) Allow() (probe bool, err error) {
	if b == nil {
		return false, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch now := time.Now(); {
	case b.until.IsZero():
		return false, nil
	case now.Before(b.until):
		return false, &OpenError{For: b.until.Sub(now)}
	case b.probing:
		return false, &OpenError{For: b.cooldown()}
	default: // the cooldown is over: this call is the probe
		b.probing = true
		return true, nil
	}
}

// end::breaker-allow[]

// tag::breaker-record[]

// Record counts one call's outcome: ok for an answer, even a refusal,
// !ok for a failure worth retrying.
func (b *Breaker) Record(ok bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if ok {
		b.failures, b.until = 0, time.Time{}
		return
	}
	b.failures++
	if b.failures >= cmp.Or(b.Threshold, 5) || !b.until.IsZero() {
		b.until = time.Now().Add(b.cooldown())
	}
}

// Release ends a probe that learned nothing, one the caller cancelled
// or whose call panicked, so the next call can probe instead.
func (b *Breaker) Release(probe bool) {
	if b == nil || !probe {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

// end::breaker-record[]

func (b *Breaker) cooldown() time.Duration {
	return cmp.Or(b.Cooldown, 30*time.Second)
}
