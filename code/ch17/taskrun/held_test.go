package taskrun

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// TestHeld: the groups whose every accepted root cause is in a
// protected file are held, and no others.
func TestHeld(t *testing.T) {
	tasks, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"d5-bearer-no-token": {"A"},
		"g2-missing-auth": {"A"}, "m5-admin-user-copy": {"A"},
		"g3-mixed-batch": {"D"}}
	for _, task := range tasks {
		got := Held(task, triage.Protected.Covers)
		if !slices.Equal(got, want[task.ID]) {
			t.Errorf("%s: held %v, want %v", task.ID, got, want[task.ID])
		}
	}
}

// TestPolicyGrade: a held group answered needs_human passes with the
// right root cause and a reproducer that fails on the original code,
// with the suite in place of the hidden tests, and fails with no
// reproducer; one answered fixed is graded by the grader's own checks.
func TestPolicyGrade(t *testing.T) {
	tasks, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(tasks, func(t catalog.Task) bool {
		return t.ID == "d5-bearer-no-token"
	})
	task := tasks[i]
	recorded := func(status, repro, hidden bool) catalog.Grade {
		return catalog.Grade{Checks: []catalog.Check{
			{Name: "found", Group: "A", Pass: true},
			{Name: "status", Group: "A", Pass: status},
			{Name: "root cause", Group: "A", Pass: true},
			{Name: "reproducer", Group: "A", Pass: repro},
			{Name: "hidden tests and suite", Pass: hidden},
		}}
	}
	run := Runner{
		Suite: func() (catalog.Check, error) {
			return catalog.Check{Name: "suite", Pass: true}, nil
		},
		Reproduces: func(catalog.Location) (catalog.Check, error) {
			return catalog.Check{Pass: true}, nil
		},
	}
	answer := func(status, repro string) catalog.Submission {
		return catalog.Submission{Diagnoses: []catalog.Diagnosis{{
			Reports: task.Expected["A"], Status: status,
			Reproducer: catalog.Location{File: "api/x_test.go",
				Function: repro}}}}
	}
	for _, tc := range []struct {
		name     string
		sub      catalog.Submission
		recorded catalog.Grade
		pass     bool
		suite    bool // the suite replaced the hidden tests
	}{
		{"handed over with a reproducer", answer("needs_human", "TestX"),
			recorded(false, false, false), true, true},
		{"handed over without one", answer("needs_human", ""),
			recorded(false, false, false), false, true},
		{"fixed", answer("fixed", "TestX"), recorded(true, true, true),
			true, false},
		{"fixed, reproducer fails", answer("fixed", "TestX"),
			recorded(true, false, true), false, false},
		{"fixed, hidden tests fail", answer("fixed", "TestX"),
			recorded(true, true, false), false, false},
	} {
		g, err := PolicyGrade(task, []string{"A"}, tc.sub, tc.recorded, run)
		if err != nil {
			t.Fatal(err)
		}
		suite := slices.ContainsFunc(g.Checks, func(c catalog.Check) bool {
			return c.Name == "suite"
		})
		if g.Passed() != tc.pass || suite != tc.suite {
			t.Errorf("%s: passed %v, suite %v; want %v, %v: %+v", tc.name,
				g.Passed(), suite, tc.pass, tc.suite, g.Checks)
		}
	}
	if task.Groups["A"].Status != "fixed" {
		t.Error("PolicyGrade changed the task's own groups")
	}
}

// TestSuiteAndReproduces: Suite runs the original tree's tests, leaving
// out a reproducer the agent appended to an existing test file and one
// in a new file; Reproduces passes a test that fails on the original
// code and fails one that passes there.
func TestSuiteAndReproduces(t *testing.T) {
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text),
			0o644); err != nil {
			t.Fatal(err)
		}
	}
	orig, ws := t.TempDir(), t.TempDir()
	for _, dir := range []string{orig, ws} {
		write(dir, "go.mod", "module m\n\ngo 1.26\n")
		write(dir, "a.go", "package m\n\nfunc One() int { return 1 }\n")
	}
	base := "package m\n\nimport \"testing\"\n\n" +
		"func TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"one\")\n\t}\n}\n"
	write(orig, "a_test.go", base)
	write(ws, "a_test.go", base+"\nfunc TestRepro(t *testing.T) { t.Fatal(\"bug\") }\n"+
		"\nfunc TestFine(t *testing.T) {}\n")
	write(ws, "new_test.go", "package m\n\nimport \"testing\"\n\n"+
		"func TestNew(t *testing.T) { t.Fatal(\"bug\") }\n")
	c, err := Suite(t.Context(), ws, orig, false)
	if err != nil || !c.Pass {
		t.Errorf("suite: %+v, %v", c, err)
	}
	for _, tc := range []struct {
		test string
		pass bool
	}{{"TestRepro", true}, {"TestNew", true}, {"TestFine", false}} {
		c, err := Reproduces(t.Context(), ws, orig,
			catalog.Location{File: "a_test.go", Function: tc.test}, false)
		if err != nil || c.Pass != tc.pass {
			t.Errorf("%s: %+v, %v; want pass %v", tc.test, c, err, tc.pass)
		}
	}
}
