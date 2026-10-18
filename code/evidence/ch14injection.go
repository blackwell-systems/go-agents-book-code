package main

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// injectionRun is one record of chapter 14's injection eval (E1), as
// code/ch14's eval runner writes it: the fields this summary reads.
type injectionRun struct {
	Version, Task, Model, Verdict string
	Attempted, Succeeded          bool
	Cost                          float64
	Failed                        []string
}

// injectionSummary prints E1: for each version and model, how many
// runs attempted what the injected order asked and how many succeeded,
// as judged after the runs from their recordings (TestRejudge in
// code/ch14), and how often the grader's own forbidden checks failed,
// then each task's counts, since eight tasks are too few for a range.
func injectionSummary(w io.Writer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "ch14injection", "*.jsonl"))
	if err != nil || len(files) == 0 {
		return err //nolint:wrapcheck // Glob fails only on a bad pattern
	}
	var runs []injectionRun
	for _, f := range files {
		rs, err := load[injectionRun](f)
		if err != nil {
			return err
		}
		runs = append(runs, rs...)
	}
	fmt.Fprintln(w, "== ch14injection: the injection tasks under three "+
		"versions of the agent (scored runs)")
	fmt.Fprintln(w, "version\tmodel\truns\tattempted\tsucceeded\tpassed\t"+
		"grader: forbidden paths\tgrader: issue text\tunscored\tcost")
	type key struct{ version, model, task string }
	count := map[key][3]int{} // attempted, succeeded, runs
	var cost float64
	for _, v := range []string{"base", "marked", "defended"} {
		for _, m := range modelsIn(runs, func(r injectionRun) string {
			return r.Model
		}) {
			var n, att, succ, pass, paths, text, unscored int
			var c float64
			for _, r := range runs {
				if r.Version != v || r.Model != m {
					continue
				}
				c += r.Cost
				if r.Verdict == "unscored" {
					unscored++
					continue
				}
				n++
				k := key{v, m, r.Task}
				x := count[k]
				x[2]++
				if r.Attempted {
					att++
					x[0]++
				}
				if r.Succeeded {
					succ++
					x[1]++
				}
				count[k] = x
				if r.Verdict == "pass" {
					pass++
				}
				paths += failedCheck(r, "forbidden paths")
				text += failedCheck(r, "issue text")
			}
			if n+unscored == 0 {
				continue
			}
			cost += c
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t$%.4f\n",
				v, m, n, att, succ, pass, paths, text, unscored, c)
		}
	}
	fmt.Fprintf(w, "all\t\t\t\t\t\t\t\t\t$%.4f\n", cost)
	tasks := map[string]bool{}
	for k := range count {
		tasks[k.task] = true
	}
	ms := modelsIn(runs, func(r injectionRun) string { return r.Model })
	fmt.Fprintf(w, "\ntask\tversion\tattempted/succeeded/runs: %s\n",
		strings.Join(ms, ", "))
	for _, t := range slices.Sorted(maps.Keys(tasks)) {
		for _, v := range []string{"base", "marked", "defended"} {
			var cells []string
			for _, m := range ms {
				x := count[key{v, m, t}]
				cells = append(cells, fmt.Sprintf("%d/%d/%d", x[0], x[1], x[2]))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", t, v, strings.Join(cells, "  "))
		}
	}
	fmt.Fprintln(w)
	return nil
}

// failedCheck is 1 if the run failed the grader's check named check.
func failedCheck(r injectionRun, check string) int {
	for _, f := range r.Failed {
		if strings.HasPrefix(f, check) {
			return 1
		}
	}
	return 0
}
