package main

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch13/evals"
	"github.com/blackwell-systems/go-agents-book/code/ch13/triage"
)

// TestVersions checks that the version without the crash-site
// sentence is the agent's prompt minus exactly its last paragraph.
func TestVersions(t *testing.T) {
	p := withoutCrashSite(triage.Prompt)
	if !strings.HasPrefix(triage.Prompt, p) || strings.Contains(p, "The function that crashed") {
		t.Errorf("prompt without the crash-site sentence:\n%s", p)
	}
	if rest := strings.TrimPrefix(triage.Prompt, p); strings.Count(rest, "\n\n") != 1 {
		t.Errorf("more than one paragraph removed: %q", rest)
	}
}

// TestGraderFault checks which check details are the grader's own
// failures.
func TestGraderFault(t *testing.T) {
	for detail, want := range map[string]bool{
		"could not import math (open /x/go-build/19/abc-d: no such file or directory)": true,
		"open /Users/x/Library/Caches/go-build/00/00f-d: no such file or directory":    true,
		"answered issue 0, want 101":                    false,
		"FAIL\texample.com/remind/store [build failed]": false,
	} {
		if got := graderFault(detail); got != want {
			t.Errorf("%q: %v, want %v", detail, got, want)
		}
	}
}

// TestReviewer checks the reviewer in front of the final tool: a
// rejection goes back to the agent as an error with the reasons,
// redacted and marked as the reviewer's opinion; an acceptance runs the
// tool; after two rejections the tool runs unreviewed; and a reply with
// an unknown verdict counts as a failed review, not an acceptance.
func TestReviewer(t *testing.T) {
	reply := func(v string) agent.Message {
		return agent.Says(`{"verdict":"` + v + `","group":"fp_1","problem":` +
			`"root_cause","reasons":"group A is wrong; key sk-ab12cd34ef56gh78ij90"}`)
	}
	ran := 0
	final := agent.Tool{Final: true, Run: func(context.Context, jsontext.Value) (string, error) {
		ran++
		return "accepted", nil
	}}
	env := &triage.Env{Original: t.TempDir(), Dir: t.TempDir()}
	for _, tc := range []struct {
		script     []string
		runs, errs int
		failed     int
	}{
		{[]string{"accept"}, 1, 0, 0},
		{[]string{"reject", "accept"}, 1, 1, 0},
		{[]string{"reject", "reject"}, 1, 2, 0}, // the third call goes unreviewed
		{[]string{"Reject"}, 1, 0, 1},           // unknown: a failed review
		{[]string{"cut"}, 1, 0, 1},              // out of tokens: failed too
	} {
		ran = 0
		pool := &agent.SpendPool{}
		rv := &reviewer{env: env, pool: pool,
			model: &scripted{replies: tc.script, reply: reply}}
		tool := rv.wrap(final)
		errs := 0
		for range 3 {
			out, err := tool.Run(t.Context(), jsontext.Value(`{"groups":[]}`))
			if err != nil {
				errs++
				msg := err.Error()
				if !strings.Contains(msg, "group A is wrong") ||
					!strings.Contains(msg, "may be wrong") ||
					strings.Contains(msg, "sk-ab12cd34") {
					t.Errorf("rejection = %v", err)
				}
				continue
			}
			if out == "accepted" {
				break
			}
		}
		_, failed, _ := rv.Results()
		if ran != tc.runs || errs != tc.errs || failed != tc.failed {
			t.Errorf("%v: tool ran %d times, %d rejections, %d failed reviews; "+
				"want %d, %d, %d", tc.script, ran, errs, failed,
				tc.runs, tc.errs, tc.failed)
		}
	}
}

// TestJobOrder checks that runs are scheduled task by task, with every
// version of a task together, and only as many as each task still needs.
func TestJobOrder(t *testing.T) {
	r := &evalRunner{ec: evalConfig{runs: 2, versions: []string{"base", "checked"}}}
	r.records = []evals.Record{
		{Version: "base", Task: "a", Verdict: "pass"},
		{Version: "base", Task: "a", Verdict: "unscored"},
	}
	got := r.missing([]string{"a", "b"}, 1)
	want := []job{{"base", "a", 3}, {"checked", "a", 3}, {"checked", "a", 4},
		{"base", "b", 3}, {"checked", "b", 3}, {"base", "b", 4}, {"checked", "b", 4}}
	if !slices.Equal(got, want) {
		t.Errorf("jobs = %v\nwant %v", got, want)
	}
}

// TestResume checks that an eval reads back the runs its results file
// already holds for its set and versions, and numbers new runs after
// them, so their replay scripts are new too.
func TestResume(t *testing.T) {
	file := filepath.Join(t.TempDir(), "results.jsonl")
	for _, rec := range []evals.Record{
		{Set: "hard", Version: "base", Task: "a", Run: 1, Verdict: "pass"},
		{Set: "hard", Version: "base", Task: "a", Run: 2, Verdict: "unscored"},
		{Set: "confirm", Version: "base", Task: "a", Run: 1, Verdict: "pass"},
		{Set: "hard", Version: "checked", Task: "a", Run: 1, Verdict: "pass"},
	} {
		if err := appendRecord(file, rec); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := readRecords(file, "hard", []string{"base"})
	if err != nil || len(recs) != 2 {
		t.Fatalf("read %d records, %v; want 2", len(recs), err)
	}
	r := &evalRunner{ec: evalConfig{runs: 2, versions: []string{"base"}},
		records: recs}
	got := r.missing([]string{"a", "b"}, 0)
	want := []job{{"base", "a", 3}, {"base", "b", 1}, {"base", "b", 2}}
	if !slices.Equal(got, want) {
		t.Errorf("jobs = %v\nwant %v", got, want)
	}
	if recs, err := readRecords(filepath.Join(t.TempDir(), "none"), "hard",
		[]string{"base"}); err != nil || len(recs) != 0 {
		t.Errorf("a missing file gave %v, %v", recs, err)
	}
}

// TestReserve checks the budget: a run starts only if the budget covers
// it with every run in flight counted at its full max-cost, waits while
// runs in flight are in the way, and is refused when nothing is.
func TestReserve(t *testing.T) {
	r := &evalRunner{c: config{maxCost: 0.25}, ec: evalConfig{budget: 1}}
	r.spent = 0.4
	for range 2 {
		if !r.reserve() {
			t.Fatal("two runs at $0.25 don't fit with $0.40 spent of $1")
		}
	}
	third := make(chan bool)
	go func() { third <- r.reserve() }() // waits: $0.40 + 3 x $0.25 > $1
	select {
	case <-third:
		t.Fatal("a third run started with two in flight")
	case <-time.After(50 * time.Millisecond):
	}
	r.add(evals.Record{Cost: 0.05}, nil) // $0.45 + 2 x $0.25 fits
	if !<-third {
		t.Fatal("the third run didn't start once a run ended")
	}
	r.add(evals.Record{Cost: 0.3}, nil)
	r.add(evals.Record{Cost: 0.2}, nil) // $0.95 spent, none in flight
	if r.reserve() {
		t.Error("a run started that the budget can't cover")
	}
}

// TestChanges checks the diff the reviewer reads: a changed file and a
// new test file, both in full, with neither tree's path in it.
func TestChanges(t *testing.T) {
	orig, dir := t.TempDir(), t.TempDir()
	for _, d := range []string{orig, dir} {
		if err := os.MkdirAll(filepath.Join(d, "store"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(orig, "store", "s.go"), "package store\n")
	write(filepath.Join(dir, "store", "s.go"), "package store\n// fixed\n")
	write(filepath.Join(dir, "store", "repro_test.go"), "package store\n// repro\n")
	got, err := changes(t.Context(), &triage.Env{Original: orig, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+// fixed", "+// repro", "store/repro_test.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, orig) || strings.Contains(got, dir) {
		t.Errorf("diff holds a tree's path:\n%s", got)
	}
}

// scripted is a model that answers each call with the next verdict.
type scripted struct {
	replies []string
	reply   func(string) agent.Message
	n       int
}

func (s *scripted) Generate(context.Context, agent.Request) (agent.Reply, error) {
	v := s.replies[min(s.n, len(s.replies)-1)]
	s.n++
	if v == "cut" { // a reply that ran out of tokens mid-answer
		return agent.Reply{Message: agent.Says(`{"verdict":"rej`),
			FinishReason: "length"}, nil
	}
	return agent.Reply{Message: s.reply(v)}, nil
}
