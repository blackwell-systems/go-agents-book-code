package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// qualityRun is one run of chapter 8's prompt-quality check, as
// ch08/quality writes it with -out.
type qualityRun struct {
	Variant   string  `json:"variant"`
	Task      string  `json:"task"`
	Run       int     `json:"run"`
	RootCause bool    `json:"root_cause"`
	Passed    bool    `json:"passed"`
	Turns     int     `json:"turns"`
	Cost      float64 `json:"cost"`
	Err       string  `json:"err,omitzero"`
}

// qualityVersions is the order chapter 8's table lists the versions in.
var qualityVersions = []string{"v1", "duplicates", "old-versions",
	"crash-site", "all"}

// qualitySummary prints each file of quality runs: per prompt version,
// how many runs found every root cause and passed every check, and
// then the full passes per task.
func qualitySummary(w io.Writer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "quality", "*.jsonl"))
	if err != nil {
		return fmt.Errorf("quality: %w", err)
	}
	for _, f := range files {
		runs, err := load[qualityRun](f)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		fmt.Fprintf(w, "== quality: %s, %d runs\n", name, len(runs))
		fmt.Fprintln(w, "version\truns\troot cause\tpassed\tturns (median)\t"+
			"cost (mean)\tstopped early")
		for _, v := range qualityVersions {
			var turns []int
			var cost []float64
			n, cause, pass, stopped := 0, 0, 0, 0
			for _, r := range runs {
				if r.Variant != v {
					continue
				}
				n++
				cause += boolInt(r.RootCause)
				pass += boolInt(r.Passed)
				stopped += boolInt(r.Err != "")
				turns = append(turns, r.Turns)
				cost = append(cost, r.Cost)
			}
			if n == 0 {
				continue
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%v\t$%.4f\t%d\n", v, n, cause,
				pass, median(turns), mean(cost), stopped)
		}
		fmt.Fprintln(w, "\npassed, per task\t"+strings.Join(qualityVersions, "\t"))
		var tasks []string
		for _, r := range runs {
			if !slices.Contains(tasks, r.Task) {
				tasks = append(tasks, r.Task)
			}
		}
		slices.Sort(tasks)
		for _, t := range tasks {
			row := []string{t}
			for _, v := range qualityVersions {
				n, pass := 0, 0
				for _, r := range runs {
					if r.Task == t && r.Variant == v {
						n++
						pass += boolInt(r.Passed)
					}
				}
				row = append(row, fmt.Sprintf("%d of %d", pass, n))
			}
			fmt.Fprintln(w, strings.Join(row, "\t"))
		}
		fmt.Fprintln(w)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
