package triage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch09/agent"
)

// tag::diagnosis[]

// A Diagnosis is the agent's answer for one fingerprint group: which reports
// share a cause, where the code is wrong, the test that shows it, and
// what was done about it.
type Diagnosis struct {
	Fingerprint string   `json:"fingerprint" desc:"the group's fingerprint, from crash_list"`
	Reports     []string `json:"reports" desc:"the IDs of every report in the group"`
	Category    string   `json:"category" enum:"nil_value,out_of_range,concurrency,bad_input,other"`
	RootCause   Location `json:"root_cause" desc:"where the code is wrong, which may not be where it crashed"`
	Evidence    string   `json:"evidence" desc:"why this is the cause: what you read and ran"`
	Reproducer  Location `json:"reproducer,omitzero" desc:"the test you wrote that fails before the fix; leave out for already_fixed and duplicate"`
	Fix         string   `json:"fix,omitzero" desc:"what you changed, for status fixed"`
	Status      string   `json:"status" enum:"fixed,already_fixed,duplicate,not_a_bug,needs_human"`
	Issue       int      `json:"issue,omitzero" desc:"the issue you filed, for status fixed or needs_human"`
	Duplicate   int      `json:"duplicate,omitzero" desc:"for status duplicate: the open issue it duplicates"`
}

// A Location is a function in a file of remind's source.
type Location struct {
	File     string `json:"file" desc:"a file path, such as store/store.go"`
	Function string `json:"function" desc:"a function or method, such as (*Store).Get or TestGetDeleted"`
}

// end::diagnosis[]

type submitArgs struct {
	Groups []Diagnosis `json:"groups" desc:"one entry per crash group; every report belongs to exactly one"`
}

// tag::submit[]

// submitDiagnosis is the final tool: a successful call ends the run,
// and its arguments are the run's answer. Before accepting them, it
// checks what can be checked, and sends anything wrong back to the
// model to fix.
func submitDiagnosis(e *Env) agent.Tool {
	t := agent.NewTool("submit_diagnosis",
		"Finish the job: submit one diagnosis per crash group. Call it "+
			"once, at the end, after filing an issue for each new cause. "+
			"For status fixed, the reproducer test must fail on the "+
			"original code and pass on yours; this is checked.",
		func(ctx context.Context, a submitArgs) (string, error) {
			if err := e.checkGroups(a.Groups); err != nil {
				return "", err
			}
			for _, d := range a.Groups {
				if d.Status != "fixed" {
					continue
				}
				if err := e.checkReproducer(ctx, d.Reproducer, e.isRace(d.Reports)); err != nil {
					return "", fmt.Errorf("group %s: %w", d.Fingerprint, err)
				}
			}
			return "accepted", nil
		})
	t.Final = true
	return t
}

// end::submit[]

// isRace reports whether any of the reports came from the race
// detector, so their reproducer runs with -race.
func (e *Env) isRace(ids []string) bool {
	for _, r := range e.Reports {
		if r.Kind == "race" && slices.Contains(ids, r.ID) {
			return true
		}
	}
	return false
}

// checkGroups checks that every report is in exactly one group, and
// that each group has what its status needs.
func (e *Env) checkGroups(groups []Diagnosis) error {
	seen := map[string]int{}
	var problems []string
	for _, d := range groups {
		for _, id := range d.Reports {
			seen[id]++
		}
		switch d.Status {
		case "fixed", "needs_human":
			if e.FileLater {
				break // the harness files it (chapter 9)
			}
			if _, err := e.Issues.Get(d.Issue); err != nil {
				problems = append(problems, fmt.Sprintf("group %s has "+
					"status %s but no filed issue; file one with issue_file "+
					"and give its number as issue", d.Fingerprint, d.Status))
			}
		case "duplicate":
			if is, err := e.Issues.Get(d.Duplicate); err != nil || is.Status != "open" {
				problems = append(problems, fmt.Sprintf("group %s is a "+
					"duplicate of #%d, which "+
					"isn't an open issue; issue_search lists them", d.Fingerprint, d.Duplicate))
			}
		}
	}
	missing := map[string][]string{} // fingerprint: reports in no group
	for _, r := range e.Reports {
		switch n := seen[r.ID]; {
		case n == 0:
			fp := Fingerprint(r)
			missing[fp] = append(missing[fp], r.ID)
		case n > 1:
			problems = append(problems, fmt.Sprintf("%s is in %d groups", r.ID, n))
		}
	}
	if len(missing) > 0 {
		problems = append(problems, notGrouped(missing))
	}
	for id := range seen {
		if !slices.ContainsFunc(e.Reports, func(r Report) bool { return r.ID == id }) {
			problems = append(problems, fmt.Sprintf("there is no report %q; "+
				"list report IDs from crash_list, such as %s", id, e.Reports[0].ID))
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// notGrouped says which reports are in no group, by fingerprint, so a
// batch's missing reports can be added to their groups in one step
// instead of looked up again one by one.
func notGrouped(missing map[string][]string) string {
	n := 0
	var parts []string
	for _, fp := range slices.Sorted(maps.Keys(missing)) {
		n += len(missing[fp])
		parts = append(parts, fp+": "+strings.Join(missing[fp], ", "))
	}
	what := fmt.Sprintf("%d reports are", n)
	if n == 1 {
		what = "1 report is"
	}
	return what + " in no group; every report must be listed by ID in " +
		"one group's reports. By fingerprint: " + strings.Join(parts, "; ")
}

// tag::check-reproducer[]

// checkReproducer runs the named test on the original code, with the
// agent's test files added, where it must fail, and on the agent's
// code, where it must pass. A test that passes before the fix doesn't
// reproduce anything, however good the fix is.
func (e *Env) checkReproducer(ctx context.Context, test Location,
	race bool) error {
	file, err := clean(test.File)
	if err != nil || !strings.HasSuffix(file, "_test.go") {
		return errors.New("the reproducer must be a test in " +
			"a _test.go file, given as file and function")
	}
	args := []string{"-run",
		"^" + regexp.QuoteMeta(test.Function) + "$",
		"./" + path.Dir(file)}
	if race {
		args = append([]string{"-race"}, args...) // <1>
	}
	orig, err := e.originalWithTests()
	if err != nil {
		return err
	}
	defer os.RemoveAll(orig)
	before, err := e.goTest(ctx, orig, args)
	if err != nil {
		return err
	}
	switch outcome(before) {
	case "pass":
		return fmt.Errorf("%s passes on the original code, so it "+
			"doesn't reproduce the crash:\n%s", test.Function, before)
	case "no test":
		return fmt.Errorf("no test named %s ran in %s", test.Function, args[len(args)-1])
	case "no build":
		return fmt.Errorf("the tests don't compile on the "+
			"original code:\n%s", before)
	}
	after, err := e.goTest(ctx, e.Dir, args) // <2>
	if err != nil {
		return err
	}
	if outcome(after) != "pass" {
		return fmt.Errorf("%s doesn't pass on your fixed code:\n%s",
			test.Function, after)
	}
	return nil
}

// end::check-reproducer[]

// tag::outcome[]

// outcome reads a run_tests report for one test: "pass", "fail",
// "no test" when nothing matched the name, or "no build". A panic, a
// test that hung until go test stopped it, or a crash of the whole
// test binary all count as a failure, since a crash is what a
// reproducer reproduces.
func outcome(report string) string {
	switch {
	case strings.Contains(report, "[build failed]"),
		strings.Contains(report, "[setup failed]"):
		return "no build"
	case strings.Contains(report, "FAIL"):
		return "fail"
	case strings.Contains(report, "[no tests to run]"):
		return "no test"
	}
	return "pass"
}

// end::outcome[]

func (e *Env) goTest(ctx context.Context, dir string, args []string) (string, error) {
	out, err := e.test(ctx, dir, 2*time.Minute, args...)
	if err != nil {
		return "", fmt.Errorf("check reproducer: %w", err)
	}
	return out, nil
}

// test runs go test in dir, once one of the shared test slots is free,
// if the Env has them (chapter 9).
func (e *Env) test(ctx context.Context, dir string, timeout time.Duration,
	args ...string) (string, error) {
	if e.TestSlots != nil {
		if err := e.TestSlots.Acquire(ctx); err != nil {
			return "", fmt.Errorf("wait for a test slot: %w", err)
		}
		defer e.TestSlots.Release()
	}
	return agent.GoTest(ctx, dir, timeout, args...) //nolint:wrapcheck // its errors say "go test"
}

// originalWithTests copies the original code to a temporary directory
// and adds every test file the agent wrote or changed.
func (e *Env) originalWithTests() (string, error) {
	dir, err := os.MkdirTemp("", "reproducer-")
	if err != nil {
		return "", fmt.Errorf("check reproducer: %w", err)
	}
	if err := os.CopyFS(dir, os.DirFS(e.Original)); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("check reproducer: %w", err)
	}
	err = fs.WalkDir(e.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return err
		}
		data, err := e.root.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		old, err := os.ReadFile(filepath.Join(e.Original, filepath.FromSlash(p)))
		if err == nil && bytes.Equal(old, data) {
			return nil // unchanged
		}
		return os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), data, 0o644)
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("check reproducer: %w", err)
	}
	return dir, nil
}
