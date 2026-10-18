package main

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
	"github.com/blackwell-systems/go-agents-book/code/ch07/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// A job is one catalog task, set up for the agent: a workspace with
// remind at the task's version, an untouched copy, and a tracker.
type job struct {
	task   catalog.Task
	env    *triage.Env
	remind string // remind's source, for grading
	dir    string // everything the job made, removed by close
	filed  int    // the highest issue number before the run
}

// tag::set-up[]

// setUp builds the workspace for a task: remind as it was at the
// task's version, an untouched copy for checking reproducers, the
// task's crash reports, and the issues already filed.
func setUp(ctx context.Context, repo, id string) (*job, error) {
	tasks, err := catalog.Load(filepath.Join(repo, "catalog", "testdata", "tasks"))
	if err != nil {
		return nil, fmt.Errorf("set up: %w (run from the book's code directory, or set -repo)", err)
	}
	i := slices.IndexFunc(tasks, func(t catalog.Task) bool { return t.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("set up: no task %q", id)
	}
	j := &job{task: tasks[i], remind: filepath.Join(repo, "remind")}
	if j.dir, err = os.MkdirTemp("", "triage-"); err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	ws, orig := filepath.Join(j.dir, "remind"), filepath.Join(j.dir, "original")
	if err := catalog.BuildRepo(ctx, j.task, j.remind, ws); err != nil {
		j.close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	if err := os.CopyFS(orig, os.DirFS(ws)); err != nil {
		j.close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	var filed []triage.Filed
	for _, k := range j.task.Known {
		filed = append(filed, triage.Filed{ID: k.ID, Title: k.Title, Body: k.Body})
		j.filed = max(j.filed, k.ID)
	}
	issues, err := triage.OpenTracker(j.dir, filed, j.now)
	if err == nil {
		var reports []triage.Report
		if reports, err = convert[[]triage.Report](j.task.Reports); err == nil {
			j.env, err = triage.NewEnv(ws, orig, reports, issues)
		}
	}
	if err != nil {
		j.close()
		return nil, fmt.Errorf("set up: %w", err)
	}
	return j, nil
}

// end::set-up[]

// now is the tracker's clock: the day the task's reports were seen, so
// a replayed run files the same issues every time.
func (j *job) now() time.Time {
	return time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
}

func (j *job) close() {
	if j.env != nil {
		j.env.Close()
	}
	os.RemoveAll(j.dir)
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

// tag::report[]

// report prints the agent's answer, the issues it filed, and the
// catalog's grade: the checks chapter 13 builds its evals from.
func (j *job) report(ctx context.Context, p *printer, final jsontext.Value) error {
	var answer struct {
		Groups []triage.Diagnosis `json:"groups"`
	}
	if err := json.Unmarshal(final, &answer); err != nil {
		return fmt.Errorf("read the answer: %w", err)
	}
	p.printf("\ndiagnosis:\n")
	for _, d := range answer.Groups {
		p.printf("  %s %s: %s, root cause %s %s", d.Fingerprint,
			strings.Join(d.Reports, ","), d.Status, d.RootCause.File,
			d.RootCause.Function)
		switch {
		case d.Issue > 0:
			p.printf(", issue #%d", d.Issue)
		case d.Duplicate > 0:
			p.printf(", duplicate of #%d", d.Duplicate)
		}
		p.printf("\n")
	}
	sub := catalog.Submission{Workspace: j.env.Dir}
	for _, is := range j.env.Issues.Search(tracker.Query{}) {
		if is.ID > j.filed {
			p.printf("filed #%d: %s\n", is.ID, is.Title)
			sub.Issues = append(sub.Issues, catalog.Issue{Title: is.Title, Body: is.Body})
		}
	}
	diagnoses, err := convert[[]catalog.Diagnosis](answer.Groups)
	if err != nil {
		return err
	}
	sub.Diagnoses = diagnoses
	g, err := catalog.GradeSubmission(ctx, j.task, j.remind, sub)
	if err != nil {
		return fmt.Errorf("grade: %w", err)
	}
	p.printf("\ngraded against the catalog: %d of %d checks passed\n",
		len(g.Checks)-len(g.Failed()), len(g.Checks))
	for _, c := range g.Failed() {
		p.printf("  failed: %s %s: %s\n", c.Name, c.Group, c.Detail)
	}
	return nil
}

// end::report[]
