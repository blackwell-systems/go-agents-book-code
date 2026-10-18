package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Chapter 12's E3: how often live runs of chapter 12's program needed a
// retry, and why. Each run's whole output is saved as
// testdata/ch12runs/<name>.txt, made with commands such as
//
//	go run ./ch12 -task m1-done-after-delete -meter -trace > out.txt 2>&1
//
// and this summary reads them.

var (
	retryLine = regexp.MustCompile(`^\s*\(retry (\d+) in [^,]+, ([a-z ]+): `)
	costLine  = regexp.MustCompile(`^with the sub-agent's calls: \$([0-9.]+)`)
	checkLine = regexp.MustCompile(`^graded against the catalog: (\d+) of (\d+)`)
	callLine  = regexp.MustCompile(`^\s+chat \S+ `) // a chat span
)

// ch12Run is what one saved output says.
type ch12Run struct {
	name, task, stopped string
	calls, retries      int
	why                 map[string]int
	cost                float64
	checks              string
}

func readCh12Run(file string) (ch12Run, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return ch12Run{}, fmt.Errorf("read run: %w", err)
	}
	r := ch12Run{name: strings.TrimSuffix(filepath.Base(file), ".txt"),
		why: map[string]int{}}
	for line := range strings.Lines(string(b)) {
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "task: "):
			r.task, _, _ = strings.Cut(strings.TrimPrefix(line, "task: "), ",")
		case strings.HasPrefix(line, "stopped: "):
			r.stopped = strings.TrimPrefix(line, "stopped: ")
		case callLine.MatchString(line):
			r.calls++
		}
		if m := retryLine.FindStringSubmatch(line); m != nil {
			r.retries++
			r.why[m[2]]++
		}
		if m := costLine.FindStringSubmatch(line); m != nil {
			r.cost, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := checkLine.FindStringSubmatch(line); m != nil {
			r.checks = m[1] + "/" + m[2]
		}
	}
	return r, nil
}

// ch12RunSummary prints E3: one line per saved run, then the totals.
func ch12RunSummary(w io.Writer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "ch12runs", "*.txt"))
	if err != nil || len(files) == 0 {
		return err //nolint:wrapcheck // Glob fails only on a bad pattern
	}
	fmt.Fprintln(w, "== ch12runs: live runs of chapter 12's program, "+
		"and the retries they needed")
	fmt.Fprintln(w, "run\ttask\tchat spans\tretries\twhy\tcost\tchecks\tstopped")
	var calls, retries int
	var cost float64
	why := map[string]int{}
	for _, f := range files {
		r, err := readCh12Run(f)
		if err != nil {
			return err
		}
		calls += r.calls
		retries += r.retries
		cost += r.cost
		for k, n := range r.why {
			why[k] += n
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t$%.4f\t%s\t%s\n", r.name,
			r.task, r.calls, r.retries, reasons(r.why), r.cost, r.checks,
			r.stopped)
	}
	fmt.Fprintf(w, "all %d runs\t\t%d\t%d\t%s\t$%.4f\t\t\n", len(files),
		calls, retries, reasons(why), cost)
	fmt.Fprintln(w)
	return nil
}

func reasons(why map[string]int) string {
	if len(why) == 0 {
		return "-"
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(why)) {
		parts = append(parts, fmt.Sprintf("%s %d", k, why[k]))
	}
	return strings.Join(parts, ", ")
}
