package evals

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/reanalysis.txt")

// sweepRun is one run from the prototype sweeps or chapter 8's quality
// check, with only the fields the reanalysis reads.
type sweepRun struct {
	Model   string   `json:"model"`
	Variant string   `json:"variant"` // chapter 8's quality check
	Task    string   `json:"task"`
	Passed  bool     `json:"passed"`
	Stop    string   `json:"stop"`
	Turns   int      `json:"turns"`
	Failed  []string `json:"failed"`
	Reviews []struct {
		Reviewer    string `json:"reviewer"`
		Verdict     string `json:"verdict"`
		RootCauseOK bool   `json:"root_cause_ok"`
	} `json:"reviews"`
}

func readRuns(t *testing.T, file string) []sweepRun {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []sweepRun
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r sweepRun
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// group collects runs into Runs keyed by key(run), keeping only runs
// keep accepts.
func group(rs []sweepRun, key func(sweepRun) string, keep func(sweepRun) bool) Runs {
	out := Runs{}
	for _, r := range rs {
		if keep(r) {
			out[key(r)] = append(out[key(r)], r.Passed)
		}
	}
	return out
}

func all(sweepRun) bool { return true }

func byTaskKey(r sweepRun) string { return r.Task }

func short(model string) string {
	_, name, _ := strings.Cut(model, "/")
	return name
}

// TestReanalysis reads the runs the book already has, from the
// prototype sweeps and chapter 8's quality check, and writes the
// numbers chapter 13 quotes. Ranges come from resampling tasks, never
// runs, and only over MinTasks tasks or more. Nothing here calls a
// model. Run with -update to rewrite the golden file after a
// deliberate change.
func TestReanalysis(t *testing.T) {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	src := func() rand.Source { return rand.NewPCG(13, 13) }

	v2 := readRuns(t, "testdata/sweeps/v2.jsonl")
	models := slices.Sorted(maps.Keys(group(v2, func(r sweepRun) string { return r.Model }, all)))
	model := func(m string) func(sweepRun) bool {
		return func(r sweepRun) bool { return m == "" || r.Model == m }
	}
	tier := func(tt Tier) func(sweepRun) bool {
		return func(r sweepRun) bool { x, _ := TierOf(r.Task); return x == tt }
	}

	p("== A1 pass rates: the v2 sweep, 29 tasks (no m7, m8, m9), three runs each")
	p("Pooled ranges treat runs as independent and are too narrow; quote the task-level ones.")
	for _, m := range append(slices.Clone(models), "") {
		rs := group(v2, byTaskKey, model(m))
		name := cmpOr(short(m), "all three models")
		p("%-22s pooled %v; task-level %v", name, rs.Rate(), rs.PassAt(1, src()))
	}
	for _, tt := range []Tier{Floor, Hard} {
		rs := group(v2, byTaskKey, tier(tt))
		p("%-22s pooled %v; task-level %v", "tier "+string(tt), rs.Rate(), rs.PassAt(1, src()))
	}

	p("\n== A2 pass@k and pass^k (unbiased, Chen et al. 2021), per model, k = 1 to 3")
	for _, m := range models {
		rs := group(v2, byTaskKey, model(m))
		var line []string
		for k := 1; k <= 3; k++ {
			line = append(line, fmt.Sprintf("pass^%d %.0f%%", k, 100*rs.PassAll(k, src()).Mean))
		}
		for k := 2; k <= 3; k++ {
			line = append(line, fmt.Sprintf("pass@%d %.0f%%", k, 100*rs.PassAt(k, src()).Mean))
		}
		p("%-22s %s", short(m), strings.Join(line, ", "))
	}
	for _, m := range models {
		rs := group(v2, byTaskKey, model(m))
		p("%-22s tasks passed every time %d of 29, at least once %d of 29",
			short(m), count(rs, func(c, n int) bool { return c == n }),
			count(rs, func(c, _ int) bool { return c > 0 }))
	}

	p("\n== A3 paired comparisons, task by task (ranges resample tasks; none below %d tasks)", MinTasks)
	plain := readRuns(t, "testdata/sweeps/plain.jsonl")
	p("plain prompt           task-level %v", group(plain, byTaskKey, all).PassAt(1, src()))
	p("v2 prompt vs plain     %v", Compare(group(plain, byTaskKey, all), group(v2, byTaskKey, all), src()))
	for _, m := range models[1:] {
		p("%-22s %v", short(m)+" vs "+short(models[0]),
			Compare(group(v2, byTaskKey, model(models[0])), group(v2, byTaskKey, model(m)), src()))
	}
	q := readRuns(t, filepath.Join("..", "..", "evidence", "testdata", "quality", "haiku.jsonl"))
	version := func(v string) Runs {
		return group(q, byTaskKey, func(r sweepRun) bool { return r.Variant == v })
	}
	base := version("v1")
	for _, v := range []string{"duplicates", "old-versions", "crash-site", "all"} {
		c := Compare(base, version(v), src())
		p("ch8 %-18s %v", v+" vs base", c)
		var each []string
		for _, pr := range c.Each {
			pv := fisherLower(pr.A.K, pr.A.N, pr.B.K, pr.B.N)
			each = append(each, fmt.Sprintf("%s %d/%d to %d/%d (p %.3f)",
				strings.SplitN(pr.Task, "-", 2)[0], pr.A.K, pr.A.N, pr.B.K, pr.B.N, pv))
		}
		p("    %s", strings.Join(each, "; "))
	}

	p("\n== A4 unscored: the repeat runs that hit a 402")
	variance := readRuns(t, "testdata/sweeps/variance.jsonl")
	var asFailed, scored Rate
	stopped, first := 0, 0
	cells := map[string]bool{}
	for _, r := range variance {
		asFailed.N++
		if r.Passed {
			asFailed.K++
		}
		if strings.Contains(r.Stop, "status 402") {
			stopped++
			if r.Turns <= 1 {
				first++
			}
			continue
		}
		scored.N++
		cells[r.Model+" "+r.Task] = true
		if r.Passed {
			scored.K++
		}
	}
	p("runs %d; stopped by a 402 %d, %d of them on their first request", len(variance), stopped, first)
	p("counted as failed      %v", asFailed)
	p("unscored left out      %v, covering %d of 12 model-task pairs", scored, len(cells))

	p("\n== A5 per-task passes in v2, by tier, and failures per fingerprint group")
	for _, tt := range []Tier{Floor, Hard} {
		rs := group(v2, byTaskKey, tier(tt))
		var line []string
		for _, task := range slices.Sorted(maps.Keys(rs)) {
			line = append(line, fmt.Sprintf("%s %d/%d", task, passes(rs[task]), len(rs[task])))
		}
		p("%s: %s", tt, strings.Join(line, ", "))
		failedOnce := count(rs, func(c, n int) bool { return c < n })
		p("%s tasks failed at least once: %d of %d", tt, failedOnce, len(rs))
	}
	rootCause, dupGroups, dupRuns := 0, 0, 0
	for _, r := range v2 {
		dup := map[string]bool{}
		for _, f := range r.Failed {
			fs := strings.Fields(f)
			label := fs[len(fs)-1]
			switch {
			case strings.HasPrefix(f, "root cause"):
				rootCause++
			case strings.HasPrefix(f, "status ") || strings.HasPrefix(f, "duplicate of "):
				dup[label] = true
			}
		}
		dupGroups += len(dup)
		if len(dup) > 0 {
			dupRuns++
		}
	}
	p("groups with a wrong root cause %d; groups with a wrong status or duplicate number %d, in %d runs",
		rootCause, dupGroups, dupRuns)

	p("\n== A6 the reviewer: another family's model reviewing each fingerprint group's diagnosis")
	review := readRuns(t, "testdata/sweeps/review.jsonl")
	type tally struct{ caught, wrong, rejected, right, unparsed, n int }
	byPair := map[[2]string]*tally{}
	reviews, parsed := 0, 0
	for _, r := range review {
		for _, v := range r.Reviews {
			key := [2]string{v.Reviewer, r.Model}
			tl := byPair[key]
			if tl == nil {
				tl = &tally{}
				byPair[key] = tl
			}
			tl.n++
			reviews++
			switch {
			case v.Verdict != "accept" && v.Verdict != "reject":
				tl.unparsed++
				continue
			case v.RootCauseOK:
				tl.right++
				if v.Verdict == "reject" {
					tl.rejected++
				}
			default:
				tl.wrong++
				if v.Verdict == "reject" {
					tl.caught++
				}
			}
			parsed++
		}
	}
	p("runs %d, crash-group reviews %d, parsed %d", len(review), reviews, parsed)
	for _, key := range slices.SortedFunc(maps.Keys(byPair), func(a, b [2]string) int {
		return strings.Compare(a[0]+a[1], b[0]+b[1])
	}) {
		tl := byPair[key]
		p("%-22s reviewing %-22s caught %d of %d wrong; rejected %d of %d right; unparsed %d",
			short(key[0]), short(key[1]), tl.caught, tl.wrong, tl.rejected, tl.right, tl.unparsed)
	}

	got := b.String()
	golden := filepath.Join("testdata", "reanalysis.txt")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run with -update", err)
	}
	if got != string(want) {
		t.Errorf("reanalysis differs from %s:\n%s", golden, got)
	}
}

// count returns how many tasks' (passes, runs) satisfy ok.
func count(rs Runs, ok func(c, n int) bool) int {
	k := 0
	for _, runs := range rs {
		if ok(passes(runs), len(runs)) {
			k++
		}
	}
	return k
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
