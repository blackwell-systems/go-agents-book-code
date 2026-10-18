package evals

import (
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
)

// tag::rate[]

// A Rate is k passes out of n scored runs.
type Rate struct{ K, N int }

// Range returns the 95% Wilson score interval around the rate: the
// pass rates that k of n is consistent with. Unlike the familiar
// p ± 1.96·sqrt(p(1-p)/n), it stays inside 0 and 1 and keeps a width
// when k is 0 or n, which small eval sets hit often.
func (r Rate) Range() (lo, hi float64) {
	if r.K < 0 || r.K > r.N {
		panic(fmt.Sprintf("evals: rate %d of %d", r.K, r.N))
	}
	if r.N == 0 {
		return 0, 1
	}
	const z = 1.96 // <1>
	n, p := float64(r.N), float64(r.K)/float64(r.N)
	mid := (p + z*z/(2*n)) / (1 + z*z/n) // <2>
	half := z / (1 + z*z/n) * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	lo, hi = max(0, mid-half), min(1, mid+half)
	if r.K == 0 {
		lo = 0
	}
	if r.K == r.N {
		hi = 1
	}
	return lo, hi
}

// end::rate[]

func (r Rate) String() string {
	if r.N == 0 {
		return "0 of 0"
	}
	lo, hi := r.Range()
	return fmt.Sprintf("%d of %d (%.0f%%, %.0f to %.0f%%)", r.K, r.N,
		100*float64(r.K)/float64(r.N), 100*lo, 100*hi)
}

// Runs holds a version's results task by task: for each task, whether
// each of its scored runs passed. Unscored runs are left out.
type Runs map[string][]bool

// Rate pools every run of every task. Runs of one task are more alike
// than runs of different tasks, so the pooled range is too narrow;
// Estimate's range, which resamples tasks, is the one to quote.
func (rs Runs) Rate() Rate {
	var r Rate
	for _, runs := range rs {
		r.N += len(runs)
		r.K += passes(runs)
	}
	return r
}

// MinTasks is the fewest tasks a resampled range is reported for.
// Below it, resampling tasks gives ranges far narrower than they
// should be (with five tasks, a 95% range holds the true value about
// two times in three), so a comparison reports its counts and its
// per-task results instead.
const MinTasks = 12

// An Estimate is a value averaged over tasks, with its 95% range from
// resampling the tasks; Lo and Hi are NaN when there are fewer than
// MinTasks tasks.
type Estimate struct {
	Tasks        int
	Mean, Lo, Hi float64
}

// tag::pass-at-k[]

// PassAt estimates pass@k, the chance that at least one of k runs of a
// task passes, and PassAll estimates pass^k, the chance that all k do,
// each averaged over the tasks with at least k scored runs. For a task
// with c passes in n runs they use the unbiased estimators: pass@k is
// 1 - C(n-c,k)/C(n,k), and pass^k is C(c,k)/C(n,k). With k = 1 both
// are the task's pass fraction.
func (rs Runs) PassAt(k int, src rand.Source) Estimate {
	return rs.estimate(k, src, func(n, c int) float64 {
		return 1 - choose(n-c, k)/choose(n, k)
	})
}

// PassAll estimates pass^k.
func (rs Runs) PassAll(k int, src rand.Source) Estimate {
	return rs.estimate(k, src, func(n, c int) float64 {
		return choose(c, k) / choose(n, k)
	})
}

// end::pass-at-k[]

// estimate applies f to each task with at least k scored runs and
// averages it over tasks, with a range from resampling the tasks.
func (rs Runs) estimate(k int, src rand.Source,
	f func(n, c int) float64) Estimate {
	var xs []float64
	for _, task := range slices.Sorted(maps.Keys(rs)) {
		if runs := rs[task]; len(runs) >= k {
			xs = append(xs, f(len(runs), passes(runs)))
		}
	}
	return over(xs, src)
}

// over averages xs, one value per task, with a resampled range when
// there are enough tasks.
func over(xs []float64, src rand.Source) Estimate {
	e := Estimate{Tasks: len(xs), Lo: math.NaN(), Hi: math.NaN()}
	if len(xs) == 0 {
		e.Mean = math.NaN()
		return e
	}
	e.Mean = mean(xs)
	if len(xs) >= MinTasks {
		e.Lo, e.Hi = bootstrap(xs, 10000, rand.New(src))
	}
	return e
}

// choose returns the binomial coefficient C(n, k) as a float, and 0
// when k > n.
func choose(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	c := 1.0
	for i := range k {
		c = c * float64(n-i) / float64(i+1)
	}
	return c
}

func (e Estimate) String() string {
	if math.IsNaN(e.Lo) {
		return fmt.Sprintf("%.0f%% over %d tasks", 100*e.Mean, e.Tasks)
	}
	return fmt.Sprintf("%.0f%% (%.0f to %.0f%%) over %d tasks",
		100*e.Mean, 100*e.Lo, 100*e.Hi, e.Tasks)
}

func passes(runs []bool) int {
	k := 0
	for _, ok := range runs {
		if ok {
			k++
		}
	}
	return k
}

// share is the fraction of a task's runs that passed.
func share(runs []bool) float64 {
	return float64(passes(runs)) / float64(len(runs))
}

// tag::comparison[]

// A Comparison is version b against version a on the tasks both ran.
type Comparison struct {
	Tasks               int
	Better, Worse, Tied int     // b passed more, less, the same
	Diff                float64 // mean of b's pass fraction minus a's
	Lo, Hi              float64 // 95% range; NaN below MinTasks tasks
	Each                []Pair  // every task's two results
}

// A Pair is one task's results under the two versions.
type Pair struct {
	Task string
	A, B Rate
}

// end::comparison[]

// tag::compare[]

// Compare pairs two versions task by task. Each task contributes the
// difference in its pass fraction, so a task both versions find easy
// or both find impossible adds nothing. The range comes from
// resampling the tasks with replacement, since the tasks, not the
// runs, are what a small eval set has too few of; below MinTasks tasks
// there is no range, only the counts and each task's results.
func Compare(a, b Runs, src rand.Source) Comparison {
	var c Comparison
	var diffs []float64
	for _, task := range slices.Sorted(maps.Keys(a)) {
		ra, rb := a[task], b[task]
		if len(ra) > 0 && len(rb) > 0 {
			diffs = append(diffs, c.add(share(rb)-share(ra)))
			c.Each = append(c.Each, Pair{task,
				Rate{passes(ra), len(ra)}, Rate{passes(rb), len(rb)}})
		}
	}
	e := over(diffs, src) // <1>
	c.Diff, c.Lo, c.Hi = e.Mean, e.Lo, e.Hi
	return c
}

// end::compare[]

// add counts one task's difference and returns it.
func (c *Comparison) add(d float64) float64 {
	c.Tasks++
	switch {
	case d > 0:
		c.Better++
	case d < 0:
		c.Worse++
	default:
		c.Tied++
	}
	return d
}

// bootstrap returns the 2.5th and 97.5th percentiles of the mean of xs
// over resamples drawn with replacement; fewer than 1,000 resamples are
// raised to 1,000, so the percentiles are more than a handful of draws.
func bootstrap(xs []float64, resamples int, r *rand.Rand) (lo, hi float64) {
	resamples = max(resamples, 1000)
	means := make([]float64, resamples)
	sample := make([]float64, len(xs))
	for i := range means {
		for j := range sample {
			sample[j] = xs[r.IntN(len(xs))]
		}
		means[i] = mean(sample)
	}
	slices.Sort(means)
	return means[resamples*25/1000], means[resamples*975/1000-1]
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func (c Comparison) String() string {
	counts := fmt.Sprintf("over %d tasks: better on %d, worse on %d, "+
		"tied on %d", c.Tasks, c.Better, c.Worse, c.Tied)
	if math.IsNaN(c.Lo) {
		return fmt.Sprintf("%s points, no range %s", points(c.Diff), counts)
	}
	return fmt.Sprintf("%s points (%s to %s) %s", points(c.Diff),
		points(c.Lo), points(c.Hi), counts)
}

// points formats a difference in pass fraction as signed percentage
// points, with no "-0".
func points(x float64) string {
	r := math.Round(100 * x)
	if r == 0 {
		return "0"
	}
	return fmt.Sprintf("%+.0f", r)
}

// tag::drops[]

// Drops returns the tasks whose results now are worse than their
// history by more than chance explains: a one-sided Fisher exact test
// at the given level, comparing each task's passes now with its passes
// before. A floor task failing once is not news; a floor task that
// used to pass every time and now fails most runs is.
func Drops(history, now Runs, level float64) []string {
	var out []string
	for _, task := range slices.Sorted(maps.Keys(now)) {
		h, n := history[task], now[task]
		if len(h) == 0 || len(n) == 0 {
			continue
		}
		p := fisherLower(passes(n), len(n), passes(h), len(h))
		if p < level {
			out = append(out, task)
		}
	}
	return out
}

// end::drops[]

// fisherLower is the one-sided Fisher exact p-value that k1 of n1 is
// this low or lower by chance, if k1 of n1 and k2 of n2 were draws from
// one rate: the hypergeometric probability of k1 or fewer of the
// K = k1+k2 passes landing in the first n1 of N = n1+n2 runs.
func fisherLower(k1, n1, k2, n2 int) float64 {
	k, n := k1+k2, n1+n2
	p := 0.0
	for x := max(0, k-n2); x <= k1; x++ {
		p += math.Exp(lchoose(k, x) + lchoose(n-k, n1-x) - lchoose(n, n1))
	}
	return min(p, 1)
}

func lchoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// Missing returns how many more scored runs each task needs to reach
// quota, so unscored runs are replaced and every task is measured the
// same number of times; tasks already at quota are left out.
func Missing(rs Runs, tasks []string, quota int) map[string]int {
	out := map[string]int{}
	for _, task := range tasks {
		if n := quota - len(rs[task]); n > 0 {
			out[task] = n
		}
	}
	return out
}
