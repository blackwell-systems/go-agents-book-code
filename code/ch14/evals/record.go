package evals

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"text/tabwriter"
)

// tag::record[]

// A Record is one run in a results file, with what produced it, so a
// number can be traced back to the model, provider, prompt, and code.
type Record struct {
	Set, Version, Task string
	Run                int
	Model, Provider    string
	Reviewer           string // the reviewer's model, if any
	MaxTurns           int
	MaxCost            float64
	Date, GoVersion    string
	Commit             string
	Dirty              bool   // the code had uncommitted changes
	PromptHash         string // the prompt and the tool definitions
	Verdict            string // pass, fail, or unscored
	Failed             []string
	Err                string
	Turns              int
	Cost               float64
	Gate               []string // the gate's refusals and refilings
	Review             []string // the reviewer's verdicts
	ReviewErrors       int      // reviews that failed

	// Attempted and Succeeded say, on an injection task, whether the
	// model tried what the injected order asked, and whether it is in
	// the run's results (chapter 14).
	Attempted, Succeeded bool `json:",omitzero"`
	Rung                 int  `json:",omitzero"` // the sandbox's
}

// end::record[]

// Scored groups records by task, leaving out unscored runs, so the
// statistics in this package can read them.
func Scored(rs []Record, keep func(Record) bool) Runs {
	out := Runs{}
	for _, r := range rs {
		if r.Verdict != Unscored.String() && (keep == nil || keep(r)) {
			out[r.Task] = append(out[r.Task], r.Verdict == Pass.String())
		}
	}
	return out
}

// Report writes a results table for each version in rs: the pass rate
// over tasks with its range; pass^k and pass@k, with k the runs each
// task was meant to get; cost and turns over scored runs, with the
// cost per passing run, which charges failed runs to the passes; what
// the unscored runs cost, separately; and the reviewer's failures. Below
// the table it lists the tasks of each version that are short of k
// scored runs, which the statistics leave out.
func Report(w io.Writer, rs []Record, tasks []string, k int,
	src func() rand.Source) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "version\ttier\ttasks\tpass rate\tevery run\t"+
		"at least once\tunscored\tcost/scored run\tcost/pass\t"+
		"turns (median)\tunscored cost\treview errors")
	versions := slices.Sorted(maps.Keys(byVersion(rs)))
	for _, v := range versions {
		for _, tier := range []Tier{"", Floor, Hard} {
			in := func(r Record) bool {
				t, _ := TierOf(r.Task)
				return r.Version == v && (tier == "" || t == tier)
			}
			row(tw, v, cmp.Or(string(tier), "all"), rs, in, k, src)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	for _, v := range versions {
		scored := Scored(rs, func(r Record) bool { return r.Version == v })
		var short []string
		for _, t := range tasks {
			if n := len(scored[t]); n < k {
				short = append(short, fmt.Sprintf("%s (%d)", t, n))
			}
		}
		if len(short) > 0 {
			if _, err := fmt.Fprintf(w, "%s: short of %d scored runs, "+
				"left out of pass^k and pass@k: %s\n", v, k,
				strings.Join(short, ", ")); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
		}
	}
	return nil
}

func byVersion(rs []Record) map[string]bool {
	out := map[string]bool{}
	for _, r := range rs {
		out[r.Version] = true
	}
	return out
}

// row writes one line of the report.
func row(w io.Writer, version, tier string, rs []Record,
	in func(Record) bool, k int, src func() rand.Source) {
	scored := Scored(rs, in)
	var turns []int
	runs, unscored, passed, reviewErrs := 0, 0, 0, 0
	cost, unscoredCost := 0.0, 0.0
	for _, r := range rs {
		if !in(r) {
			continue
		}
		runs++
		reviewErrs += r.ReviewErrors
		if r.Verdict == Unscored.String() {
			unscored++
			unscoredCost += r.Cost
			continue
		}
		cost += r.Cost
		turns = append(turns, r.Turns)
		if r.Verdict == Pass.String() {
			passed++
		}
	}
	if runs == 0 {
		return
	}
	every, once := "n/a", "n/a"
	if k > 1 {
		every = fmt.Sprintf("pass^%d %s", k, pct(scored.PassAll(k, src()).Mean))
		once = fmt.Sprintf("pass@%d %s", k, pct(scored.PassAt(k, src()).Mean))
	}
	perRun, perPass, median := "n/a", "none passed", 0
	if len(turns) > 0 {
		perRun = fmt.Sprintf("$%.4f", cost/float64(len(turns)))
		slices.Sort(turns)
		median = turns[len(turns)/2]
	}
	if passed > 0 {
		perPass = fmt.Sprintf("$%.4f", cost/float64(passed))
	}
	rate := "no scored runs"
	if len(scored) > 0 {
		rate = scored.PassAt(1, src()).String()
	}
	fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%d of %d\t%s\t%s\t%d\t$%.4f\t%d\n",
		version, tier, len(scored), rate, every, once, unscored, runs,
		perRun, perPass, median, unscoredCost, reviewErrs)
}

func pct(x float64) string {
	if math.IsNaN(x) {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*x)
}

// Tasks returns the catalog's single tasks in a set: "tuning",
// "confirm", "floor", "hard", "injection" (chapter 14), or "all",
// sorted.
func Tasks(set string) ([]string, error) {
	var out []string
	for task, tier := range tiers {
		switch {
		case set == "all",
			set == "tuning" && tuning[task],
			set == "confirm" && !tuning[task],
			set == "injection" && injection(task),
			set == string(tier):
			out = append(out, task)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no task set %q; try tuning, confirm, "+
			"floor, hard, injection, or all", set)
	}
	slices.Sort(out)
	return out, nil
}

// injection reports whether a task is one of the catalog's injection
// tasks, i1 to i8.
func injection(task string) bool {
	return len(task) > 2 && task[0] == 'i' && task[1] >= '1' &&
		task[1] <= '9' && task[2] == '-'
}
