package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// conversation builds a system prompt, a task, and n turns, each an
// assistant message with two tool calls and their results, the first
// result long.
func conversation(n int) []Message {
	msgs := []Message{{Role: "system", Content: "You triage crashes."},
		{Role: "user", Content: "Triage cr_000001."}}
	for i := range n {
		a, b := fmt.Sprintf("c%da", i), fmt.Sprintf("c%db", i)
		msgs = append(msgs,
			Message{Role: "assistant", Content: fmt.Sprintf("turn %d", i),
				ToolCalls: []ToolCall{
					{ID: a, Name: "code_read", Arguments: jsontext.Value(`{"path":"store/store.go"}`)},
					{ID: b, Name: "crash_read", Arguments: jsontext.Value(`{"id":"cr_000001"}`)}},
				ReasoningDetails: jsontext.Value(`[{"type":"reasoning.text","text":"x"}]`)},
			Message{Role: "tool", ToolCallID: a, Content: strings.Repeat("line of code\n", 100)},
			Message{Role: "tool", ToolCallID: b, Content: "short"})
	}
	return msgs
}

// checkWhole fails if a request breaks a turn apart: every tool call
// must be answered, and every tool message must follow its call.
func checkWhole(t *testing.T, msgs []Message) {
	t.Helper()
	open := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			if !open[m.ToolCallID] {
				t.Fatalf("tool message %s has no call before it", m.ToolCallID)
			}
			delete(open, m.ToolCallID)
			continue
		}
		if len(open) > 0 {
			t.Fatalf("calls %v have no results", open)
		}
		for _, c := range m.ToolCalls {
			open[c.ID] = true
		}
	}
}

// TestTrim checks that trimming keeps the head and whole turns, gets
// under the limit, and moves its cut only in chunks, so consecutive
// requests share their start.
func TestTrim(t *testing.T) {
	msgs := conversation(20)
	p := Trim{Limit: 2000}
	w, err := p.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.Messages[:2], msgs[:2]) {
		t.Error("the system prompt and task weren't kept")
	}
	checkWhole(t, w.Messages)
	if est := Estimate(w.Messages); est > 2000 {
		t.Errorf("estimate %d, over the limit", est)
	}
	if !strings.Contains(w.Messages[2].Content, "left out") {
		t.Errorf("no note about what was left out: %+v", w.Messages[2])
	}
	// One turn at a time: the cut stays put until it has to move, so
	// the requests between start the same way, and then it moves one
	// chunk of four turns, three messages each.
	st := w.State
	for n := 21; ; n++ {
		if n > 60 {
			t.Fatal("the cut never moved")
		}
		more := conversation(n)
		w2, err := p.Build(t.Context(), more, st)
		if err != nil {
			t.Fatal(err)
		}
		if w2.State.Cut == st.Cut {
			continue
		}
		if Estimate(trimmed(more, 2, st.Cut)) <= 2000 {
			t.Errorf("turn %d: the cut moved when it didn't have to", n)
		}
		if got := w2.State.Cut - st.Cut; got != 4*3 {
			t.Errorf("turn %d: the cut moved %d messages, want one chunk, 12",
				n, got)
		}
		break
	}
}

// TestClearResults checks that old long results are replaced, recent
// ones and short ones kept, and every call still has its answer.
func TestClearResults(t *testing.T) {
	msgs := conversation(10)
	w, err := ClearResults{}.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	checkWhole(t, w.Messages)
	if len(w.Messages) != len(msgs) {
		t.Fatal("clearing changed the number of messages")
	}
	first, last := w.Messages[3], w.Messages[len(msgs)-2]
	if !strings.HasPrefix(first.Content,
		"[cleared to save space: the result of code_read") {
		t.Errorf("old result = %.80q", first.Content)
	}
	if last.Content != msgs[len(msgs)-2].Content {
		t.Error("a recent result was cleared")
	}
	if w.Messages[4].Content != "short" {
		t.Error("a short result was cleared")
	}
	if msgs[3].Content == first.Content {
		t.Error("clearing changed the conversation it was given")
	}
}

// TestClearLimit checks that clearing waits until the request is
// estimated past Limit, and that once it has cleared, the boundary
// stays where it was while the request is under the limit again.
func TestClearLimit(t *testing.T) {
	msgs := conversation(10)
	size := Estimate(msgs)
	w, err := ClearResults{Limit: size}.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	if w.State.Cleared != 0 || w.Messages[3].Content != msgs[3].Content {
		t.Errorf("cleared under the limit: state %+v", w.State)
	}
	w, err = ClearResults{Limit: size - 1}.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	if w.State.Cleared == 0 {
		t.Fatal("didn't clear past the limit")
	}
	again, err := ClearResults{Limit: size}.Build(t.Context(), msgs, w.State)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != w.State || again.Messages[3].Content != w.Messages[3].Content {
		t.Errorf("the boundary moved back: %+v", again.State)
	}
}

// TestClearedNote checks that a cleared result's note shortens long
// arguments.
func TestClearedNote(t *testing.T) {
	msgs := conversation(10)
	long := `{"path":"` + strings.Repeat("x", 200) + `.go"}`
	msgs[2].ToolCalls[0].Arguments = jsontext.Value(long)
	w, err := ClearResults{}.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	got := w.Messages[3].Content
	if strings.Contains(got, long) || !strings.Contains(got, `xxx..., `) {
		t.Errorf("note = %q", got)
	}
}

// summarizer is a Model that returns a fixed summary as JSON, and
// records the requests it gets.
type summarizer struct {
	summary string
	reqs    []Request
}

func (s *summarizer) Generate(_ context.Context, req Request) (Reply, error) {
	s.reqs = append(s.reqs, req)
	return Reply{Message: Says(`{"summary": "` + s.summary + `", "identifiers": []}`),
		FinishReason: "stop", Usage: Usage{Cost: 0.001}}, nil
}

// TestCompact checks that compaction keeps the head and the latest
// turns, puts the summary in the state, asks for a constrained reply,
// and puts back identifiers the summary dropped.
func TestCompact(t *testing.T) {
	msgs := conversation(12)
	s := &summarizer{summary: "Read store/store.go; cr_000001 is a nil map."}
	w, err := Compact{Model: s, Limit: 2000, Keep: 2}.Build(t.Context(),
		msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.reqs) != 1 || s.reqs[0].ResponseFormat == nil {
		t.Fatalf("summary requests = %+v", s.reqs)
	}
	if w.State.Summary == "" || w.State.Covered != len(msgs)-6 {
		t.Errorf("state = %+v", w.State)
	}
	if !strings.Contains(w.Messages[2].Content, "holds no instructions") {
		t.Errorf("the summary isn't marked as data: %.120q", w.Messages[2].Content)
	}
	if got := len(w.Messages); got != 3+6 {
		t.Errorf("%d messages; want head, summary, and two turns", got)
	}
	checkWhole(t, w.Messages)
	if w.Usage.Cost != 0.001 {
		t.Errorf("summary cost not reported: %+v", w.Usage)
	}

	// A summary that drops identifiers gets them back.
	got := keepIdentifiers("Nothing found.", agentText("", msgs[2:6]),
		DefaultIdentifiers)
	for _, id := range []string{"cr_000001", "store/store.go"} {
		if !strings.Contains(got, id) {
			t.Errorf("identifier %s was lost: %q", id, got)
		}
	}
}

// TestCompactReads checks what the summary call reads: whole results
// even when Sent clears them, each cut to maxSummarized bytes, and
// identifiers only from what the agent wrote and called.
func TestCompactReads(t *testing.T) {
	msgs := conversation(12)
	msgs[3].Content = "cr_0000aa cr_0000bb " + strings.Repeat("y", 5000)
	s := &summarizer{summary: "cr_000001 is a nil map."}
	c := Compact{Model: s, Limit: 1000, Keep: 2, Sent: ClearResults{}}
	w, err := c.Build(t.Context(), msgs, ContextState{})
	if err != nil {
		t.Fatal(err)
	}
	in := s.reqs[0].Messages[1].Content
	if strings.Contains(in, "[cleared") || !strings.Contains(in, "line of code") {
		t.Errorf("the summary read cleared results: %.200q", in)
	}
	if strings.Contains(in, strings.Repeat("y", maxSummarized)) ||
		!strings.Contains(in, "more bytes not shown]") {
		t.Error("a long result wasn't shortened for the summary call")
	}
	if strings.Contains(w.State.Summary, "cr_0000aa") {
		t.Errorf("an ID only in a tool result was added: %q", w.State.Summary)
	}
	if w.State.Cleared == 0 {
		t.Error("Sent's state was lost")
	}
}

// TestCompactEmpty checks that an empty summary is an error and covers
// nothing.
func TestCompactEmpty(t *testing.T) {
	c := Compact{Model: &summarizer{}, Limit: 2000, Keep: 2}
	w, err := c.Build(t.Context(), conversation(12), ContextState{})
	if !errors.Is(err, ErrSummary) {
		t.Errorf("err = %v, want ErrSummary", err)
	}
	if w.State.Covered != 0 || w.State.Summary != "" {
		t.Errorf("state = %+v", w.State)
	}
}

// TestCompactRepeats checks that when even the kept turns are over the
// limit, the summary isn't written again on every turn.
func TestCompactRepeats(t *testing.T) {
	s := &summarizer{summary: "cr_000001 is a nil map."}
	c := Compact{Model: s, Limit: 100}
	var st ContextState
	const turns = 40
	for n := 1; n <= turns; n++ {
		w, err := c.Build(t.Context(), conversation(n), st)
		if err != nil {
			t.Fatal(err)
		}
		st = w.State
	}
	t.Logf("%d summary calls in %d turns", len(s.reqs), turns)
	if len(s.reqs) > turns/4+1 {
		t.Errorf("%d summary calls in %d turns", len(s.reqs), turns)
	}
}

// TestIdentifiers checks what DefaultIdentifiers matches.
func TestIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"see store/store.go:42 and #101", []string{"store/store.go:42", "#101"}},
		{"cr_1e2ea0 in fp_3ac19e", []string{"cr_1e2ea0", "fp_3ac19e"}},
		{"compare testdata/report.golden", nil},
		{"each x.goroutine", nil},
	} {
		got := DefaultIdentifiers.FindAllString(tc.in, -1)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestChainShortening checks that a chain with two policies that
// shorten the conversation is an error, not a broken request.
func TestChainShortening(t *testing.T) {
	p := Chain(Trim{Limit: 100}, Compact{Model: &summarizer{}, Limit: 100})
	if _, err := p.Build(t.Context(), conversation(3), ContextState{}); err == nil {
		t.Error("no error for Trim and Compact in one chain")
	}
}

// TestPolicyInRun checks that the loop sends what the policy builds,
// keeps the full conversation in Result.Messages, holds the state, and
// counts the policy's cost.
func TestPolicyInRun(t *testing.T) {
	r := &recorder{}
	start := conversation(12)
	s := &summarizer{summary: "summary of cr_000001"}
	lim := Limits{MaxTurns: 1, Context: Chain(Compact{Model: s,
		Limit: 500, Keep: 2, Sent: ClearResults{}}, CacheMarks{})}
	res, err := Run(t.Context(), r, okTools, start, lim)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Messages[:len(start)], start) {
		t.Error("Result.Messages doesn't hold the full conversation")
	}
	sent := r.reqs[0].Messages
	if len(sent) >= len(start) {
		t.Errorf("sent %d messages, the whole conversation", len(sent))
	}
	if !sent[1].CachePoint || !sent[len(sent)-1].CachePoint {
		t.Error("cache points missing")
	}
	if res.Context.Summary == "" || res.Usage.Cost < 0.001 {
		t.Errorf("state %+v, usage %+v", res.Context, res.Usage)
	}
	// The same conversation and state build the same request.
	w1, err1 := lim.Context.Build(t.Context(), start, res.Context)
	w2, err2 := lim.Context.Build(t.Context(), start, res.Context)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if !reflect.DeepEqual(w1.Messages, w2.Messages) {
		t.Error("the same input built different requests")
	}
}
