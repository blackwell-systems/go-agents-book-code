package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// sleeper is a read-only tool that takes d to answer with its name.
func sleeper(name string, d time.Duration) Tool {
	return Tool{Def: ToolDef{Name: name}, ReadOnly: true,
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			select {
			case <-time.After(d):
				return name + " done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}}
}

func collect(ctx context.Context, tools []Tool, calls []ToolCall) (
	[]Message, error) {
	_, byName := index(tools)
	answers, _, err := runTools(ctx, byName, calls, newJournal(nil, State{}),
		func(Message) bool { return true })
	return answers, err
}

// TestStages checks how a reply's calls are split: read-only calls
// together, anything else alone, in the model's order.
func TestStages(t *testing.T) {
	_, byName := index([]Tool{
		{Def: ToolDef{Name: "read"}, ReadOnly: true},
		{Def: ToolDef{Name: "edit"}},
	})
	calls := func(names ...string) []ToolCall {
		var out []ToolCall
		for i, n := range names {
			out = append(out, Call(fmt.Sprint(i), n, `{}`))
		}
		return out
	}
	for _, tc := range []struct {
		names []string
		want  [][]int
	}{
		{[]string{"read", "read", "read"}, [][]int{{0, 1, 2}}},
		{[]string{"edit", "read"}, [][]int{{0}, {1}}},
		{[]string{"read", "edit", "read", "read"}, [][]int{{0}, {1}, {2, 3}}},
		{[]string{"edit", "edit"}, [][]int{{0}, {1}}},
		{[]string{"read", "nope", "read"}, [][]int{{0}, {1}, {2}}},
	} {
		got := stages(byName, calls(tc.names...))
		if !slices.EqualFunc(got, tc.want, slices.Equal) {
			t.Errorf("stages(%v) = %v, want %v", tc.names, got, tc.want)
		}
	}
}

// tag::test-order[]

// TestRunnerOrder checks that read-only calls run at once and their
// answers come back in the model's order, whatever order they finish
// in. In a synctest bubble time is virtual, so the test is exact and
// takes no real time.
func TestRunnerOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tools := []Tool{sleeper("slow", 30*time.Millisecond),
			sleeper("fast", 10*time.Millisecond),
			sleeper("mid", 20*time.Millisecond)}
		calls := []ToolCall{Call("1", "slow", `{}`),
			Call("2", "fast", `{}`), Call("3", "mid", `{}`)}
		start := time.Now()
		answers, err := collect(t.Context(), tools, calls)
		if err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took != 30*time.Millisecond { // <1>
			t.Errorf("took %v, want 30ms: the calls didn't run at once", took)
		}
		for i, want := range []string{"slow done", "fast done", "mid done"} {
			if answers[i].Content != want || answers[i].ToolCallID != calls[i].ID {
				t.Errorf("answer %d = %+v, want %q", i, answers[i], want)
			}
		}
	})
}

// end::test-order[]

// TestBarrier replays a reply the default model sent in all 10 of its
// fix-task runs: an edit, then a test run in the same reply. The test
// run must see the edit, so the edit, which isn't read-only, runs
// alone first, even when it is slower.
func TestBarrier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// tag::barrier-tools[]
		var mu sync.Mutex
		code := "old"
		edit := Tool{Def: ToolDef{Name: "edit_file"},
			Run: func(context.Context, jsontext.Value) (string, error) {
				time.Sleep(50 * time.Millisecond) // writing takes a while
				mu.Lock()
				defer mu.Unlock()
				code = "new"
				return "edited", nil
			}}
		test := Tool{Def: ToolDef{Name: "run_tests"}, Idempotent: true,
			Run: func(context.Context, jsontext.Value) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				return "tested the " + code + " code", nil
			}}
		// end::barrier-tools[]
		// tag::barrier-check[]
		calls := []ToolCall{Call("1", "edit_file", `{}`),
			Call("2", "run_tests", `{}`)}
		_, byName := index([]Tool{edit, test})
		if got := allAtOnce(t.Context(), byName, calls); got[1] !=
			"tested the old code" {
			t.Errorf("all at once, run_tests answered %q", got[1])
		}
		code = "old"
		answers, err := collect(t.Context(), []Tool{edit, test}, calls)
		if err != nil {
			t.Fatal(err)
		}
		if got := answers[1].Content; got != "tested the new code" {
			t.Errorf("run_tests answered %q: it ran before the edit", got)
		}
		// end::barrier-check[]
	})
}

// allAtOnce runs every call at once, with no barriers: the mistake
// TestBarrier shows.
func allAtOnce(ctx context.Context, byName map[string]Tool,
	calls []ToolCall) []string {
	out := make([]string, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() {
			msg, _ := runTool(ctx, byName, c, newJournal(nil, State{})) // no tool here is fatal
			out[i] = msg.Content
		})
	}
	wg.Wait()
	return out
}

// TestRunnerPanic checks that a tool panicking in its goroutine becomes
// an error answer, and the calls beside it still answer.
func TestRunnerPanic(t *testing.T) {
	boom := Tool{Def: ToolDef{Name: "boom"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			panic("index out of range")
		}}
	answers, err := collect(t.Context(), []Tool{boom,
		sleeper("ok", time.Millisecond)},
		[]ToolCall{Call("1", "ok", `{}`), Call("2", "boom", `{}`),
			Call("3", "ok", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !answers[1].IsError || !strings.Contains(answers[1].Content, "internal error") {
		t.Errorf("panicking call answered %+v", answers[1])
	}
	if answers[0].Content != "ok done" || answers[2].Content != "ok done" {
		t.Errorf("the other calls answered %q and %q",
			answers[0].Content, answers[2].Content)
	}
}

// TestRunnerCancel cancels the run from inside a stage: the calls
// running beside the one that cancels stop, the calls of later stages
// are answered "cancelled" without running, and every call has an
// answer.
func TestRunnerCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		stopper := Tool{Def: ToolDef{Name: "stop"}, ReadOnly: true,
			Run: func(context.Context, jsontext.Value) (string, error) {
				cancel(errors.New("Ctrl-C"))
				return "stopped", nil
			}}
		edit := Tool{Def: ToolDef{Name: "edit"},
			Run: func(context.Context, jsontext.Value) (string, error) {
				t.Error("a call after the cancel ran")
				return "", nil
			}}
		calls := []ToolCall{Call("1", "read", `{}`), Call("2", "stop", `{}`),
			Call("3", "read", `{}`), Call("4", "edit", `{}`)}
		start := time.Now()
		answers, err := collect(ctx, []Tool{stopper, edit,
			sleeper("read", time.Hour)}, calls)
		if err != nil {
			t.Fatal(err)
		}
		if len(answers) != len(calls) {
			t.Fatalf("%d answers for %d calls", len(answers), len(calls))
		}
		if took := time.Since(start); took >= time.Hour {
			t.Errorf("took %v: the running reads didn't stop", took)
		}
		for _, i := range []int{0, 2} {
			if !answers[i].IsError {
				t.Errorf("running read %d answered %q", i, answers[i].Content)
			}
		}
		if !strings.Contains(answers[3].Content, "cancelled before it ran: Ctrl-C") {
			t.Errorf("the edit answered %q", answers[3].Content)
		}
	})
}

// TestToolFatal checks that a tool that can't run stops the run with
// ErrToolFatal, with every call of the reply answered, so the
// conversation is still one the API accepts.
func TestToolFatal(t *testing.T) {
	gone := Tool{Def: ToolDef{Name: "code_read"}, ReadOnly: true,
		Run: func(context.Context, jsontext.Value) (string, error) {
			return "", fmt.Errorf("%w: the workspace is gone", ErrToolFatal)
		}}
	edit := Tool{Def: ToolDef{Name: "code_patch"},
		Run: func(context.Context, jsontext.Value) (string, error) {
			t.Error("code_patch ran after a fatal error")
			return "", nil
		}}
	m := &Fake{Script: []Message{
		Calls(Call("1", "code_read", `{}`), Call("2", "code_patch", `{}`)),
		Says("never sent"),
	}}
	res, err := Run(t.Context(), m, []Tool{gone, edit},
		[]Message{{Role: "user", Content: "fix it"}}, Limits{MaxTurns: 5})
	if !errors.Is(err, ErrToolFatal) {
		t.Fatalf("err = %v, want ErrToolFatal", err)
	}
	last := res.Messages[len(res.Messages)-2:]
	// The fatal call says it stopped, since it may have acted (chapter
	// 15); the call after it never ran.
	for i, prefix := range []string{"stopped", "not run"} {
		m := last[i]
		if m.Role != "tool" || !m.IsError || !strings.HasPrefix(m.Content, prefix) {
			t.Errorf("answer %+v, want %q...", m, prefix)
		}
	}
}
