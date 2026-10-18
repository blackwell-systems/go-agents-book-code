package evals

// This file turns the eval records into the results table chapter 17
// includes: one row per model, led by cost per pass and turns, then
// pass rates with their ranges, injection counts, and unscored runs;
// then the h1 line, and which models these runs can and can't tell
// apart. The table is generated, never typed.

import (
	"bufio"
	"cmp"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
)

// h1 is the task the history tool was added for; the table reports it
// on its own line, so a recommendation doesn't rest on it.
const h1 = "h1-paging-commit"

// Load reads records from JSON Lines files, in order.
func Load(files ...string) ([]Record, error) {
	var out []Record
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("load records: %w", err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var r Record
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				f.Close()
				return nil, fmt.Errorf("load %s: %w", name, err)
			}
			out = append(out, r)
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
	}
	return out, nil
}

// A Value is a number with its 95% range from resampling tasks; Lo and
// Hi are NaN when there are too few tasks for a range.
type Value struct {
	Mean, Lo, Hi float64
}

// A Row is one model's results.
type Row struct {
	Model    string
	Version  string  // the agent version, such as final
	Runs     int     // scored single-task runs
	Unscored int     // single-task runs that couldn't be judged
	Wasted   float64 // what the unscored runs cost

	CostPerPass Value   // every scored run's cost over the passes
	CostPerRun  float64 // mean over the scored runs
	Turns       [3]float64
	Seconds     float64 // median; NaN if not recorded

	All, Floor, Hard Estimate // pass fraction over tasks
	WithoutH1        Estimate
	H1               Rate

	// K is the k every compared row shares, the fewest scored runs of
	// any task in any of them; Every and Once are pass^K and pass@K.
	K           int
	Every, Once Estimate

	// Reviews counts the reviewer's verdicts on the scored runs, and
	// ReviewErrors the reviews that failed, which let the diagnosis
	// through unreviewed.
	Reviews, ReviewErrors int

	// Injection counts the injection tasks' runs that chapter 14's
	// judge looked at, and what it found.
	Injection struct{ Judged, Attempted, Succeeded int }

	Batch struct {
		Pass     Rate
		Unscored int     // batch runs that couldn't be judged
		Wasted   float64 // what they cost
		Cost     float64 // mean per scored run
		Seconds  float64 // median

		// Reviews and ReviewErrors as for single tasks; Recorded is
		// false for batch records made before they carried reviews.
		Reviews, ReviewErrors int
		Recorded              bool
	}
}

// tag::summarize[]

// Summarize computes each model's row from the records, sorted by
// model. Ranges come from resampling tasks with a fixed seed, so the
// table is the same each time it is generated.
func Summarize(rs []Record, seed uint64) []Row {
	type key struct{ model, version string }
	byKey := map[key][]Record{}
	for _, r := range rs {
		k := key{r.Model, r.Version}
		byKey[k] = append(byKey[k], r)
	}
	keys := slices.SortedFunc(maps.Keys(byKey), func(a, b key) int {
		return cmp.Or(cmp.Compare(a.model, b.model),
			cmp.Compare(a.version, b.version))
	})
	var rows []Row
	for _, k := range keys {
		row := summarize(k.model, byKey[k], seed)
		row.Version = k.version
		rows = append(rows, row)
	}
	return shareK(rows, byKey, seed)
}

// end::summarize[]

func summarize(model string, rs []Record, seed uint64) Row {
	row := Row{Model: model}
	src := func() rand.Source { return rand.NewPCG(seed, seed) }
	var single []Record
	var turns, secs, batchSecs []float64
	for _, r := range rs {
		switch {
		case r.Set == "batch":
			if r.Verdict == Unscored.String() {
				row.Batch.Unscored++
				row.Batch.Wasted += r.Cost
				continue
			}
			row.Batch.Pass.N++
			if r.Verdict == Pass.String() {
				row.Batch.Pass.K++
			}
			row.Batch.Cost += r.Cost
			batchSecs = append(batchSecs, r.Seconds)
			row.Batch.Reviews += len(r.Review)
			row.Batch.ReviewErrors += r.ReviewErrors
			row.Batch.Recorded = row.Batch.Recorded || r.PromptHash != ""
		case r.Verdict == Unscored.String():
			row.Unscored++
			row.Wasted += r.Cost
		default:
			single = append(single, r)
			turns = append(turns, float64(r.Turns))
			secs = append(secs, r.Seconds)
			row.CostPerRun += r.Cost
			row.Reviews += len(r.Review)
			row.ReviewErrors += r.ReviewErrors
		}
	}
	if len(single) > 0 {
		row.CostPerRun /= float64(len(single))
	}
	if row.Batch.Pass.N > 0 {
		row.Batch.Cost /= float64(row.Batch.Pass.N)
	}
	row.Batch.Seconds = median(batchSecs)
	row.Runs = len(single)
	row.Turns = [3]float64{quantile(turns, .25), median(turns),
		quantile(turns, .75)}
	row.Seconds = median(secs)
	if slices.Max(append(secs, 0)) == 0 {
		row.Seconds = math.NaN() // the records have no times
	}
	runs := scored(single, nil)
	row.All = runs.PassAt(1, src())
	row.Floor = scored(single, tier(Floor)).PassAt(1, src())
	row.Hard = scored(single, tier(Hard)).PassAt(1, src())
	row.WithoutH1 = scored(single, func(r Record) bool {
		return r.Task != h1
	}).PassAt(1, src())
	row.H1 = rateOf(runs[h1])
	row.K = fewest(runs) // Summarize sets the k all rows share
	row.CostPerPass = costPerPass(single, src())
	for _, r := range single {
		if injection(r.Task) && r.Judged {
			row.Injection.Judged++
			if r.Attempted {
				row.Injection.Attempted++
			}
			if r.Succeeded {
				row.Injection.Succeeded++
			}
		}
	}
	return row
}

func tier(t Tier) func(Record) bool {
	return func(r Record) bool {
		got, ok := TierOf(r.Task)
		return ok && got == t
	}
}

// shareK estimates every row's pass^k and pass@k with the same k, the
// fewest scored runs any row has of any task, so the rows compare; the
// estimators are unbiased for a task with more runs than k.
func shareK[K comparable](rows []Row, byKey map[K][]Record,
	seed uint64) []Row {
	k := 0 // over the rows the tables compare, as Table chooses them
	for _, r := range rows {
		if r.Runs > 0 && r.All.Tasks >= 2 && (k == 0 || r.K < k) {
			k = r.K
		}
	}
	k = max(k, 1)
	for _, rs := range byKey {
		single := slices.DeleteFunc(slices.Clone(rs), func(r Record) bool {
			return r.Set == "batch" || r.Verdict == Unscored.String()
		})
		if len(single) == 0 {
			continue
		}
		i := slices.IndexFunc(rows, func(r Row) bool {
			return r.Model == single[0].Model && r.Version == single[0].Version
		})
		runs := scored(single, nil)
		rows[i].K = k
		rows[i].Every = runs.PassAll(k, rand.NewPCG(seed, seed))
		rows[i].Once = runs.PassAt(k, rand.NewPCG(seed, seed))
	}
	return rows
}

// fewest returns the fewest scored runs of any task, at least 1.
func fewest(runs Runs) int {
	k := 0
	for _, rs := range runs {
		if k == 0 || len(rs) < k {
			k = len(rs)
		}
	}
	return max(k, 1)
}

// rateOf counts the passes in one task's runs.
func rateOf(runs []bool) Rate {
	r := Rate{N: len(runs)}
	for _, ok := range runs {
		if ok {
			r.K++
		}
	}
	return r
}

// scored groups scored single-task runs by task, as chapter 13's
// Scored does, keeping those keep accepts.
func scored(rs []Record, keep func(Record) bool) Runs {
	out := Runs{}
	for _, r := range rs {
		if keep == nil || keep(r) {
			out[r.Task] = append(out[r.Task],
				r.Verdict == Pass.String())
		}
	}
	return out
}

// tag::cost-per-pass[]

// costPerPass is the cost of every scored run divided by the passes, so
// a model that fails often pays for its failures, with a range from
// resampling tasks: each resample adds up the chosen tasks' costs and
// passes, and a resample with no pass costs infinitely much per pass.
func costPerPass(rs []Record, src rand.Source) Value {
	type tally struct {
		cost   float64
		passes int
	}
	byTask := map[string]*tally{}
	for _, r := range rs {
		t := byTask[r.Task]
		if t == nil {
			t = &tally{}
			byTask[r.Task] = t
		}
		t.cost += r.Cost
		if r.Verdict == Pass.String() {
			t.passes++
		}
	}
	tasks := slices.Sorted(maps.Keys(byTask))
	ratio := func(pick func(i int) *tally) float64 {
		cost, passes := 0.0, 0
		for i := range tasks {
			t := pick(i)
			cost += t.cost
			passes += t.passes
		}
		if passes == 0 {
			return math.Inf(1)
		}
		return cost / float64(passes)
	}
	v := Value{Mean: ratio(func(i int) *tally { return byTask[tasks[i]] }),
		Lo: math.NaN(), Hi: math.NaN()}
	if len(tasks) < MinTasks {
		return v
	}
	r := rand.New(src)
	const resamples = 10000
	xs := make([]float64, resamples)
	for i := range xs {
		xs[i] = ratio(func(int) *tally {
			return byTask[tasks[r.IntN(len(tasks))]]
		})
	}
	slices.Sort(xs)
	v.Lo, v.Hi = xs[resamples*25/1000], xs[resamples*975/1000-1]
	return v
}

// end::cost-per-pass[]

func median(xs []float64) float64 { return quantile(xs, .5) }

// quantile returns the q-quantile of xs by linear interpolation, or NaN
// for no values.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Sorted(slices.Values(xs))
	pos := q * float64(len(s)-1)
	i := int(pos)
	if i+1 >= len(s) {
		return s[len(s)-1]
	}
	return s[i] + (pos-float64(i))*(s[i+1]-s[i])
}

// tag::apart[]

// Apart compares every pair of models task by task on the tasks both
// ran (chapter 13's Compare) and says, for each, whether these runs
// tell them apart: only when the range of the difference leaves out
// zero.
func Apart(rs []Record, seed uint64) []string {
	byModel := map[string]Runs{}
	for _, r := range rs {
		if r.Set == "batch" || r.Verdict == Unscored.String() {
			continue
		}
		label := strings.TrimSpace(r.Model + " " + r.Version)
		if byModel[label] == nil {
			byModel[label] = Runs{}
		}
		byModel[label][r.Task] = append(byModel[label][r.Task],
			r.Verdict == Pass.String())
	}
	models := slices.Sorted(maps.Keys(byModel))
	var out []string
	for i, a := range models {
		for _, b := range models[i+1:] {
			c := Compare(byModel[a], byModel[b],
				rand.NewPCG(seed, seed))
			verdict := "can't be told apart"
			switch {
			case math.IsNaN(c.Lo):
				verdict = "too few shared tasks for a range"
			case c.Lo > 0:
				verdict = b + " passes more"
			case c.Hi < 0:
				verdict = a + " passes more"
			}
			out = append(out, fmt.Sprintf("`%s` against `%s`: %s, %s",
				b, a, c, verdict))
		}
	}
	return out
}

// end::apart[]

// tag::table[]

// Table writes the rows as two AsciiDoc tables, pass rates and then
// cost and effort, then the h1 table, the batch table, and the
// comparisons, ready to include in the chapter. A version run on one
// task, such as h1 without the history tools, appears only in the h1
// table.
func Table(w io.Writer, rows []Row, apart []string) error {
	var b strings.Builder
	compared := slices.DeleteFunc(slices.Clone(rows), func(r Row) bool {
		return r.Runs+r.Unscored == 0 || r.All.Tasks < 2
	})
	k := 0 // the k every row shares, or 0
	for i, r := range compared {
		if i == 0 || r.K == k {
			k = r.K
			continue
		}
		k = 0
		break
	}
	every, once := "Every run passes", "At least one passes"
	if k > 0 {
		every = fmt.Sprintf("All %d runs pass", k)
		once = fmt.Sprintf("At least one of %d passes", k)
	}
	fmt.Fprintf(&b, "[%%header,cols=\"3,3,3,2,3,3,3\"]\n|===\n"+
		"|Model |Pass rate |Floor |Hard |%s |%s |Injection\n", every, once)
	for _, r := range compared {
		fmt.Fprintf(&b, "|%s |%s |%s |%s |%s |%s |%s\n", label(r),
			estimate(r.All), estimate(r.Floor), estimate(r.Hard),
			passK(r.Every, r.K, k), passK(r.Once, r.K, k), injectionText(r))
	}
	b.WriteString("|===\n\n[%header,cols=\"3,2,3,2,2,2,1\"]\n|===\n" +
		"|Model |Cost per scored run |Cost per pass |Turns |Unscored " +
		"|Review errors |Time\n")
	for _, r := range compared {
		fmt.Fprintf(&b, "|%s |$%.4f |%s |%s |%s |%s |%s\n", label(r),
			r.CostPerRun, dollars(r.CostPerPass), turns(r.Turns),
			unscored(r.Unscored, r.Wasted),
			reviews(r.Reviews, r.ReviewErrors, true), seconds(r.Seconds))
	}
	b.WriteString("|===\n")
	if slices.ContainsFunc(rows, func(r Row) bool { return r.H1.N > 0 }) {
		b.WriteString("\n[%header,cols=\"3,2,2,4\"]\n|===\n" +
			"|Model |Version |`" + h1 + "` |Confirmation tasks without it\n")
		for _, r := range rows {
			if r.H1.N > 0 {
				fmt.Fprintf(&b, "|`%s` |%s |%d of %d |%s\n", r.Model,
					r.Version, r.H1.K, r.H1.N, estimate(r.WithoutH1))
			}
		}
		b.WriteString("|===\n")
	}
	if slices.ContainsFunc(rows, func(r Row) bool {
		return r.Batch.Pass.N+r.Batch.Unscored > 0
	}) {
		b.WriteString("\n[%header,cols=\"3,2,2,2,2,2,2\"]\n|===\n" +
			"|Model |Version |Batch passed |Unscored |Cost per run " +
			"|Review errors |Time\n")
		for _, r := range rows {
			if r.Batch.Pass.N+r.Batch.Unscored == 0 {
				continue
			}
			fmt.Fprintf(&b, "|`%s` |%s |%d of %d |%s |$%.4f |%s |%s\n",
				r.Model, r.Version, r.Batch.Pass.K, r.Batch.Pass.N,
				unscored(r.Batch.Unscored, r.Batch.Wasted), r.Batch.Cost,
				reviews(r.Batch.Reviews, r.Batch.ReviewErrors,
					r.Batch.Recorded), seconds(r.Batch.Seconds))
		}
		b.WriteString("|===\n")
	}
	for _, s := range apart {
		b.WriteString("\n* " + s + "\n")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}

// end::table[]

// label is a row's model, with its version unless it is the
// finished agent's.
func label(r Row) string {
	if r.Version == "" || r.Version == "final" {
		return "`" + r.Model + "`"
	}
	return "`" + r.Model + "` (" + r.Version + ")"
}

// passK is pass^k or pass@k as a percentage with its range, with k
// named when the rows don't share one.
func passK(e Estimate, k, shared int) string {
	if e.Tasks == 0 {
		return "no tasks"
	}
	s := pct(e.Mean)
	if !math.IsNaN(e.Lo) {
		s = fmt.Sprintf("%.0f%% (%.0f to %.0f%%)", 100*e.Mean, 100*e.Lo,
			100*e.Hi)
	}
	if shared == 0 {
		s += fmt.Sprintf(", k = %d", k)
	}
	return s
}

// reviews is how many of the reviewer's verdicts failed.
func reviews(n, failed int, recorded bool) string {
	switch {
	case !recorded:
		return "not recorded"
	case n == 0:
		return "no reviews"
	}
	return fmt.Sprintf("%d of %d", failed, n)
}

// estimate is a pass rate over tasks, or "no tasks" for a tier the
// runs didn't reach.
func estimate(e Estimate) string {
	if e.Tasks == 0 {
		return "no tasks"
	}
	return e.String()
}

// unscored is a count of runs that couldn't be judged, with what they
// cost.
func unscored(n int, cost float64) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("%d ($%.4f)", n, cost)
}

func dollars(v Value) string {
	if math.IsInf(v.Mean, 1) {
		return "no passes"
	}
	if math.IsNaN(v.Lo) {
		return fmt.Sprintf("$%.4f", v.Mean)
	}
	return fmt.Sprintf("$%.4f ($%.4f to %s)", v.Mean, v.Lo, upper(v.Hi))
}

func upper(x float64) string {
	if math.IsInf(x, 1) {
		return "no bound"
	}
	return fmt.Sprintf("$%.4f", x)
}

func turns(t [3]float64) string {
	if math.IsNaN(t[1]) {
		return "none"
	}
	return fmt.Sprintf("%.0f (%.0f to %.0f)", t[1], t[0], t[2])
}

func injectionText(r Row) string {
	if r.Injection.Judged == 0 {
		return "not judged"
	}
	return fmt.Sprintf("%d of %d succeeded, %d attempted",
		r.Injection.Succeeded, r.Injection.Judged, r.Injection.Attempted)
}

func seconds(s float64) string {
	if math.IsNaN(s) {
		return "not recorded"
	}
	return fmt.Sprintf("%.0f s", cmp.Or(math.Round(s), 1))
}
