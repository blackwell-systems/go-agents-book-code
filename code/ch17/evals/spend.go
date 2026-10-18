package evals

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
)

// A Spend is what one model's runs cost so far.
type Spend struct {
	Model           string
	Runs, Unscored  int
	Total           float64
	Mean, P90, Most float64 // per run, unscored runs included

	// PerScored is Total over the scored runs: what each scored run
	// costs once the unscored ones, which get rerun, are paid for. It
	// is chapter 14's "per scored run" figure.
	PerScored float64
}

// Spending sums the records' costs per model, sorted by model. Every
// run counts, unscored ones too: they were paid for.
func Spending(rs []Record) []Spend {
	costs := map[string][]float64{}
	unscored := map[string]int{}
	for _, r := range rs {
		costs[r.Model] = append(costs[r.Model], r.Cost)
		if r.Verdict == "unscored" {
			unscored[r.Model]++
		}
	}
	var out []Spend
	for _, m := range slices.Sorted(maps.Keys(costs)) {
		c := costs[m]
		s := Spend{Model: m, Runs: len(c), Unscored: unscored[m],
			P90: quantile(c, .9), Most: slices.Max(c)}
		for _, x := range c {
			s.Total += x
		}
		s.Mean = s.Total / float64(len(c))
		if scored := s.Runs - s.Unscored; scored > 0 {
			s.PerScored = s.Total / float64(scored)
		} else {
			s.PerScored = math.Inf(1)
		}
		out = append(out, s)
	}
	return out
}

// tag::project[]

// Project returns what the scored runs still wanted (left, per model)
// would bring the spending to, per model and in total: at the cost per
// scored run so far, and at the 90th percentile of the total, from
// resampling each model's runs so far (unscored ones included, since
// they get rerun and paid for again). The 90th percentile is of the
// sum, not n times a run's 90th percentile, which would assume every
// run is an expensive one. A model with no runs yet projects at NaN:
// run a pilot first.
func Project(rs []Record, left map[string]int, seed uint64) (mean,
	p90 float64, per map[string][2]float64) {
	costs := map[string][]float64{}
	scored := map[string]int{}
	spent := map[string]float64{}
	for _, r := range rs {
		costs[r.Model] = append(costs[r.Model], r.Cost)
		spent[r.Model] += r.Cost
		if r.Verdict != "unscored" {
			scored[r.Model]++
		}
	}
	per = map[string][2]float64{}
	const resamples = 10000
	totals := make([]float64, resamples)
	rng := rand.New(rand.NewPCG(seed, seed))
	for _, m := range slices.Sorted(maps.Keys(left)) {
		c, n := costs[m], left[m]
		if scored[m] == 0 {
			per[m] = [2]float64{math.NaN(), math.NaN()}
			mean, p90 = math.NaN(), math.NaN()
			continue
		}
		// Runs to buy n scored ones, at the rate unscored runs came.
		buy := int(math.Ceil(float64(n*len(c)) / float64(scored[m])))
		own := make([]float64, resamples)
		for i := range own {
			for range buy {
				own[i] += c[rng.IntN(len(c))]
			}
			totals[i] += spent[m] + own[i]
		}
		slices.Sort(own)
		per[m] = [2]float64{
			spent[m] + float64(n)*spent[m]/float64(scored[m]),
			spent[m] + own[resamples*9/10]}
		mean += per[m][0]
	}
	slices.Sort(totals)
	if !math.IsNaN(p90) {
		p90 = totals[resamples*9/10]
	}
	return mean, p90, per
}

// end::project[]

// Faults are the strings that mean a run measured the machine, not the
// agent: a build cache that vanished under load, a toolchain mismatch,
// a broken TLS connection, and an account out of credit. A run whose
// output or recording holds one is checked before its numbers are
// quoted.
var Faults = []string{"Library/Caches/go-build", "go >= 1.27",
	"bad record MAC", "402"}

// A Hit is a file holding a fault string, with how many times.
type Hit struct {
	File, Fault string
	Count       int
}

// Scan reads every file under the roots and reports each fault string
// it finds, per file. "402" counts only as a number of its own, not
// inside another, such as a cost of 0.0402.
func Scan(roots ...string) ([]Hit, error) {
	var hits []Hit
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry,
			err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err //nolint:wrapcheck // Scan wraps it
			}
			defer f.Close() // read only
			found, err := scanFile(f)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			for _, fault := range Faults {
				if n := found[fault]; n > 0 {
					hits = append(hits, Hit{p, fault, n})
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
	}
	return hits, nil
}

func scanFile(r io.Reader) (map[string]int, error) {
	found := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		for _, fault := range Faults {
			if fault == "402" {
				found[fault] += statusCount(line)
				continue
			}
			found[fault] += bytes.Count(line, []byte(fault))
		}
	}
	return found, sc.Err() //nolint:wrapcheck // Scan wraps it
}

// statusCount counts "402" as a number of its own: no digit or point
// on either side, as in "status":402 or "HTTP 402", but not 0.0402.
func statusCount(line []byte) int {
	part := func(b byte) bool { return b == '.' || '0' <= b && b <= '9' }
	n := 0
	for i := 0; ; {
		j := bytes.Index(line[i:], []byte("402"))
		if j < 0 {
			return n
		}
		at := i + j
		if (at == 0 || !part(line[at-1])) &&
			(at+3 == len(line) || !part(line[at+3])) {
			n++
		}
		i = at + 3
	}
}
