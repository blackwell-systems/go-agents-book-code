package agent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"iter"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestLimiter checks that calls sharing a Limiter start Every apart,
// and that a waiting call gives up when its context ends.
func TestLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &Limiter{Every: 100 * time.Millisecond}
		start := time.Now()
		var mu sync.Mutex
		var at []time.Duration
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if err := l.Wait(t.Context()); err != nil {
					t.Error(err)
				}
				mu.Lock()
				defer mu.Unlock()
				at = append(at, time.Since(start))
			})
		}
		wg.Wait()
		if last := at[len(at)-1]; last != 300*time.Millisecond {
			t.Errorf("fourth call started at %v, want 300ms (%v)", last, at)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		shutdown := errors.New("shutting down")
		cancel(shutdown)
		if err := l.Wait(ctx); !errors.Is(err, shutdown) {
			t.Errorf("Wait after cancel = %v", err)
		}
	})
}

// costly is a model that never finishes: it calls a tool on every turn,
// and each reply costs cost dollars.
type costly struct{ cost float64 }

func (c costly) Generate(_ context.Context, req Request) (Reply, error) {
	id := fmt.Sprint(len(req.Messages))
	return Reply{Message: Calls(Call(id, "look", `{}`)),
		Usage: Usage{Cost: c.cost}}, nil
}

// tag::test-spend[]

// TestSpendPool runs six agents on one pool: together they stop near
// the pool's limit, never more than one call each beyond it, and each
// stops with ErrBudgetExceeded.
func TestSpendPool(t *testing.T) {
	const agents, cost, limit = 6, 0.01, 0.50
	pool := &SpendPool{Limit: limit}
	look := Tool{Def: ToolDef{Name: "look"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "nothing", nil
		}}
	var wg sync.WaitGroup
	for range agents {
		wg.Go(func() {
			_, err := Run(t.Context(), costly{cost}, []Tool{look},
				[]Message{{Role: "user", Content: "go"}},
				Limits{MaxTurns: 1000, Spend: pool})
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Errorf("err = %v, want ErrBudgetExceeded", err)
			}
		})
	}
	wg.Wait()
	spent := pool.Spent()
	if spent < limit || spent > limit+agents*cost+1e-9 { // <1>
		t.Errorf("spent $%.2f; want $%.2f to $%.2f",
			spent, limit, limit+agents*cost)
	}
}

// end::test-spend[]

// counter is a run that reports n text events and then stops.
func counter(n int) func(context.Context) iter.Seq[Event] {
	return func(ctx context.Context) iter.Seq[Event] {
		return func(yield func(Event) bool) {
			for i := range n {
				if ctx.Err() != nil || !yield(Event{Kind: TextEvent,
					Text: fmt.Sprint(i)}) {
					return
				}
			}
			yield(Event{Kind: StoppedEvent})
		}
	}
}

// TestMerge checks that Merge tags every event with its run and ends
// after every run's StoppedEvent.
func TestMerge(t *testing.T) {
	got := map[string]int{}
	stops := 0
	for ev := range Merge(t.Context(), map[string]func(context.Context) iter.Seq[Event]{
		"fp_aaaaaa": counter(3), "fp_bbbbbb": counter(5),
	}) {
		got[ev.Agent]++
		if ev.Kind == StoppedEvent {
			stops++
		}
	}
	if got["fp_aaaaaa"] != 4 || got["fp_bbbbbb"] != 6 || stops != 2 {
		t.Errorf("events per agent %v, %d stops", got, stops)
	}
}

// tag::leaky[]

// leakyMerge is Merge without the cancel and the drain: after the
// caller stops reading, the runs' goroutines block forever on a send
// no one will receive.
func leakyMerge(runs []iter.Seq[Event]) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		events := make(chan Event)
		for _, run := range runs {
			go func() {
				for ev := range run {
					events <- ev
				}
			}()
		}
		for ev := range events {
			if !yield(ev) {
				return
			}
		}
	}
}

// end::leaky[]

// tag::test-leak[]

// TestMergeLeak reads one event from each fan-in and stops. The leak
// profile finds leakyMerge's blocked senders, and none from Merge.
func TestMergeLeak(t *testing.T) {
	ctx := t.Context()
	for range leakyMerge([]iter.Seq[Event]{counter(9)(ctx),
		counter(9)(ctx)}) {
		break
	}
	for range Merge(ctx,
		map[string]func(context.Context) iter.Seq[Event]{
			"a": counter(9), "b": counter(9)}) {
		break
	}
	// end::test-leak[]
	// tag::test-leak-profile[]
	var b bytes.Buffer
	for range 100 { // until both leaked senders have reached their send
		b.Reset()
		err := pprof.Lookup("goroutineleak").WriteTo(&b, 1)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(),
			"goroutineleak profile: total 2") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log(b.String())
	if !strings.Contains(b.String(), "leakyMerge") {
		t.Error("the leak profile didn't find leakyMerge's senders")
	}
	if strings.Contains(b.String(), "agent.Merge") {
		t.Error("Merge left a goroutine blocked")
	}
}

// end::test-leak-profile[]

// TestMergeCancel cancels the caller's context while every run is
// waiting: each run still reports its StoppedEvent, which a send that
// also gave up on the context would sometimes drop. It repeats, since
// the bug was a random choice between two ready cases.
func TestMergeCancel(t *testing.T) {
	waiter := func(ctx context.Context) iter.Seq[Event] {
		return func(yield func(Event) bool) {
			<-ctx.Done()
			yield(Event{Kind: StoppedEvent, Err: ctx.Err()})
		}
	}
	for range 50 {
		ctx, cancel := context.WithCancel(t.Context())
		runs := map[string]func(context.Context) iter.Seq[Event]{
			"a": waiter, "b": waiter, "c": waiter}
		cancel()
		stops := 0
		for ev := range Merge(ctx, runs) {
			if ev.Kind == StoppedEvent {
				stops++
			}
		}
		if stops != len(runs) {
			t.Fatalf("%d stop events for %d runs", stops, len(runs))
		}
	}
}

// pricey is a context policy whose every build costs cost, the way a
// compaction summary does.
type pricey struct{ cost float64 }

func (p pricey) Build(_ context.Context, msgs []Message, st ContextState) (Window, error) {
	return Window{Messages: msgs, State: st, Usage: Usage{Cost: p.cost}}, nil
}

// TestSpendAfterSummary checks that a costly build, such as a summary,
// is checked against the pool before the turn's model call, so a turn
// can't spend a summary and a reply past the limit.
func TestSpendAfterSummary(t *testing.T) {
	m := &counting{}
	pool := &SpendPool{Limit: 0.05}
	_, err := Run(t.Context(), m, nil, []Message{{Role: "user", Content: "go"}},
		Limits{MaxTurns: 5, Spend: pool, Context: pricey{0.10}})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if m.calls != 0 {
		t.Errorf("%d model calls after the summary spent the pool", m.calls)
	}
}

// counting is a model that counts its calls and answers each.
type counting struct{ calls int }

func (c *counting) Generate(context.Context, Request) (Reply, error) {
	c.calls++
	return Reply{Message: Says("done")}, nil
}
