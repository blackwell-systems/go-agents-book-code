package agent

import (
	"cmp"
	"container/heap"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// A request is one client's request, due at a time.
type request struct {
	at     time.Duration // since the start
	client int
	n      int // which retry it is, from 1; 0 for the first send
}

// queue orders requests by time, then by client, so the simulation
// gives the same answer on every run.
type queue []request

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	return cmp.Or(cmp.Compare(q[i].at, q[j].at),
		cmp.Compare(q[i].client, q[j].client)) < 0
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(x any)   { *q = append(*q, x.(request)) }
func (q *queue) Pop() any {
	old := *q
	r := old[len(old)-1]
	*q = old[:len(old)-1]
	return r
}

// contend simulates clients clients that all send at time zero to a
// provider that answers per requests in each window and refuses the
// rest; each refused client waits b(client).Delay(n) and sends again.
// It returns the requests sent and when the last client was answered.
// Brooker's experiment was a simulation too; this one uses the book's
// Backoff, and needs no goroutines, so it gives the same numbers on
// every run.
func contend(clients, per int, window time.Duration,
	b func(client int) Backoff) (int, time.Duration) {
	q := &queue{}
	for c := range clients {
		heap.Push(q, request{client: c})
	}
	backoffs := make([]Backoff, clients)
	for c := range clients {
		backoffs[c] = b(c)
	}
	requests, used, current := 0, 0, time.Duration(-1)
	var last time.Duration
	for q.Len() > 0 {
		r := heap.Pop(q).(request)
		requests++
		if w := r.at / window; w != current {
			current, used = w, 0
		}
		if used < per {
			used++
			last = r.at
			continue
		}
		next := request{client: r.client, n: r.n + 1}
		next.at = r.at + backoffs[r.client].Delay(next.n)
		heap.Push(q, next)
	}
	return requests, last
}

// TestContention is experiment E2 in notes/ch12-plan.md: 100 clients
// fail together against a provider that answers 10 requests per 100ms,
// and request. The jittered strategies run over 200 seeds; the table gives
// the mean and the range. Run with -v to see the numbers.
func TestContention(t *testing.T) {
	const clients, per, window, seeds = 100, 10, 100 * time.Millisecond, 200
	one := func() float64 { return 1 }
	fixed := func(int) func(int) Backoff {
		return func(int) Backoff {
			return Backoff{Base: 10 * time.Millisecond,
				Max: 10 * time.Millisecond, Rand: one}
		}
	}
	plain := func(base time.Duration) func(int) func(int) Backoff {
		return func(int) func(int) Backoff {
			return func(int) Backoff {
				return Backoff{Base: base, Max: 10 * time.Second, Rand: one}
			}
		}
	}
	jitter := func(base time.Duration) func(int) func(int) Backoff {
		return func(seed int) func(int) Backoff {
			return func(c int) Backoff {
				r := rand.New(rand.NewPCG(uint64(seed), uint64(c)))
				return Backoff{Base: base, Max: 10 * time.Second,
					Rand: r.Float64}
			}
		}
	}
	strategies := []struct {
		name string
		runs int
		b    func(seed int) func(client int) Backoff
	}{
		{"fixed 10ms", 1, fixed},
		{"backoff from 100ms, no jitter", 1, plain(window)},
		{"backoff from 100ms, full jitter", seeds, jitter(window)},
		{"backoff from 10ms, no jitter", 1, plain(10 * time.Millisecond)},
		{"backoff from 10ms, full jitter", seeds, jitter(10 * time.Millisecond)},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-33s %5s %-18s %s\n", "strategy", "runs",
		"requests mean (range)", "last answered mean (range)")
	for _, s := range strategies {
		var reqs []int
		var lasts []time.Duration
		for seed := 1; seed <= s.runs; seed++ {
			r, l := contend(clients, per, window, s.b(seed))
			reqs, lasts = append(reqs, r), append(lasts, l)
		}
		fmt.Fprintf(&b, "%-33s %5d %-18s %s\n", s.name, s.runs,
			spread(reqs, func(v float64) string { return fmt.Sprintf("%.0f", v) }),
			spread(lasts, func(v float64) string {
				return time.Duration(v).Round(10 * time.Millisecond).String()
			}))
	}
	t.Log("\n" + b.String())
	golden := filepath.Join("testdata", "contention.txt")
	if *update {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Errorf("results differ from %s; rerun with -update if the change is meant", golden)
	}
}

// spread formats values as "mean (min to max)", or the one value.
func spread[T int | time.Duration](vs []T, f func(float64) string) string {
	lo, hi := slices.Min(vs), slices.Max(vs)
	sum := 0.0
	for _, v := range vs {
		sum += float64(v)
	}
	if lo == hi {
		return f(float64(lo))
	}
	return fmt.Sprintf("%s (%s to %s)", f(sum/float64(len(vs))),
		f(float64(lo)), f(float64(hi)))
}
