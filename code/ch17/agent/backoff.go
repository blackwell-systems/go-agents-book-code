package agent

import (
	"cmp"
	"math/rand/v2"
	"time"
)

// tag::backoff[]

// Backoff spaces out retries with "full jitter": the wait before retry
// n is a random time between zero and a ceiling that doubles with each
// retry, up to Max. Spreading the waits keeps many clients that failed
// together from all retrying together.
type Backoff struct {
	Base time.Duration // the first ceiling; 0 means one second
	Max  time.Duration // the largest ceiling; 0 means 30 seconds

	// Rand returns a number in [0, 1]; nil means math/rand/v2. Tests
	// set it so waits are exact.
	Rand func() float64
}

// Delay returns the wait before retry n, counting from 1.
func (b Backoff) Delay(n int) time.Duration {
	ceiling := cmp.Or(b.Base, time.Second)
	limit := cmp.Or(b.Max, 30*time.Second)
	for i := 1; i < n && ceiling < limit; i++ { // <1>
		ceiling *= 2
	}
	ceiling = min(ceiling, limit)
	r := rand.Float64
	if b.Rand != nil {
		r = b.Rand
	}
	return time.Duration(r() * float64(ceiling))
}

// end::backoff[]
