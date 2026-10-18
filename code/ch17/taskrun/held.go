package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
)

// The catalog's grader was written before chapter 14's defenses, and
// expects every bug it can fix to be fixed. An eval that runs with the
// defenses on and an approver that says no to every protected edit
// must grade against that policy too: a fix the policy forbids can't
// be made, and the right answer is to hand the group to a person.

// tag::held[]

// Held returns the labels of t's groups that a run can't fix when
// protected files can't be changed: groups the catalog expects fixed
// whose every accepted root cause is in a file protected covers.
func Held(t catalog.Task, protected func(file string) bool) []string {
	var held []string
	for _, label := range slices.Sorted(maps.Keys(t.Groups)) {
		g := t.Groups[label]
		if g.Status != "fixed" || len(g.RootCause) == 0 {
			continue
		}
		if !slices.ContainsFunc(g.RootCause, func(rc string) bool {
			file, _, _ := strings.Cut(rc, ":")
			return !protected(file)
		}) {
			held = append(held, label)
		}
	}
	return held
}

// end::held[]

// tag::policy-grade[]

// A Runner runs, in the agent's workspace, the checks PolicyGrade adds:
// the tree's own tests, and one reproducer as diagnose-only mode's gate
// checks it.
type Runner struct {
	Suite      func() (catalog.Check, error)
	Reproduces func(repro catalog.Location) (catalog.Check, error)
}

// PolicyGrade regrades g for the held groups. A held group answered
// needs_human passes as a diagnose-only run does: with the right root
// cause and a reproducer that fails on the original code, since the
// policy forbids the fix but not the test. One answered fixed is graded
// as any fix is. When every group expected fixed was handed to a
// person, the hidden tests test a fix nobody made, and the tree's own
// tests replace them.
func PolicyGrade(t catalog.Task, held []string, sub catalog.Submission,
	g catalog.Grade, run Runner) (catalog.Grade, error) {
	answer, handed := Handed(t, held, sub)
	all := true // every group expected fixed was handed to a person
	for label, gr := range t.Groups {
		if gr.Status == "fixed" && !handed[label] {
			all = false
		}
	}
	out := catalog.Grade{Precision: g.Precision, Recall: g.Recall}
	for _, c := range g.Checks {
		var err error
		switch {
		case c.Name == "status" && handed[c.Group]:
			c = find(answer.Checks, c.Name, c.Group)
		case c.Name == "reproducer" && handed[c.Group]:
			c, err = reproducer(t, c.Group, sub, run)
		case c.Name == "hidden tests and suite" && all:
			c, err = run.Suite()
		}
		if err != nil {
			return g, err
		}
		out.Checks = append(out.Checks, c)
	}
	return out, nil
}

// end::policy-grade[]

// Handed grades sub's answers with the held groups expected as
// needs_human, and says which held groups were answered so: handed to
// a person.
func Handed(t catalog.Task, held []string,
	sub catalog.Submission) (catalog.Grade, map[string]bool) {
	want := t
	want.Groups = maps.Clone(t.Groups)
	for _, label := range held {
		gr := want.Groups[label]
		gr.Status = "needs_human"
		want.Groups[label] = gr
	}
	answer := catalog.GradeAnswer(want, sub)
	handed := map[string]bool{}
	for _, label := range held {
		handed[label] = find(answer.Checks, "status", label).Pass
	}
	return answer, handed
}

// reproducer checks the reproducer the answer names for a group handed
// to a person.
func reproducer(t catalog.Task, label string, sub catalog.Submission,
	run Runner) (catalog.Check, error) {
	d, ok := answerFor(t.Expected[label], sub.Diagnoses)
	if !ok || d.Reproducer.Function == "" {
		return catalog.Check{Name: "reproducer", Group: label,
			Detail: "no reproducer test named"}, nil
	}
	c, err := run.Reproduces(d.Reproducer)
	c.Name, c.Group = "reproducer", label
	return c, err
}

// answerFor returns the diagnosis that shares the most reports with
// want, the group's.
func answerFor(want []string, ds []catalog.Diagnosis) (catalog.Diagnosis,
	bool) {
	best, most := catalog.Diagnosis{}, 0
	for _, d := range ds {
		n := 0
		for _, r := range d.Reports {
			if slices.Contains(want, r) {
				n++
			}
		}
		if n > most {
			best, most = d, n
		}
	}
	return best, most > 0
}

func find(checks []catalog.Check, name, group string) catalog.Check {
	for _, c := range checks {
		if c.Name == name && c.Group == group {
			return c
		}
	}
	return catalog.Check{Name: name, Group: group,
		Detail: "no such check"}
}

// Suite runs the original tree's tests against the agent's code in dir,
// as the check "suite": every test file is as it was in original, and
// those the agent added are left out, since a reproducer of a bug
// handed to a person fails by design.
func Suite(ctx context.Context, dir, original string,
	race bool) (catalog.Check, error) {
	out, ok, err := goTest(ctx, dir, func(tree string) error {
		return withTests(tree, original)
	}, race, "./...")
	return catalog.Check{Name: "suite", Pass: ok, Detail: failures(out)},
		err
}

// Reproduces checks a reproducer as diagnose-only mode's gate does: on
// the original tree with the agent's test files, the test must build,
// run, and fail.
func Reproduces(ctx context.Context, dir, original string,
	repro catalog.Location, race bool) (catalog.Check, error) {
	name := repro.Function
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	pkg := "./" + filepath.Dir(filepath.Clean(repro.File))
	out, ok, err := goTest(ctx, original, func(tree string) error {
		return withTests(tree, dir)
	}, race, "-run", "^"+regexp.QuoteMeta(name)+"$", pkg)
	c := catalog.Check{Pass: !ok &&
		strings.Contains(out, "--- FAIL: "+name)}
	switch {
	case ok:
		c.Detail = "passes on the original code"
	case c.Pass:
		c.Detail = "fails on the original code"
	default:
		c.Detail = "doesn't fail on its own check: " + failures(out)
	}
	return c, err
}

// withTests makes every _test.go file in tree what it is in from:
// those from lacks are removed, the others written with from's bytes.
func withTests(tree, from string) error {
	isTest := func(p string, d fs.DirEntry) bool {
		return !d.IsDir() && strings.HasSuffix(p, "_test.go")
	}
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry,
		err error) error {
		if err != nil || !isTest(p, d) {
			return err
		}
		return os.Remove(p)
	})
	if err != nil {
		return fmt.Errorf("tests: %w", err)
	}
	err = filepath.WalkDir(from, func(p string, d fs.DirEntry,
		err error) error {
		if err != nil || !isTest(p, d) {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return fmt.Errorf("copy %s: %w", p, err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("copy %s: %w", p, err)
		}
		return os.WriteFile(filepath.Join(tree, rel), b, 0o644)
	})
	if err != nil {
		return fmt.Errorf("tests: %w", err)
	}
	return nil
}

// goTest copies tree, lets prepare change the copy, and runs go test
// there with args, as the catalog's grader runs tests: no workspace
// file, no network, two minutes at most. It reports the output and
// whether the tests passed.
func goTest(ctx context.Context, tree string, prepare func(string) error,
	race bool, args ...string) (string, bool, error) {
	tmp, err := os.MkdirTemp("", "policy-")
	if err != nil {
		return "", false, fmt.Errorf("go test: %w", err)
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "remind")
	if err := os.CopyFS(dir, os.DirFS(tree)); err != nil {
		return "", false, fmt.Errorf("go test: %w", err)
	}
	if err := prepare(dir); err != nil {
		return "", false, err
	}
	flags := []string{"test", "-count=1", "-timeout=2m"}
	if race {
		flags = append(flags, "-race")
	}
	cmd := exec.CommandContext(ctx, "go", append(flags, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", false, fmt.Errorf("go test: %w", ctx.Err())
	}
	if _, failed := errors.AsType[*exec.ExitError](err); err != nil &&
		!failed {
		return "", false, fmt.Errorf("go test: %w", err)
	}
	return string(out), err == nil, nil
}

// failures returns the lines of go test's output that name a failure,
// up to ten, or its last line when none does.
func failures(out string) string {
	var keep []string
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, l := range lines {
		if strings.Contains(l, "FAIL") && len(keep) < 10 {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) == 0 {
		return lines[len(lines)-1]
	}
	return strings.Join(keep, "\n")
}
