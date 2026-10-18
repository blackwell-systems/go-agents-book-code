package quota

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A Limiter rate-limits requests with one token bucket per key.
// It is safe for concurrent use.
type Limiter struct {
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time // last refill, and last use
}

// NewLimiter returns a Limiter that reads the time from now
// (time.Now if nil).
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, buckets: make(map[string]*bucket)}
}

// Allow takes a token from key's bucket, sized and refilled by plan p.
// When the bucket is empty it returns false and how long until a token
// is available.
func (l *Limiter) Allow(key string, p Plan) (ok bool, retryAfter time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: float64(p.Burst), last: now}
		l.buckets[key] = b
	}
	if now.After(b.last) {
		b.tokens += now.Sub(b.last).Seconds() * p.Rate
		b.last = now
	}
	b.tokens = min(b.tokens, float64(p.Burst))

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if p.Rate <= 0 {
		return false, 0
	}
	wait := (1 - b.tokens) / p.Rate * float64(time.Second)
	return false, time.Duration(math.Ceil(wait))
}

// Sweep removes buckets that have not been used for idle or longer and
// returns how many it removed.
func (l *Limiter) Sweep(idle time.Duration) int {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	n := 0
	for key, b := range l.buckets {
		if now.Sub(b.last) >= idle {
			delete(l.buckets, key)
			n++
		}
	}
	return n
}

// Middleware rate-limits requests to next. The key function names the
// caller and its plan. A request over the limit gets 429 Too Many
// Requests with a Retry-After header.
func Middleware(l *Limiter, key func(*http.Request) (string, Plan), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, p := key(r)
		ok, retry := l.Allow(k, p)
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		secs := retryAfterSeconds(retry)
		w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":       "rate limit exceeded",
			"retry_after": secs,
		})
	})
}

// retryAfterSeconds rounds d up to whole seconds, at least one.
func retryAfterSeconds(d time.Duration) int64 {
	secs := int64(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return max(secs, 1)
}
