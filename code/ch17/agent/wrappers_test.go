package agent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// attempts is a StreamModel that plays one script per call: deltas,
// then an error or the end. It counts the calls.
type attempts struct {
	scripts [][]Delta
	errs    []error
	calls   int
}

func (a *attempts) Stream(context.Context, Request) iter.Seq2[Delta, error] {
	i := a.calls
	a.calls++
	return func(yield func(Delta, error) bool) {
		for _, d := range a.scripts[i] {
			if !yield(d, nil) {
				return
			}
		}
		if a.errs[i] != nil {
			yield(Delta{}, a.errs[i])
		}
	}
}

var zero = Backoff{Rand: func() float64 { return 0 }}

// TestRetryingReset checks a stream that breaks after its first text:
// the retry sends a Reset before the new attempt's text, the reply is
// the second attempt's alone, and its usage includes the first's.
func TestRetryingReset(t *testing.T) {
	billed := Usage{PromptTokens: 100, Cost: 0.01}
	final := Usage{PromptTokens: 100, CompletionTokens: 5, Cost: 0.02}
	m := &attempts{
		scripts: [][]Delta{
			{{ID: "gen-1", Text: "Hel"}, {Usage: &billed}},
			{{ID: "gen-2", Text: "Hello"}, {FinishReason: "stop", Usage: &final}},
		},
		errs: []error{fmt.Errorf("read stream: %w", io.ErrUnexpectedEOF), nil},
	}
	var kinds []string
	var got Reply
	for ev := range Events(t.Context(), WrapModel(m, Retrying(Policy{Backoff: zero})),
		nil, []Message{{Role: "user", Content: "hi"}}, Limits{MaxTurns: 1}) {
		switch ev.Kind {
		case TextEvent:
			kinds = append(kinds, "text "+ev.Text)
		case ResetEvent:
			kinds = append(kinds, "reset")
		case StoppedEvent:
			if ev.Err != nil {
				t.Fatal(ev.Err)
			}
			got.Usage = ev.Result.Usage
			got.Message = ev.Result.Messages[len(ev.Result.Messages)-1]
		}
	}
	if want := "text Hel,reset,text Hello"; strings.Join(kinds, ",") != want {
		t.Errorf("events %v, want %s", kinds, want)
	}
	if got.Message.Content != "Hello" {
		t.Errorf("reply %q", got.Message.Content)
	}
	if want := billed.Add(final); got.Usage != want {
		t.Errorf("usage %+v, want %+v", got.Usage, want)
	}
}

// TestRetryingBadReplies checks which of the ErrBadReply family are
// retried: a stream that ended early is; a reply that was cut off,
// filtered, or refused is not.
func TestRetryingBadReplies(t *testing.T) {
	for _, tc := range []struct {
		err   error
		retry bool
	}{
		{fmt.Errorf("%w: %w", ErrIncompleteResponse, io.ErrUnexpectedEOF), true},
		{ErrOutputTruncated, false},
		{ErrOutputFiltered, false},
		{ErrRefused, false},
	} {
		if d := Classify(t.Context(), tc.err); d.Retry != tc.retry {
			t.Errorf("%v: retry %v, want %v", tc.err, d.Retry, tc.retry)
		}
	}
}

// TestGenerator checks that a compaction summary, a whole-reply call,
// gets the same retries through Generator.
func TestGenerator(t *testing.T) {
	ok := Usage{PromptTokens: 10}
	m := &attempts{
		scripts: [][]Delta{nil, {{Text: "summary"}, {Usage: &ok}}},
		errs:    []error{&net503{}, nil},
	}
	r, err := Generator(WrapModel(m, Retrying(Policy{Backoff: zero}))).
		Generate(t.Context(), Request{})
	if err != nil || r.Message.Content != "summary" || m.calls != 2 {
		t.Errorf("%q, %v after %d calls", r.Message.Content, err, m.calls)
	}
}

type net503 struct{}

func (*net503) Error() string   { return "503" }
func (*net503) Retryable() bool { return true }

// slowTool returns a tool that waits for its context, and counts runs.
func slowTool(name string, readOnly bool, runs *int) Tool {
	return Tool{Def: ToolDef{Name: name}, ReadOnly: readOnly,
		Timeout: time.Second,
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			*runs++
			<-ctx.Done()
			return "", fmt.Errorf("waited: %w", ctx.Err())
		}}
}

// TestTimeouts checks the two outcomes of a tool's timeout: a read-only
// tool's is an error result the model reads, retried first; a tool
// that may have changed something stops the run. A tool that ignores
// its context runs to the end, and its late result stands.
func TestTimeouts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runs := 0
		wrap := []ToolWare{RetryTools(Policy{Attempts: 2, Backoff: zero}), Timeouts}
		look := WrapTools([]Tool{slowTool("look", true, &runs)}, wrap...)[0]
		_, err := look.Run(t.Context(), nil)
		if !errors.Is(err, ErrToolTimeout) || !errors.Is(err, ErrTool) || runs != 2 {
			t.Errorf("read-only: %v after %d runs", err, runs)
		}

		runs = 0
		file := WrapTools([]Tool{slowTool("file", false, &runs)}, wrap...)[0]
		_, err = file.Run(t.Context(), nil)
		if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrTool) || runs != 1 {
			t.Errorf("side effects: %v after %d runs", err, runs)
		}

		deaf := Timeouts(Tool{Def: ToolDef{Name: "deaf"}, Timeout: time.Second,
			Run: func(context.Context, jsontext.Value) (string, error) {
				time.Sleep(3 * time.Second) // ignores its context
				return "done", nil
			}})
		start := time.Now()
		out, err := deaf.Run(t.Context(), nil)
		if out != "done" || err != nil || time.Since(start) != 3*time.Second {
			t.Errorf("ignores its context: %q, %v after %v", out, err, time.Since(start))
		}

		calls := []ToolCall{{ID: "1", Name: "file", Arguments: jsontext.Value("{}")}}
		model := &Fake{Script: []Message{{Role: "assistant", ToolCalls: calls}}}
		res, err := RunStream(t.Context(), Streamed(model), []Tool{file},
			[]Message{{Role: "user", Content: "file it"}}, Limits{MaxTurns: 3})
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("the run went on: %v", err)
		}
		if last := res.Messages[len(res.Messages)-1]; !strings.HasPrefix(last.Content, "outcome unknown") {
			t.Errorf("last message %q", last.Content)
		}
	})
}

// TestMeter checks that failed attempts are recorded and sub-agents
// roll up into their parent.
func TestMeter(t *testing.T) {
	var m Meter
	billed := Usage{PromptTokens: 100, Cost: 0.01}
	ok := Usage{PromptTokens: 120, Cost: 0.02}
	model := WrapModel(&attempts{
		scripts: [][]Delta{{{ID: "gen-1", Usage: &billed}},
			{{ID: "gen-2", Usage: &ok}}, {{Usage: &ok}}},
		errs: []error{&net503{}, nil, nil},
	}, Retrying(Policy{Backoff: zero}), m.Wrap)
	ctx := WithName(t.Context(), "fp_1")
	for range model.Stream(ctx, Request{}) {
	}
	for range model.Stream(WithName(ctx, "explain_package"), Request{}) {
	}
	calls := m.Calls()
	if len(calls) != 3 || !calls[0].Failed || calls[2].Agent != "fp_1/explain_package" {
		t.Fatalf("calls %+v", calls)
	}
	if calls[0].ID != "gen-1" || calls[1].ID != "gen-2" {
		t.Errorf("IDs %q, %q; want gen-1, gen-2", calls[0].ID, calls[1].ID)
	}
	var b bytes.Buffer
	if err := m.Report(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "fp_1   3      340") ||
		!strings.Contains(b.String(), "(failed gen-1)") {
		t.Errorf("report:\n%s", b.String())
	}
}

// TestTracedRun checks the span tree of a run with one tool call: the
// run, a chat span per call with an attempt inside, and the tool.
func TestTracedRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := NewRecorder()
		calls := []ToolCall{{ID: "c1", Name: "look", Arguments: jsontext.Value("{}")}}
		model := WrapModel(Streamed(&Fake{Script: []Message{
			{Role: "assistant", ToolCalls: calls},
			{Role: "assistant", Content: "done"}}}),
			Traced(rec, "openrouter", "m"), Retrying(Policy{}),
			TracedAttempts(rec))
		tools := WrapTools([]Tool{{Def: ToolDef{Name: "look"}, ReadOnly: true,
			Run: func(context.Context, jsontext.Value) (string, error) { return "ok", nil }}},
			TracedTools(rec))
		ctx, span := StartRun(t.Context(), rec, "triage", "openrouter")
		_, err := RunStream(ctx, model, tools, []Message{{Role: "user", Content: "go"}},
			Limits{MaxTurns: 3})
		span.Finish(err)
		var names []string
		for _, s := range rec.Spans() {
			names = append(names, fmt.Sprintf("%d:%s", s.Parent, s.Name))
		}
		want := "-1:invoke_agent triage,0:chat m,1:attempt,0:execute_tool look,0:chat m,4:attempt"
		if strings.Join(names, ",") != want {
			t.Errorf("spans %v\nwant %s", names, want)
		}
		for _, s := range rec.Spans() {
			if s.Name != "attempt" {
				continue
			}
			if len(s.Attrs) != 1 || s.Attrs[0].Key != "attempt" ||
				s.Attrs[0].Value.Int64() != 1 {
				t.Errorf("attempt span attributes %v; want attempt=1 only", s.Attrs)
			}
		}
	})
}

// TestLoggedToolsPanic checks that a tool's panic is logged with its
// stack, and still reaches the loop, which turns it into an error the
// model reads.
func TestLoggedToolsPanic(t *testing.T) {
	var b bytes.Buffer
	log := slog.New(slog.NewTextHandler(&b, nil))
	boom := WrapTools([]Tool{{Def: ToolDef{Name: "boom"},
		Run: func(context.Context, jsontext.Value) (string, error) { panic("kaboom") }}},
		LoggedTools(log, LogOptions{}))
	calls := []ToolCall{{ID: "1", Name: "boom", Arguments: jsontext.Value("{}")}}
	model := &Fake{Script: []Message{
		{Role: "assistant", ToolCalls: calls},
		{Role: "assistant", Content: "gave up"}}}
	res, err := RunStream(t.Context(), Streamed(model), boom,
		[]Message{{Role: "user", Content: "go"}}, Limits{MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Messages[2].Content, "internal error") {
		t.Errorf("tool message %q", res.Messages[2].Content)
	}
	if !strings.Contains(b.String(), "tool panicked") || !strings.Contains(b.String(), "goroutine") {
		t.Errorf("log:\n%s", b.String())
	}
}

// TestLimiterPause checks that a pause holds the next caller of Wait.
func TestLimiterPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &Limiter{Every: time.Millisecond}
		l.Pause(3 * time.Second)
		start := time.Now()
		if err := l.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 3*time.Second {
			t.Errorf("waited %v, want 3s", got)
		}
	})
	// Callers already waiting when the pause starts are held too.
	synctest.Test(t, func(t *testing.T) {
		l := &Limiter{Every: 100 * time.Millisecond}
		start := time.Now()
		var mu sync.Mutex
		var got []time.Duration
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				if err := l.Wait(t.Context()); err != nil {
					t.Error(err)
				}
				mu.Lock()
				got = append(got, time.Since(start))
				mu.Unlock()
			})
		}
		synctest.Wait() // one has gone; two wait for their turns
		l.Pause(time.Second)
		wg.Wait()
		slices.Sort(got) // the first went at once, before the pause
		if len(got) != 3 || got[0] != 0 || got[1] < time.Second {
			t.Errorf("callers went at %v; want 0s, then after 1s", got)
		}
	})
}

// TestGeneratorKeepsFailedUsage checks the compaction path: a call
// that fails on every attempt, each failure reporting its usage on the
// error's Delta, as a failed Anthropic stream does, still reports what
// was billed, and the last generation ID, through Generator and Collect.
func TestGeneratorKeepsFailedUsage(t *testing.T) {
	billed := Usage{PromptTokens: 100, Cost: 1}
	n := 0
	m := StreamFunc(func(context.Context, Request) iter.Seq2[Delta, error] {
		n++
		id := fmt.Sprintf("gen-%d", n)
		return func(yield func(Delta, error) bool) {
			yield(Delta{ID: id, Usage: &billed}, &net503{})
		}
	})
	g := Generator(Retrying(Policy{Attempts: 3, Backoff: zero})(m))
	reply, err := g.Generate(t.Context(), Request{})
	if err == nil {
		t.Fatal("no error")
	}
	if reply.Usage.PromptTokens != 300 || reply.Usage.Cost != 3 ||
		reply.ID != "gen-3" {
		t.Errorf("usage %+v, ID %q; want 300 tokens, $3, gen-3",
			reply.Usage, reply.ID)
	}
}

// TestTracedToolsPanic checks that a tool's panic finishes its span,
// with the panic as its error, and still reaches the loop.
func TestTracedToolsPanic(t *testing.T) {
	rec := NewRecorder()
	boom := WrapTools([]Tool{{Def: ToolDef{Name: "boom"},
		Run: func(context.Context, jsontext.Value) (string, error) { panic("kaboom") }}},
		TracedTools(rec))
	calls := []ToolCall{{ID: "1", Name: "boom", Arguments: jsontext.Value("{}")}}
	model := &Fake{Script: []Message{
		{Role: "assistant", ToolCalls: calls},
		{Role: "assistant", Content: "gave up"}}}
	if _, err := RunStream(t.Context(), Streamed(model), boom,
		[]Message{{Role: "user", Content: "go"}}, Limits{MaxTurns: 3}); err != nil {
		t.Fatal(err)
	}
	spans := rec.Spans()
	if len(spans) != 1 || spans[0].End.IsZero() || spans[0].Err == nil ||
		!strings.Contains(spans[0].Err.Error(), "kaboom") {
		t.Errorf("spans %+v; want one finished span with the panic", spans)
	}
}
