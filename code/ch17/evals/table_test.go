package evals

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

// TestChapter13Records builds the table from chapter 13's L1 records
// (the book's agent on the confirmation tasks, one model, two runs
// each), read in place, and checks it against chapter 13's numbers.
func TestChapter13Records(t *testing.T) {
	rs, err := Load("../../ch13/testdata/evals/l1-confirm-base.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rows := Summarize(rs, 1)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	r := rows[0]
	cost, passes := 0.0, 0
	for _, rec := range rs {
		cost += rec.Cost
		if rec.Verdict == "pass" {
			passes++
		}
	}
	if r.Runs != 40 || passes != 29 ||
		math.Abs(r.CostPerPass.Mean-cost/29) > 1e-12 {
		t.Errorf("runs %d, cost per pass %v, want 40 runs and $%.6f",
			r.Runs, r.CostPerPass, cost/29)
	}
	// Formatted as chapter 13 prints it: 72.5 rounds to even.
	if got := fmt.Sprintf("%.0f", 100*r.All.Mean); got != "72" ||
		r.All.Tasks != 20 {
		t.Errorf("pass rate %v, want chapter 13's 72%% over 20 tasks",
			r.All)
	}
	if !(r.CostPerPass.Lo < r.CostPerPass.Mean &&
		r.CostPerPass.Mean < r.CostPerPass.Hi) {
		t.Errorf("cost per pass range %v doesn't hold its mean",
			r.CostPerPass)
	}
	var b strings.Builder
	if err := Table(&b, rows, Apart(rs, 1)); err != nil {
		t.Fatal(err)
	}
	golden(t, "testdata/ch13-l1.adoc", b.String())
}

func golden(t *testing.T, file, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run with -update to see\ngot:\n%s", file,
			got)
	}
}

// fake returns n runs of each of the confirmation tasks for a model,
// passing where pass says, at a cost of a cent each.
func fake(model string, runs int,
	pass func(task string, run int) bool) []Record {
	tasks, err := Tasks("confirm")
	if err != nil {
		panic(err)
	}
	var out []Record
	for _, task := range tasks {
		for i := range runs {
			r := Record{}
			r.Model, r.Task, r.Set, r.Run = model, task, "confirm", i+1
			r.Cost, r.Turns, r.Verdict = 0.01, 10+i, "fail"
			if pass(task, i) {
				r.Verdict = "pass"
			}
			out = append(out, r)
		}
	}
	return out
}

func TestApart(t *testing.T) {
	always := func(string, int) bool { return true }
	never := func(string, int) bool { return false }
	half := func(_ string, i int) bool { return i%2 == 0 }
	rs := append(fake("a", 2, always), fake("b", 2, never)...)
	rs = append(rs, fake("c", 2, half)...)
	rs = append(rs, fake("d", 2, half)...)
	got := strings.Join(Apart(rs, 1), "\n")
	for _, want := range []string{
		"`b` against `a`: -100 points", "a passes more",
		"`d` against `c`: 0 points", "can't be told apart",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Apart lacks %q:\n%s", want, got)
		}
	}
}

// TestRowDetails checks the parts the chapter 13 records don't have:
// unscored runs, injection judgments, h1's own line, and the batch.
func TestRowDetails(t *testing.T) {
	rs := fake("m", 3, func(task string, run int) bool {
		return task != h1 || run == 0
	})
	for i := range rs {
		rs[i].Judged = strings.HasPrefix(rs[i].Task, "i")
		switch {
		case rs[i].Task == "i2-encoded" && rs[i].Run == 1:
			rs[i].Attempted, rs[i].Succeeded = true, true
		case rs[i].Task == "i3-split-fields" && rs[i].Run == 1:
			rs[i].Attempted = true
		}
		rs[i].Seconds = 60
		if rs[i].Task == "d2-clock-no-minutes" { // three reviews, one failed
			rs[i].Review = []string{"accept: fine"}
			if rs[i].Run == 1 {
				rs[i].ReviewErrors = 1
			}
		}
	}
	extra := Record{}
	extra.Model, extra.Task, extra.Set = "m", "d2-clock-no-minutes", "confirm"
	extra.Verdict, extra.Cost = "unscored", 0.5
	batch := Record{}
	batch.Model, batch.Set, batch.Task = "m", "batch", "batch"
	batch.Verdict, batch.Cost, batch.Seconds = "pass", 0.3, 400
	old := batch // made before batch records carried reviews
	batch.Model, batch.PromptHash = "n", "abc"
	batch.Review, batch.ReviewErrors = []string{"accept", "error"}, 1
	rows := Summarize(append(rs, extra, old, batch), 1)
	r := rows[0]
	if r.Unscored != 1 || r.Wasted != 0.5 || r.Runs != 60 {
		t.Errorf("unscored %d ($%.2f), runs %d", r.Unscored, r.Wasted,
			r.Runs)
	}
	if r.Injection.Judged != 18 || r.Injection.Attempted != 2 ||
		r.Injection.Succeeded != 1 {
		t.Errorf("injection %+v", r.Injection)
	}
	if r.H1.K != 1 || r.H1.N != 3 || r.WithoutH1.Mean != 1 {
		t.Errorf("h1 %v, without it %v", r.H1, r.WithoutH1)
	}
	if r.Batch.Pass.K != 1 || r.Batch.Cost != 0.3 || r.Seconds != 60 {
		t.Errorf("batch %+v, seconds %v", r.Batch, r.Seconds)
	}
	if r.K != 3 || r.Reviews != 3 || r.ReviewErrors != 1 ||
		math.Abs(r.CostPerRun-0.01) > 1e-12 {
		t.Errorf("k %d, reviews %d (%d failed), cost per run %v", r.K,
			r.Reviews, r.ReviewErrors, r.CostPerRun)
	}
	// h1 passes once in three runs: pass^3 is 0 there, pass@3 is 1.
	if r.Every.Mean != 19.0/20 || r.Once.Mean != 1 {
		t.Errorf("pass^3 %v, pass@3 %v", r.Every, r.Once)
	}
	var b strings.Builder
	if err := Table(&b, rows, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 of 18 succeeded, 2 attempted",
		"|`m` | |1 of 3 |", "All 3 runs pass", "|1 of 3 |60 s",
		"|`m` | |1 of 1 |0 |$0.3000 |not recorded |400 s",
		"|`n` | |1 of 1 |0 |$0.3000 |1 of 2 |400 s"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, b.String())
		}
	}
}

// TestVersions: runs of two versions stay in two rows, unscored batch
// runs and their cost are counted, and a tier with no runs says so.
func TestVersions(t *testing.T) {
	var rs []Record
	for i := range 3 {
		for _, v := range []string{"final", "final-no-history"} {
			r := Record{}
			r.Model, r.Version, r.Task, r.Set = "m", v, h1, "confirm"
			r.Run, r.Cost, r.Verdict = i+1, 0.01, "pass"
			if v == "final-no-history" {
				r.Verdict = "fail"
			}
			rs = append(rs, r)
		}
	}
	for i := range 3 { // a second task, so final has a row of its own
		r := Record{}
		r.Model, r.Version, r.Task, r.Set = "m", "final", "d2-clock-no-minutes", "confirm"
		r.Run, r.Cost, r.Verdict = i+1, 0.01, "pass"
		rs = append(rs, r)
	}
	for _, v := range []string{"unscored", "pass"} {
		r := Record{}
		r.Model, r.Version, r.Task, r.Set = "m", "final", "batch", "batch"
		r.Verdict, r.Cost = v, 0.4
		rs = append(rs, r)
	}
	rows := Summarize(rs, 1)
	if len(rows) != 2 || rows[0].H1.K != 3 || rows[1].H1.K != 0 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Batch.Unscored != 1 || rows[0].Batch.Wasted != 0.4 {
		t.Errorf("batch %+v", rows[0].Batch)
	}
	var b strings.Builder
	if err := Table(&b, rows, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"|`m` |final |3 of 3 |",
		"|`m` |final-no-history |0 of 3 |", "no tasks", "1 ($0.4000)",
		"|`m` |100% over 2 tasks"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "NaN") {
		t.Errorf("NaN in the table:\n%s", b.String())
	}
}

// TestChapter17Records builds chapter 17's results table from its live
// records (testdata/evals/ch17-*.jsonl in the chapter's code): the
// confirmation runs, h1 without the history tools, and the batch. The
// comparisons use the confirmation runs only. Run with -update after
// new records.
func TestChapter17Records(t *testing.T) {
	dir := "../testdata/evals/"
	var confirm []string
	for _, m := range []string{"haiku", "luna", "deepseek"} {
		confirm = append(confirm, dir+"ch17-t1-"+m+".jsonl")
	}
	t1, err := Load(confirm...)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := Load(dir+"ch17-t1-h1-haiku.jsonl",
		dir+"ch17-t1-h1-luna.jsonl", dir+"ch17-t1-h1-deepseek.jsonl",
		dir+"ch17-t2-haiku.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	rows := Summarize(append(t1, rest...), 1)
	if err := Table(&b, rows, Apart(t1, 1)); err != nil {
		t.Fatal(err)
	}
	golden(t, "testdata/results.adoc", b.String())
}

// TestSharedK: rows with three and two runs a task are compared at
// k = 2, the fewest any row has.
func TestSharedK(t *testing.T) {
	always := func(string, int) bool { return true }
	rows := Summarize(append(fake("a", 3, always), fake("b", 2, always)...), 1)
	for _, r := range rows {
		if r.K != 2 || r.Every.Mean != 1 || r.Every.Tasks != 20 {
			t.Errorf("%s: k %d, pass^k %v", r.Model, r.K, r.Every)
		}
	}
}
