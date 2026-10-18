package catalog

import (
	"flag"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var runTasks = flag.Bool("tasks", false, "run every task's hidden tests against the seeded and fixed code (slow)")

const (
	tasksDir  = "testdata/tasks"
	remindDir = "../remind"
)

func load(t *testing.T) []Task {
	t.Helper()
	tasks, err := Load(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func TestCategories(t *testing.T) {
	got := map[string]int{}
	for _, task := range load(t) {
		got[task.Category]++
	}
	if !maps.Equal(got, Categories) {
		t.Errorf("tasks per category = %v, want %v", got, Categories)
	}
}

func TestExpectedGroups(t *testing.T) {
	for _, task := range load(t) {
		if !slices.Equal(slices.Sorted(maps.Keys(task.Groups)), slices.Sorted(maps.Keys(task.Expected))) {
			t.Errorf("%s: groups %v, generated groups %v", task.ID, slices.Sorted(maps.Keys(task.Groups)), task.Expected)
		}
		seen := map[string]bool{}
		for _, r := range task.Reports {
			if seen[r.ID] {
				t.Errorf("%s: report %s twice", task.ID, r.ID)
			}
			seen[r.ID] = true
			if r.Stack == "" || r.Panic == "" || r.Count < 1 {
				t.Errorf("%s: report %s is incomplete", task.ID, r.ID)
			}
		}
		for g, ids := range task.Expected {
			for _, id := range ids {
				if !seen[id] {
					t.Errorf("%s: group %s lists unknown report %s", task.ID, g, id)
				}
			}
		}
		if task.AlreadyFixed() != (len(task.Fix) == 0) {
			t.Errorf("%s: already fixed = %v but has %d fix edits", task.ID, task.AlreadyFixed(), len(task.Fix))
		}
		if hidden, err := task.Hidden(); err != nil || len(hidden) == 0 {
			t.Errorf("%s: no hidden tests (%v)", task.ID, err)
		}
	}
}

// TestRootCausesExist checks that every accepted root cause names a
// function that exists in remind.
func TestRootCausesExist(t *testing.T) {
	for _, task := range load(t) {
		for label, g := range task.Groups {
			for _, rc := range g.RootCause {
				file, fn, ok := strings.Cut(rc, ":")
				if !ok {
					t.Errorf("%s/%s: root cause %q is not file:function", task.ID, label, rc)
					continue
				}
				src, err := os.ReadFile(filepath.Join(remindDir, file))
				if err != nil {
					t.Errorf("%s/%s: %v", task.ID, label, err)
					continue
				}
				if !funcPattern(fn).Match(src) {
					t.Errorf("%s/%s: no function %s in %s", task.ID, label, fn, file)
				}
			}
		}
	}
}

// funcPattern matches the declaration of "Name", "Type.Name", or
// "(*Type).Name".
func funcPattern(fn string) *regexp.Regexp {
	recv, name, ok := strings.Cut(fn, ".")
	if !ok {
		return regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(fn) + `[\[(]`)
	}
	recv = strings.Trim(recv, "()")
	return regexp.MustCompile(`(?m)^func \(\w+ ` + regexp.QuoteMeta(recv) + `\) ` + regexp.QuoteMeta(name) + `\(`)
}

func TestFixesApply(t *testing.T) {
	for _, task := range load(t) {
		tree := copyRemind(t)
		for _, e := range append(slices.Clone(task.PreEdits), task.Fix...) {
			if err := Apply(tree, e); err != nil {
				t.Errorf("%s: %v", task.ID, err)
			}
		}
		for i, decoy := range task.Decoys {
			tree := copyRemind(t)
			for _, e := range decoy {
				if err := Apply(tree, e); err != nil {
					t.Errorf("%s decoy %d: %v", task.ID, i, err)
				}
			}
		}
	}
}

// TestHistory checks that each task's history rebuilds today's remind.
func TestHistory(t *testing.T) {
	for _, task := range load(t) {
		if len(task.History) == 0 {
			continue
		}
		dst := filepath.Join(t.TempDir(), "remind")
		if err := BuildRepo(t.Context(), task, remindDir, dst); err != nil {
			t.Fatalf("%s: %v", task.ID, err)
		}
		changes, err := diffTrees(remindDir, dst)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes) > 0 {
			t.Errorf("%s: history ends with %d files that differ from remind, such as %s", task.ID, len(changes), changes[0].path)
		}
		if got := commitSubject(t.Context(), dst, "HEAD~"+strconv.Itoa(len(task.History))); !strings.Contains(got, "baseline") {
			t.Errorf("%s: first commit is %q, want the baseline", task.ID, got)
		}
	}
}

// TestTasks checks each task's hidden tests: they fail on the seeded code
// and pass with the reference fix, or, for crashes already fixed, pass on
// the current code and fail on the older code. Run with -tasks.
func TestTasks(t *testing.T) {
	if !*runTasks {
		t.Skip("run with -tasks")
	}
	sem := make(chan struct{}, 4)
	for _, task := range load(t) {
		t.Run(task.ID, func(t *testing.T) {
			t.Parallel()
			sem <- struct{}{}
			defer func() { <-sem }()

			hidden, err := task.Hidden()
			if err != nil {
				t.Fatal(err)
			}
			// The hidden tests fail on the code that crashes and pass on
			// the fixed code. For a crash already fixed, the code that
			// crashes is the older code, and the fixed code is today's.
			var failing, passing []Edit = nil, task.Fix
			if task.AlreadyFixed() {
				failing, passing = task.PreEdits, nil
			}
			out, ok := goTest(t, hidden, failing, task.Race)
			switch {
			case ok:
				t.Errorf("hidden tests pass on the code that crashes:\n%s", out)
			case !task.AlreadyFixed() && !strings.Contains(out, "--- FAIL: TestHidden"):
				// Older code may also fail tests added with its fix.
				t.Errorf("hidden tests fail, but not in a hidden test:\n%s", out)
			}
			if out, ok := goTest(t, hidden, passing, task.Race); !ok {
				t.Errorf("tests fail on the fixed code:\n%s", out)
			}
			for i, decoy := range task.Decoys {
				if out, ok := goTest(t, hidden, decoy, task.Race); ok {
					t.Errorf("hidden tests pass with decoy fix %d:\n%s", i, out)
				}
			}
		})
	}
}

// goTest copies remind, applies edits, adds the hidden tests, and runs
// every test.
func goTest(t *testing.T, hidden map[string][]byte, edits []Edit, race bool) (string, bool) {
	t.Helper()
	tree := copyRemind(t)
	for _, e := range edits {
		if err := Apply(tree, e); err != nil {
			t.Fatal(err)
		}
	}
	for path, src := range hidden {
		if err := os.WriteFile(filepath.Join(tree, filepath.FromSlash(path)), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"test", "-count=1", "-timeout=2m", "./..."}
	if race {
		args = append([]string{"test", "-race"}, args[1:]...)
	}
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = tree
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func copyRemind(t *testing.T) string {
	t.Helper()
	tree := filepath.Join(t.TempDir(), "remind")
	if err := os.CopyFS(tree, os.DirFS(remindDir)); err != nil {
		t.Fatal(err)
	}
	return tree
}
