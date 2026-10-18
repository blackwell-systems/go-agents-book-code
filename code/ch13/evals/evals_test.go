package evals

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
)

// TestRange checks the Wilson interval against values worked by hand,
// including the ends, where the familiar formula gives a width of zero.
func TestRange(t *testing.T) {
	for _, tc := range []struct {
		r      Rate
		lo, hi float64
	}{
		{Rate{0, 10}, 0, 0.2775},
		{Rate{5, 10}, 0.2366, 0.7634},
		{Rate{10, 10}, 0.7225, 1},
		{Rate{219, 261}, 0.7896, 0.8787},
		{Rate{0, 0}, 0, 1},
	} {
		lo, hi := tc.r.Range()
		if math.Abs(lo-tc.lo) > 1e-4 || math.Abs(hi-tc.hi) > 1e-4 {
			t.Errorf("%v: range %.4f to %.4f, want %.4f to %.4f",
				tc.r, lo, hi, tc.lo, tc.hi)
		}
	}
}

// TestRangeEnds checks the exact ends and that an impossible rate is a
// programmer's error.
func TestRangeEnds(t *testing.T) {
	if lo, _ := (Rate{0, 7}).Range(); lo != 0 {
		t.Errorf("0 of 7: lo = %v", lo)
	}
	if _, hi := (Rate{7, 7}).Range(); hi != 1 {
		t.Errorf("7 of 7: hi = %v", hi)
	}
	defer func() {
		if recover() == nil {
			t.Error("8 of 7 didn't panic")
		}
	}()
	Rate{8, 7}.Range()
}

// TestPassK checks the unbiased pass@k and pass^k against values
// worked by hand, and that tasks with fewer than k runs are skipped.
func TestPassK(t *testing.T) {
	rs := Runs{
		"a": {true, true, true},    // 3 of 3
		"b": {true, false, true},   // 2 of 3
		"c": {false, false, false}, // 0 of 3
		"d": {true},                // too few runs for k > 1
	}
	src := func() rand.Source { return rand.NewPCG(1, 2) }
	for _, tc := range []struct {
		name  string
		got   Estimate
		want  float64
		tasks int
	}{
		{"pass@1", rs.PassAt(1, src()), (1 + 2.0/3 + 0 + 1) / 4, 4},
		{"pass@2", rs.PassAt(2, src()), (1 + 1 + 0) / 3.0, 3},
		{"pass^2", rs.PassAll(2, src()), (1 + 1.0/3 + 0) / 3, 3},
		{"pass^3", rs.PassAll(3, src()), (1 + 0 + 0) / 3.0, 3},
	} {
		if math.Abs(tc.got.Mean-tc.want) > 1e-9 || tc.got.Tasks != tc.tasks {
			t.Errorf("%s = %v over %d tasks, want %v over %d",
				tc.name, tc.got.Mean, tc.got.Tasks, tc.want, tc.tasks)
		}
		if !math.IsNaN(tc.got.Lo) {
			t.Errorf("%s: a range over %d tasks", tc.name, tc.got.Tasks)
		}
	}
	if got := rs.Rate(); got != (Rate{6, 10}) {
		t.Errorf("rate = %v", got)
	}
}

// TestCompare checks the paired comparison: the counts, the mean
// difference, each task's results, no range below MinTasks tasks, a
// range that holds the difference above it, and that a version against
// itself differs by exactly nothing.
func TestCompare(t *testing.T) {
	a := Runs{"x": {false, false}, "y": {true, false}, "z": {true, true}, "w": {true}}
	b := Runs{"x": {true, true}, "y": {true, false}, "z": {true, false}, "v": {true}}
	c := Compare(a, b, rand.NewPCG(1, 2))
	if c.Tasks != 3 || c.Better != 1 || c.Worse != 1 || c.Tied != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if want := (1.0 + 0 - 0.5) / 3; math.Abs(c.Diff-want) > 1e-9 {
		t.Errorf("diff = %v, want %v", c.Diff, want)
	}
	first := Pair{"x", Rate{0, 2}, Rate{2, 2}}
	if !math.IsNaN(c.Lo) || len(c.Each) != 3 || c.Each[0] != first {
		t.Errorf("three tasks: range %v to %v, each %v", c.Lo, c.Hi, c.Each)
	}

	x, y := Runs{}, Runs{}
	for i := range MinTasks {
		task := fmt.Sprint("t", i)
		x[task] = []bool{i%3 == 0, true}
		y[task] = []bool{i%2 == 0, i%3 != 0}
	}
	c = Compare(x, y, rand.NewPCG(1, 2))
	if math.IsNaN(c.Lo) || c.Lo > c.Diff || c.Hi < c.Diff {
		t.Errorf("%d tasks: range %v to %v around %v", c.Tasks, c.Lo, c.Hi, c.Diff)
	}
	again := Compare(x, y, rand.NewPCG(1, 2))
	if again.Lo != c.Lo || again.Hi != c.Hi {
		t.Error("same seed, different range")
	}
	self := Compare(x, x, rand.NewPCG(1, 2))
	if self.Diff != 0 || self.Lo != 0 || self.Hi != 0 || self.Tied != MinTasks {
		t.Errorf("a version against itself = %+v", self)
	}
}

// TestDrops checks the one-sided Fisher test: 5 of 5 before and 0 of 5
// now has p = 1/252, a drop; one failure in three after three of three
// isn't.
func TestDrops(t *testing.T) {
	if p := fisherLower(0, 5, 5, 5); math.Abs(p-1.0/252) > 1e-12 {
		t.Errorf("p = %v, want 1/252", p)
	}
	history := Runs{"a": {true, true, true, true, true}, "b": {true, true, true}}
	now := Runs{"a": {false, false, false, false, false}, "b": {true, false, true}}
	if got := Drops(history, now, 0.05); !slices.Equal(got, []string{"a"}) {
		t.Errorf("drops = %v", got)
	}
}

// TestMissing checks the reruns needed to bring every task to quota.
func TestMissing(t *testing.T) {
	rs := Runs{"a": {true, false, true}, "b": {true}}
	got := Missing(rs, []string{"a", "b", "c"}, 3)
	if len(got) != 2 || got["b"] != 2 || got["c"] != 3 {
		t.Errorf("missing = %v", got)
	}
}

var errHarness = errors.New("harness failed")

// TestJudge checks the verdict for every kind of ending.
func TestJudge(t *testing.T) {
	unscored := func(err error) bool { return errors.Is(err, errHarness) }
	turns := errors.New("out of turns")
	for _, tc := range []struct {
		o    Outcome
		want Verdict
	}{
		{Outcome{Graded: true, Passed: true}, Pass},
		{Outcome{Graded: true}, Fail},
		{Outcome{Err: turns}, Fail},
		{Outcome{Err: turns, Graded: true, Passed: true}, Fail},
		{Outcome{Err: fmt.Errorf("run: %w", errHarness)}, Unscored},
		{Outcome{Graded: true, GraderFault: true}, Unscored},
		{Outcome{}, Fail}, // no answer, no error: nothing to pass
	} {
		if got := Judge(tc.o, unscored); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.o, got, tc.want)
		}
	}
}

// TestTiers checks that every catalog task has exactly one tier, the
// tiers' sizes, that the tuning set is drawn from both tiers, and that
// a task listed in two tiers is caught.
func TestTiers(t *testing.T) {
	tasks, err := catalog.Load("../../catalog/testdata/tasks")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	count := map[Tier]int{}
	for _, task := range tasks {
		tier, ok := TierOf(task.ID)
		if !ok {
			t.Errorf("%s has no tier", task.ID)
		}
		seen[task.ID] = true
		count[tier]++
	}
	for id := range tiers {
		if !seen[id] {
			t.Errorf("tier for %s, which isn't in the catalog", id)
		}
	}
	tuned := map[Tier]int{}
	for id := range tuning {
		tier, ok := TierOf(id)
		if !ok {
			t.Errorf("tuning task %s has no tier", id)
		}
		tuned[tier]++
	}
	if count[Floor] != 22 || count[Hard] != 10 || len(tuning) != 12 ||
		tuned[Floor] == 0 || tuned[Hard] == 0 {
		t.Errorf("tiers %v, tuning %v of %d", count, tuned, len(tuning))
	}
	defer func() {
		if recover() == nil {
			t.Error("a task in two tiers didn't panic")
		}
	}()
	byTier(map[Tier][]string{Floor: {"x"}, Hard: {"x"}})
}

// TestReport checks the report's handling of unscored runs: a task with
// no scored runs is listed as short, cost per scored run leaves out the
// unscored run's spend, which has its own column.
func TestReport(t *testing.T) {
	rs := []Record{
		{Version: "base", Task: "d1-empty-snooze", Verdict: "pass", Cost: 0.03, Turns: 10},
		{Version: "base", Task: "d1-empty-snooze", Verdict: "fail", Cost: 0.03, Turns: 12},
		{Version: "base", Task: "m2-unknown-zone-list", Verdict: "unscored", Cost: 0.02, Turns: 3},
	}
	var b strings.Builder
	err := Report(&b, rs, []string{"d1-empty-snooze", "m2-unknown-zone-list"}, 2,
		func() rand.Source { return rand.NewPCG(1, 2) })
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"$0.0300", "1 of 3", "$0.0200",
		"short of 2 scored runs", "m2-unknown-zone-list (0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}
