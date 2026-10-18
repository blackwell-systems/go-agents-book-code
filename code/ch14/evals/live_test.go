package evals

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// liveFiles are chapter 13's live results, in ../testdata/evals, with
// the set each ran and the scored runs each task was meant to get.
var liveFiles = []struct {
	name, set string
	k         int
}{
	{"l1-confirm-base", "confirm", 2},
	{"l1b-confirm-no-crash-site", "confirm", 1},
	{"l2-hard", "hard", 2},
	{"l3-hard-checked", "hard", 2},
}

// TestLive pins what the chapter quotes from its live runs: each
// results file's report and metadata, the comparisons between them,
// the failed checks, and every gate and reviewer event. Regenerate
// with go test ./ch14/evals -run Live -update after new runs.
func TestLive(t *testing.T) {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	src := func() rand.Source { return rand.NewPCG(13, 13) }

	recs := map[string][]Record{}
	for _, f := range liveFiles {
		rs := readRecords(t, filepath.Join("..", "testdata", "evals", f.name+".jsonl"))
		recs[f.name] = rs
		tasks, err := Tasks(f.set)
		if err != nil {
			t.Fatal(err)
		}
		cost := 0.0
		for _, r := range rs {
			cost += r.Cost
		}
		p("== %s: %d records, $%.4f in all", f.name, len(rs), cost)
		for _, line := range metadata(rs) {
			p("%s", line)
		}
		if err := Report(&b, rs, tasks, f.k, src); err != nil {
			t.Fatal(err)
		}
		p("failed checks, per scored run: %s", failedChecks(rs))
		p("")
	}

	version := func(v string) func(Record) bool {
		return func(r Record) bool { return r.Version == v }
	}
	l2base := Scored(recs["l2-hard"], version("base"))
	first, second := byOrder(recs["l1-confirm-base"])
	for _, c := range []struct {
		title string
		a, b  Runs
	}{
		{"L1 the same agent twice: each task's first scored run against its second (confirm)",
			first, second},
		{"L1b without the crash-site sentence, against base (confirm)",
			Scored(recs["l1-confirm-base"], nil), Scored(recs["l1b-confirm-no-crash-site"], nil)},
		{"L2 checked with the first reviewer setup, against base (hard)",
			l2base, Scored(recs["l2-hard"], version("checked"))},
		{"L3 checked with the capped, pinned reviewer, against L2's base (hard)",
			l2base, Scored(recs["l3-hard-checked"], nil)},
	} {
		res := Compare(c.a, c.b, src())
		p("== %s", c.title)
		p("%v", res)
		for _, e := range res.Each {
			p("  %-28s %d/%d -> %d/%d", e.Task, e.A.K, e.A.N, e.B.K, e.B.N)
		}
		p("")
	}

	for _, name := range []string{"l2-hard", "l3-hard-checked"} {
		p("== %s: the gate and the reviewer, run by run (checked)", name)
		var reviews, accepts, rejects, outOfTokens, otherErrs, refusals, refiled int
		for _, r := range recs[name] {
			if r.Version != "checked" {
				continue
			}
			p("%s run %d: %s, %d turns, $%.4f; failed: %s", r.Task, r.Run,
				r.Verdict, r.Turns, r.Cost, cmp.Or(strings.Join(r.Failed, ", "), "none"))
			for _, g := range r.Gate {
				p("  gate: %s", clip(g, 140))
				if strings.HasPrefix(g, "refused") {
					refusals++
				} else {
					refiled++
				}
			}
			for _, v := range r.Review {
				p("  review: %s", clip(v, 140))
				reviews++
				switch {
				case strings.HasPrefix(v, "accept"):
					accepts++
				case strings.HasPrefix(v, "reject"):
					rejects++
				case strings.Contains(v, "ran out of tokens"):
					outOfTokens++
				default:
					otherErrs++
				}
			}
		}
		p("reviews %d: accepted %d, rejected %d, out of tokens %d, other errors %d; "+
			"gate refusals %d, refilings without a reproducer %d",
			reviews, accepts, rejects, outOfTokens, otherErrs, refusals, refiled)
		p("")
	}

	p("== reviewer calls, from the recordings (testdata/evals/reviews.jsonl)")
	calls := readJSONL[reviewCall](t, filepath.Join("..", "testdata", "evals", "reviews.jsonl"))
	for _, f := range []string{"l2-hard", "l3-hard-checked"} {
		finish, providers := map[string]int{}, map[string]bool{}
		var reasoning []int
		n, cost := 0, 0.0
		for _, c := range calls {
			if c.File != f {
				continue
			}
			n++
			cost += c.Cost
			finish[cmp.Or(c.FinishReason, "none")]++
			providers[c.Provider] = true
			reasoning = append(reasoning, c.ReasoningTokens)
		}
		slices.Sort(reasoning)
		var fs []string
		for _, k := range slices.Sorted(maps.Keys(finish)) {
			fs = append(fs, fmt.Sprintf("%s %d", k, finish[k]))
		}
		p("%s: %d calls, $%.4f, %d providers; finish: %s; reasoning tokens median %d, max %d",
			f, n, cost, len(providers), strings.Join(fs, ", "),
			reasoning[len(reasoning)/2], reasoning[len(reasoning)-1])
	}

	got := b.String()
	golden := filepath.Join("testdata", "live.txt")
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
		t.Errorf("live results differ from %s:\n%s", golden, got)
	}
}

// A reviewCall is one HTTP call a reviewer made, summarized from its
// recording.
type reviewCall struct {
	File, Script, Provider, FinishReason string
	CompletionTokens, ReasoningTokens    int
	AnswerBytes                          int
	Cost                                 float64
	Error                                string
}

func readRecords(t *testing.T, file string) []Record {
	return readJSONL[Record](t, file)
}

func readJSONL[T any](t *testing.T, file string) []T {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	for line := range strings.Lines(string(data)) {
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out = append(out, v)
	}
	return out
}

// metadata lists, per version, every value the records' metadata took.
func metadata(rs []Record) []string {
	var out []string
	for _, v := range slices.Sorted(maps.Keys(byVersion(rs))) {
		vals := map[string]map[string]bool{}
		add := func(field, val string) {
			if vals[field] == nil {
				vals[field] = map[string]bool{}
			}
			vals[field][val] = true
		}
		for _, r := range rs {
			if r.Version != v {
				continue
			}
			add("model", r.Model)
			add("provider", r.Provider)
			add("reviewer", cmp.Or(r.Reviewer, "none"))
			add("commit", fmt.Sprintf("%s dirty=%t", r.Commit, r.Dirty))
			add("prompt", r.PromptHash)
			add("date", r.Date)
			add("go", r.GoVersion)
			add("caps", fmt.Sprintf("%d turns, $%.2f", r.MaxTurns, r.MaxCost))
		}
		var fields []string
		for _, f := range []string{"model", "provider", "reviewer", "commit",
			"prompt", "date", "go", "caps"} {
			fields = append(fields, f+" "+strings.Join(slices.Sorted(maps.Keys(vals[f])), "|"))
		}
		out = append(out, v+": "+strings.Join(fields, "; "))
	}
	return out
}

// failedChecks counts, per version, the scored runs that failed each
// check, whatever the group.
func failedChecks(rs []Record) string {
	var parts []string
	for _, v := range slices.Sorted(maps.Keys(byVersion(rs))) {
		n := map[string]int{}
		for _, r := range rs {
			if r.Version != v || r.Verdict == Unscored.String() {
				continue
			}
			seen := map[string]bool{}
			for _, f := range r.Failed {
				check := checkName(f)
				if !seen[check] {
					seen[check] = true
					n[check]++
				}
			}
		}
		var cs []string
		for _, c := range slices.Sorted(maps.Keys(n)) {
			cs = append(cs, fmt.Sprintf("%s %d", c, n[c]))
		}
		parts = append(parts, v+": "+cmp.Or(strings.Join(cs, ", "), "none"))
	}
	return strings.Join(parts, "; ")
}

// checkName takes the group off a failed check, "root cause B".
func checkName(failed string) string {
	for _, c := range []string{"found", "grouping", "status", "duplicate of",
		"root cause", "reproducer", "hidden tests and suite", "one issue per group",
		"forbidden paths", "existing tests kept", "no patch"} {
		if strings.HasPrefix(failed, c) {
			return c
		}
	}
	return strings.TrimSpace(failed)
}

// byOrder splits each task's scored runs into its first and its
// second, by run number, for comparing two runs of one version.
func byOrder(rs []Record) (first, second Runs) {
	first, second = Runs{}, Runs{}
	byTask := map[string][]Record{}
	for _, r := range rs {
		if r.Verdict != Unscored.String() {
			byTask[r.Task] = append(byTask[r.Task], r)
		}
	}
	for task, runs := range byTask {
		slices.SortFunc(runs, func(a, b Record) int { return a.Run - b.Run })
		if len(runs) >= 2 {
			first[task] = []bool{runs[0].Verdict == Pass.String()}
			second[task] = []bool{runs[1].Verdict == Pass.String()}
		}
	}
	return first, second
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
