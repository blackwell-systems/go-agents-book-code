package main

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// TestParseFaults checks each kind of -faults item, and that a bad one
// is an error rather than a request sent on.
func TestParseFaults(t *testing.T) {
	got, err := parseFaults("503:2, drop,lost,reset:40,truncate,ok")
	if err != nil {
		t.Fatal(err)
	}
	want := []agent.Fault{{Status: 503, RetryAfter: "2"}, {Drop: true},
		{Lost: true}, {Reset: 40}, {Truncate: 1}, {}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fault %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if fs, err := parseFaults(""); fs != nil || err != nil {
		t.Errorf(`"": %v, %v`, fs, err)
	}
	for _, bad := range []string{"200", "slow", "reset:x", "503,"} {
		if _, err := parseFaults(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// TestToolRetriesOwnLimits checks that a flaky tool's retries leave
// the model's retry budget and breaker alone.
func TestToolRetriesOwnLimits(t *testing.T) {
	w := newWrapped(config{backoff: agent.Backoff{Rand: func() float64 { return 0 }}},
		"openrouter", "m", &printer{w: io.Discard}, nil)
	flaky := agent.Tool{Def: agent.ToolDef{Name: "look"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "", syscall.ECONNRESET
		}}
	look := w.Tools([]agent.Tool{flaky})[0]
	for range 5 {
		if _, err := look.Run(t.Context(), nil); err == nil {
			t.Fatal("the flaky tool succeeded")
		}
	}
	if _, err := w.policy.Breaker.Allow(); err != nil {
		t.Errorf("the model's breaker: %v", err)
	}
	if !w.policy.RetryBudget.Allow() {
		t.Error("the model's retry budget ran out")
	}
}

// TestToolRetriesLeavePaceAlone checks that a tool's retry, even after
// an error that asks for a delay, doesn't pause the model's limiter,
// which only a provider's refusal should do.
func TestToolRetriesLeavePaceAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pace := &agent.Limiter{}
		w := newWrapped(config{}, "openrouter", "m",
			&printer{w: io.Discard}, pace)
		w.tools.OnRetry(1, errors.New("busy"),
			agent.Decision{Retry: true, After: time.Minute}, time.Minute)
		start := time.Now()
		if err := pace.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("the model's limiter waited %v after a tool retry", d)
		}
	})
}
