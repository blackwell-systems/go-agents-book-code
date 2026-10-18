package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch09/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch09/triage"
)

// TestVariants checks that every version starts from the base prompt
// and that each one-change version adds exactly its own sentence.
func TestVariants(t *testing.T) {
	for _, name := range variantNames() {
		p := variants[name](basePrompt)
		if !strings.HasPrefix(p, basePrompt) {
			t.Errorf("%s doesn't start from the base prompt", name)
		}
		added := 0
		for _, s := range []string{duplicates, oldVersions, crashSite} {
			if strings.Contains(p, s) {
				added++
			}
		}
		want := map[string]int{"v1": 0, "all": 3}[name]
		if name != "v1" && name != "all" {
			want = 1
		}
		if added != want {
			t.Errorf("%s adds %d sentences, want %d", name, added, want)
		}
	}
}

// TestRootCausesRight checks the scoring of a grade's checks.
func TestRootCausesRight(t *testing.T) {
	ok := catalog.Check{Name: "root cause", Pass: true}
	for _, tc := range []struct {
		checks []catalog.Check
		want   bool
	}{
		{[]catalog.Check{ok, ok}, true},
		{[]catalog.Check{ok, {Name: "root cause"}}, false},
		{[]catalog.Check{ok, {Name: "found"}}, false},
		{[]catalog.Check{{Name: "status", Pass: true}}, false},
	} {
		if got := rootCausesRight(tc.checks); got != tc.want {
			t.Errorf("%+v: got %v", tc.checks, got)
		}
	}
}

// TestSummary checks the table's arithmetic.
func TestSummary(t *testing.T) {
	got := summary([]result{
		{Variant: "v1", RootCause: true, Passed: false, Turns: 10, Cost: 0.02},
		{Variant: "v1", RootCause: false, Passed: false, Turns: 14, Cost: 0.04},
		{Variant: "all", RootCause: true, Passed: true, Turns: 12, Cost: 0.03},
	}, []string{"v1", "all"})
	for _, want := range []string{"v1           1 of 2      0 of 2          14   $0.0300",
		"all          1 of 1      1 of 1          12   $0.0300"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

// TestReplayWithoutRecording checks that, with no saved calls, a run
// is reported as stopped, not as a crash of the check, and graded as
// diagnosing nothing.
func TestReplayWithoutRecording(t *testing.T) {
	var out bytes.Buffer
	err := check(t.Context(), "", &out, config{repo: "../..",
		replays: t.TempDir(), variants: []string{"v1"},
		tasks: []string{"d1-empty-snooze"}, runs: 1, maxTurns: 5, maxCost: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v1 d1-empty-snooze 1: root cause false, passed false, 0 turns",
		"no saved response", "v1           0 of 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if err := check(t.Context(), "", &out, config{variants: []string{"v9"}}); err == nil {
		t.Error("an unknown version should be an error")
	}
}

// TestConnectionFailed checks that only a failed connection makes the
// check run a task again, not a missing recording or a timeout.
func TestConnectionFailed(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("send request: %w",
			&url.Error{Op: "Post", URL: "https://x", Err: err})
	}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{wrap(errors.New("connection reset by peer")), true},
		{wrap(replay.ErrNoRecording), false},
		{wrap(context.DeadlineExceeded), false},
		{errors.New("agent: max turns reached"), false},
	} {
		if got := connectionFailed(tc.err); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.err, got, tc.want)
		}
	}
}

// TestAgentPrompt checks that the triage agent's prompt is the version
// the check measured best, so the two can't drift apart.
func TestAgentPrompt(t *testing.T) {
	if got, want := triage.Prompt, variants["all"](basePrompt); got != want {
		t.Errorf("triage.Prompt isn't the \"all\" version:\n%s", got)
	}
}
