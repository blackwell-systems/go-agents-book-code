// Command evalcheck checks a live eval against its budget before, during,
// and after the runs (chapter 17): what each model has spent, what the
// runs left would cost at the mean and the 90th percentile of the runs
// so far, and whether any output or recording holds a fault string
// that means a run measured the machine instead of the agent.
//
// Projections count scored runs: -left is the scored runs still
// wanted, each at the cost per scored run so far.
//
//	go run ./ch17/cmd/evalcheck -records pilot.jsonl \
//		-left anthropic/claude-haiku-5.5=60,... -limit 6 \
//		-budgets anthropic/claude-haiku-5.5=1.0,... -scan rec/
//
// It exits with status 1 if the spending or the projection at the mean
// is over a limit, or a fault string was found.
package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/evals"
)

func main() {
	records := flag.String("records", "", "results files, comma-separated")
	left := flag.String("left", "", "runs still planned, as model=n, "+
		"comma-separated")
	limit := flag.Float64("limit", 6, "the whole eval's budget, dollars")
	budgets := flag.String("budgets", "", "each model's budget, as "+
		"model=dollars, comma-separated")
	scan := flag.String("scan", "", "directories of outputs and "+
		"recordings to scan for fault strings, comma-separated")
	flag.Parse()
	ok, err := check(os.Stdout, *records, *left, *budgets, *scan, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evalcheck:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func check(w io.Writer, records, left, budgets, scan string,
	limit float64) (bool, error) {
	rs, err := evals.Load(list(records)...)
	if err != nil {
		return false, fmt.Errorf("-records: %w", err)
	}
	planned, err := pairs(left, strconv.Atoi)
	if err != nil {
		return false, fmt.Errorf("-left: %w", err)
	}
	caps, err := pairs(budgets, func(s string) (float64, error) {
		return strconv.ParseFloat(s, 64)
	})
	if err != nil {
		return false, fmt.Errorf("-budgets: %w", err)
	}
	ok := true
	spent := evals.Spending(rs)
	total := 0.0
	fmt.Fprintf(w, "%-34s %5s %4s %9s %8s %8s %8s %8s\n", "model",
		"runs", "unsc", "spent", "mean", "scored", "p90", "most")
	for _, s := range spent {
		total += s.Total
		fmt.Fprintf(w, "%-34s %5d %4d %9.4f %8.4f %8.4f %8.4f %8.4f\n",
			s.Model, s.Runs, s.Unscored, s.Total, s.Mean, s.PerScored, s.P90,
			s.Most)
		if c, has := caps[s.Model]; has && s.Total > c {
			fmt.Fprintf(w, "OVER: %s spent $%.4f of $%.2f\n", s.Model,
				s.Total, c)
			ok = false
		}
	}
	fmt.Fprintf(w, "spent $%.4f of $%.2f\n", total, limit)
	if total > limit {
		ok = false
	}
	if len(planned) > 0 {
		mean, p90, per := evals.Project(rs, planned, 1)
		for _, m := range slices.Sorted(maps.Keys(per)) {
			v := per[m]
			fmt.Fprintf(w, "projected %s: $%.4f at the mean, $%.4f at "+
				"p90\n", m, v[0], v[1])
			if c, has := caps[m]; has && v[0] > c {
				fmt.Fprintf(w, "OVER: %s projects past its $%.2f\n", m, c)
				ok = false
			}
		}
		fmt.Fprintf(w, "projected total: $%.4f at the mean, $%.4f at p90\n",
			mean, p90)
		if !(mean <= limit) { // NaN, a model with no pilot, fails too
			ok = false
		}
	}
	if scan != "" {
		hits, err := evals.Scan(list(scan)...)
		if err != nil {
			return false, fmt.Errorf("-scan: %w", err)
		}
		for _, h := range hits {
			fmt.Fprintf(w, "FAULT %q x%d in %s\n", h.Fault, h.Count, h.File)
			ok = false
		}
		if len(hits) == 0 {
			fmt.Fprintln(w, "no fault strings")
		}
	}
	return ok, nil
}

func list(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// pairs parses "key=value,key=value" with parse for the values.
func pairs[T any](s string, parse func(string) (T, error)) (map[string]T,
	error) {
	out := map[string]T{}
	for _, kv := range list(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("%q: want model=value", kv)
		}
		x, err := parse(v)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", kv, err)
		}
		out[k] = x
	}
	return out, nil
}
