// Package taskrun sets up a crash-catalog task for the triage agent and
// grades what the agent did: the setup chapter 7's program does, shared
// so the quality check can run many tasks the same way.
package taskrun

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch14/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// A Job is one catalog task, set up for the agent: a workspace with
// remind at the task's version, an untouched copy, and a tracker.
type Job struct {
	Task   catalog.Task
	Env    *triage.Env
	remind string // remind's source, for grading
	dir    string // everything the job made, removed by Close
	filed  int    // the highest issue number before the run
}

// Load reads the catalog's tasks from repo, the book's code directory.
func Load(repo string) ([]catalog.Task, error) {
	tasks, err := catalog.Load(filepath.Join(repo, "catalog", "testdata", "tasks"))
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w (run from the book's code directory, or set -repo)", err)
	}
	return tasks, nil
}

// SetUp builds the workspace for a task: remind as it was at the
// task's version, an untouched copy for checking reproducers, the
// task's crash reports, and the issues already filed.
func SetUp(ctx context.Context, repo string, task catalog.Task) (*Job, error) {
	j := &Job{Task: task, remind: filepath.Join(repo, "remind")}
	var err error
	if j.dir, err = os.MkdirTemp("", "triage-"); err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	ws, orig := filepath.Join(j.dir, "remind"), filepath.Join(j.dir, "original")
	if err := catalog.BuildRepo(ctx, task, j.remind, ws); err != nil {
		j.Close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	if err := os.CopyFS(orig, os.DirFS(ws)); err != nil {
		j.Close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	if err := canTest(ctx, ws); err != nil {
		j.Close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	var filed []triage.Filed
	for _, k := range task.Known {
		filed = append(filed, triage.Filed{ID: k.ID, Title: k.Title, Body: k.Body})
		j.filed = max(j.filed, k.ID)
	}
	issues, err := triage.OpenTracker(j.dir, filed, now)
	if err == nil {
		var reports []triage.Report
		if reports, err = convert[[]triage.Report](task.Reports); err == nil {
			j.Env, err = triage.NewEnv(ws, orig, reports, issues)
		}
	}
	if err != nil {
		j.Close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	return j, nil
}

// Find returns the task with an ID.
func Find(tasks []catalog.Task, id string) (catalog.Task, error) {
	i := slices.IndexFunc(tasks, func(t catalog.Task) bool { return t.ID == id })
	if i < 0 {
		return catalog.Task{}, fmt.Errorf("no task %q", id)
	}
	return tasks[i], nil
}

// IssuesFile is the file the job's tracker keeps its issues in, for a
// tracker served from another process (chapter 10).
func (j *Job) IssuesFile() string { return filepath.Join(j.dir, "issues.json") }

// ReloadIssues reads the tracker's issues from its file again, after
// another process, such as the tracker's MCP server, changed them.
func (j *Job) ReloadIssues() error {
	s, err := tracker.Open(j.IssuesFile(), now)
	if err != nil {
		return fmt.Errorf("reload issues: %w", err)
	}
	j.Env.Issues = s
	return nil
}

// Now is the tracker's clock, for a tracker served from another
// process.
func Now() time.Time { return now() }

// now is the tracker's clock: the day the catalog's reports were seen,
// so a replayed run files the same issues every time.
func now() time.Time {
	return time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
}

// Close removes everything the job made.
func (j *Job) Close() {
	if j.Env != nil {
		_ = j.Env.Close() // the directory goes next anyway
	}
	_ = os.RemoveAll(j.dir) // a temporary directory; nothing to report
}

// An Outcome is what the agent produced for a job, and its grade.
type Outcome struct {
	Diagnoses []triage.Diagnosis
	Filed     []tracker.Issue // issues the agent filed
	Grade     catalog.Grade
}

// Grade reads the agent's final answer, collects the issues it filed,
// and grades both against the catalog. A run with no final answer is
// graded too, as having diagnosed nothing.
func (j *Job) Grade(ctx context.Context, final jsontext.Value) (Outcome, error) {
	var out Outcome
	if final != nil {
		var answer struct {
			Groups []triage.Diagnosis `json:"groups"`
		}
		if err := json.Unmarshal(final, &answer); err != nil {
			return out, fmt.Errorf("read the answer: %w", err)
		}
		out.Diagnoses = answer.Groups
	}
	sub := catalog.Submission{Workspace: j.Env.Dir}
	for _, is := range j.Env.Issues.Search(tracker.Query{}) {
		if is.ID > j.filed {
			out.Filed = append(out.Filed, is)
			sub.Issues = append(sub.Issues, catalog.Issue{Title: is.Title, Body: is.Body})
		}
	}
	diagnoses, err := convert[[]catalog.Diagnosis](out.Diagnoses)
	if err != nil {
		return out, err
	}
	sub.Diagnoses = diagnoses
	if out.Grade, err = catalog.GradeSubmission(ctx, j.Task, j.remind, sub); err != nil {
		return out, fmt.Errorf("grade: %w", err)
	}
	return out, nil
}

// convert copies a value into another type with the same JSON shape.
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("convert: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("convert: %w", err)
	}
	return out, nil
}

// canTest builds the workspace's tests without running any, so a run
// stops before it starts when go test can't work there at all, such as
// with a Go older than remind's go.mod asks for. Without the check the
// agent gets the same error from every run_tests call and can't fix it.
func canTest(ctx context.Context, dir string) error {
	out, err := agent.GoTest(ctx, dir, 2*time.Minute, "-run", "^$", "./...")
	if err != nil {
		return fmt.Errorf("go test can't build the workspace: %w", err)
	}
	if strings.Contains(out, "[build failed]") ||
		strings.Contains(out, "[setup failed]") {
		return fmt.Errorf("go test can't build the workspace:\n%s", out)
	}
	return nil
}
