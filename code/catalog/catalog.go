// Package catalog loads the seeded crash-triage tasks: the crash reports
// each task gives the agent, the groups and root causes it should find,
// and the hidden tests and reference fixes used to grade its work.
//
// The tasks live in testdata/tasks, one directory each. task.json is
// written by hand; reports.jsonl and expected.json are generated from it
// by tools/crashgen, which runs the task's requests against the remind
// service and records the crashes.
package catalog

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Categories are the kinds of task, with how many of each the catalog
// holds.
var Categories = map[string]int{
	"direct":        6, // the cause is where it crashes
	"misleading":    9, // the crash site or the logs point the wrong way
	"grouping":      3, // reports that group in ways fingerprints get wrong
	"already_fixed": 2, // crashes from an older version; no patch expected
	"injection":     8, // reports that try to give the agent orders
	"deadlock":      1, // a goroutine dump instead of a panic
	"long":          1, // a cause several packages from the crash
	"race":          1, // a report from the race detector
	"introduced":    1, // find the commit that introduced the bug
}

// A Task is one triage job.
type Task struct {
	ID        string           `json:"id"`
	Category  string           `json:"category"`
	Title     string           `json:"title"`
	Notes     string           `json:"notes"` // for authors; never shown to the agent
	Version   string           `json:"version"`
	Commit    string           `json:"commit"`
	Groups    map[string]Group `json:"groups"`
	Fix       []Edit           `json:"fix"`
	PreEdits  []Edit           `json:"pre_edits"`
	Forbidden *Forbidden       `json:"forbidden"`
	// Decoys are plausible wrong fixes, such as a nil check where the
	// crash happened; the hidden tests must fail with each one.
	Decoys [][]Edit `json:"decoys"`
	// Noise names other tasks whose crashes are mixed into this task's
	// batch; each is already filed in the tracker, so the agent should
	// recognize it as a duplicate.
	Noise []string `json:"noise"`
	// Race marks a task whose report comes from the race detector; its
	// tests run with -race.
	Race bool `json:"race"`
	// Tier is "hard" for tasks cheap models usually get wrong; others are
	// the regression floor, which a working agent should always pass.
	Tier string `json:"tier"`
	// Private tasks are left out of the public companion repository, so
	// they can't end up in models' training data.
	Private bool `json:"private"`
	// History, when set, is the commit history leading to today's
	// remind, oldest first, so the agent can find the commit that
	// introduced a bug.
	History []Commit `json:"history"`

	Dir      string              `json:"-"`
	Reports  []Report            `json:"-"`
	Expected map[string][]string `json:"-"` // group label to report IDs
	Known    []KnownIssue        `json:"-"` // open issues in the tracker before the run

	hidden map[string][]byte // a batch's merged hidden tests
}

// A KnownIssue is an issue already in the tracker when the agent starts.
type KnownIssue struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Fingerprint string `json:"fingerprint"`
	Group       string `json:"group"` // the group label whose reports it describes
}

// A Commit is one change in a task's history.
type Commit struct {
	Message string `json:"message"`
	Date    string `json:"date"` // RFC 3339
	Edits   []Edit `json:"edits"`
}

// A Group is one cause the agent should find.
type Group struct {
	// RootCause lists the accepted places for the root cause, as
	// "file:function", such as "store/store.go:(*Store).Get".
	RootCause []string `json:"root_cause"`
	// Status is "fixed" when a patch is expected, "already_fixed" when
	// the crash doesn't happen in the current code, or "duplicate" when
	// the cause is already filed as Issue.
	Status string `json:"status"`
	Issue  int    `json:"issue"`
	// IntroducedBy is the subject of the commit that introduced the bug,
	// for tasks with a history.
	IntroducedBy string `json:"introduced_by"`
}

// An Edit replaces text that appears exactly once in a file.
type Edit struct {
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// Forbidden lists what an agent must not do on an injection task.
type Forbidden struct {
	Paths     []string `json:"paths"`      // files or directories it must not change
	IssueText []string `json:"issue_text"` // text that must not appear in an issue
}

// A Report is a crash report as the agent sees it.
type Report struct {
	ID        string    `json:"id"`
	Service   string    `json:"service"`
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	GoVersion string    `json:"go_version"`
	Kind      string    `json:"kind"` // "panic" or "stuck"
	Panic     string    `json:"panic"`
	Stack     string    `json:"stack"`
	Request   Request   `json:"request"`
	Logs      []string  `json:"logs"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Count     int       `json:"count"`
}

// Request is the request being served when the crash happened.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Load reads every task under dir, sorted by ID.
func Load(dir string) ([]Task, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*", "task.json"))
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}
	var tasks []Task
	for _, f := range files {
		t, err := loadTask(filepath.Dir(f))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	slices.SortFunc(tasks, func(a, b Task) int { return strings.Compare(a.ID, b.ID) })
	return tasks, nil
}

func loadTask(dir string) (Task, error) {
	var t Task
	b, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		return t, fmt.Errorf("load task: %w", err)
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("load %s: %w", dir, err)
	}
	t.Dir = dir

	b, err = os.ReadFile(filepath.Join(dir, "reports.jsonl"))
	if err != nil {
		return t, fmt.Errorf("load %s: %w", t.ID, err)
	}
	for line := range strings.Lines(string(b)) {
		var r Report
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return t, fmt.Errorf("load %s reports: %w", t.ID, err)
		}
		t.Reports = append(t.Reports, r)
	}

	var exp struct {
		Groups map[string][]string `json:"groups"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		return t, fmt.Errorf("load %s: %w", t.ID, err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		return t, fmt.Errorf("load %s expected groups: %w", t.ID, err)
	}
	t.Expected = exp.Groups
	b, err = os.ReadFile(filepath.Join(dir, "known.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return t, fmt.Errorf("load %s: %w", t.ID, err)
	default:
		if err := json.Unmarshal(b, &t.Known); err != nil {
			return t, fmt.Errorf("load %s known issues: %w", t.ID, err)
		}
	}
	for _, k := range t.Known {
		if t.Groups == nil {
			t.Groups = map[string]Group{}
		}
		t.Groups[k.Group] = Group{Status: "duplicate", Issue: k.ID}
	}
	return t, nil
}

// Hidden returns the task's hidden tests, keyed by their path in the
// remind tree. Tasks with hidden API tests also get the shared helpers.
func (t Task) Hidden() (map[string][]byte, error) {
	if t.hidden != nil {
		return t.hidden, nil
	}
	files := map[string][]byte{}
	root := filepath.Join(t.Dir, "hidden")
	err := fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("read hidden test: %w", err)
		}
		files[path] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hidden tests for %s: %w", t.ID, err)
	}
	for path := range files {
		if strings.HasPrefix(path, "api/") {
			shared := filepath.Join(t.Dir, "../../hidden/api/hidden_helpers_test.go")
			b, err := os.ReadFile(shared)
			if err != nil {
				return nil, fmt.Errorf("hidden tests for %s: %w", t.ID, err)
			}
			files["api/hidden_helpers_test.go"] = b
			break
		}
	}
	return files, nil
}

// AlreadyFixed reports whether the task expects no patch.
func (t Task) AlreadyFixed() bool {
	for _, g := range t.Groups {
		if g.Status != "already_fixed" && g.Status != "duplicate" {
			return false
		}
	}
	return true
}

// Apply makes an edit to a file under tree.
func Apply(tree string, e Edit) error {
	path := filepath.Join(tree, filepath.FromSlash(e.File))
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("apply edit: %w", err)
	}
	if n := strings.Count(string(b), e.Old); n != 1 {
		return fmt.Errorf("apply edit to %s: old text found %d times, want 1", e.File, n)
	}
	s := strings.Replace(string(b), e.Old, e.New, 1)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return fmt.Errorf("apply edit: %w", err)
	}
	return nil
}
