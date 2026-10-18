package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	agent7 "github.com/blackwell-systems/go-agents-book/code/ch07/agent"
	or7 "github.com/blackwell-systems/go-agents-book/code/ch07/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch07/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// The triage comparison (chapter 7): chapter 6's agent, with only its
// four file tools and the crash reports as a file, against chapter 7's
// triage agent, on a misleading crash with two crashes already filed
// (m1) and a batch of four causes (g3). Both are graded by the catalog.

var triageTasks = []string{"m1-done-after-delete", "g3-mixed-batch"}

type triageRun struct {
	Model    string   `json:"model"`
	Task     string   `json:"task"`
	Setup    string   `json:"setup"` // "ch06" or "ch07"
	Run      int      `json:"run"`
	Stopped  string   `json:"stopped"`
	Turns    int      `json:"turns"`
	Total    usage    `json:"total"`
	Seconds  float64  `json:"seconds"`
	Passed   bool     `json:"passed"`    // every check the catalog makes
	FixRight bool     `json:"fix_right"` // the hidden tests pass
	Failed   []string `json:"failed,omitzero"`
	Issues   int      `json:"issues"` // new issues filed
}

func (r triageRun) model() string { return r.Model }

func runTriage(ctx context.Context, e *env) error {
	tasks, err := catalog.Load("catalog/testdata/tasks")
	if err != nil {
		return fmt.Errorf("triage: %w", err)
	}
	out := &results{file: filepath.Join(e.dir, "triage.jsonl")}
	return perModel(models, func(model string) error {
		saved, err := load[triageRun](out.file)
		if err != nil {
			return err
		}
		for _, id := range triageTasks {
			i := slices.IndexFunc(tasks, func(t catalog.Task) bool { return t.ID == id })
			for _, setup := range []string{"ch06", "ch07"} {
				done := 0
				for _, r := range saved {
					if r.Model == model && r.Task == id && r.Setup == setup {
						done++
					}
				}
				for n := done + 1; n <= max(e.n, 2); n++ {
					if err := e.spend.check(); err != nil {
						return err
					}
					r, err := triageOnce(ctx, e, model, tasks[i], setup, n)
					if err != nil {
						return err
					}
					e.spend.add(r.Total.Cost)
					fmt.Printf("triage %s %s %s %d: passed %v, fix right %v, %d turns, $%.4f\n",
						model, id, setup, n, r.Passed, r.FixRight, r.Turns, r.Total.Cost)
					if err := out.add(r); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func triageOnce(ctx context.Context, e *env, model string, task catalog.Task,
	setup string, n int) (triageRun, error) {
	r := triageRun{Model: model, Task: task.ID, Setup: setup, Run: n, Stopped: "answer"}
	dir, err := os.MkdirTemp("", "evidence-triage-")
	if err != nil {
		return r, fmt.Errorf("triage: %w", err)
	}
	defer os.RemoveAll(dir)
	ws, orig := filepath.Join(dir, "remind"), filepath.Join(dir, "original")
	if err := catalog.BuildRepo(ctx, task, "remind", ws); err != nil {
		return r, fmt.Errorf("triage: %w", err)
	}
	if err := os.CopyFS(orig, os.DirFS(ws)); err != nil {
		return r, fmt.Errorf("triage: %w", err)
	}
	t := &tee{dir: filepath.Join(e.dir, "triage", setup, slug(model)),
		script: fmt.Sprintf("%s-%02d", task.ID, n)}
	c := &or7.Client{APIKey: e.key, Model: model, HTTP: &http.Client{Transport: t}}
	lim := agent7.Limits{MaxTurns: 40, MaxCost: 0.25}
	start := time.Now()
	var res agent7.Result
	var runErr error
	sub := catalog.Submission{Workspace: ws}
	switch setup {
	case "ch06":
		files, err := loadFS(ws)
		if err != nil {
			return r, err
		}
		b, err := os.ReadFile(filepath.Join("catalog/testdata/tasks", task.ID, "reports.jsonl"))
		if err != nil {
			return r, fmt.Errorf("triage: %w", err)
		}
		files["crash-reports.jsonl"] = string(b)
		res, runErr = agent7.Run(ctx, c, agent7.Tools(files), []agent7.Message{{Role: "user",
			Content: "crash-reports.jsonl holds crash reports from this service, " +
				"remind. Find out what causes the crashes and fix them."}}, lim)
		delete(files, "crash-reports.jsonl")
		if err := files.WriteDir(ws); err != nil {
			return r, fmt.Errorf("triage: %w", err)
		}
	case "ch07":
		var filed []triage.Filed
		known := 0
		for _, k := range task.Known {
			filed = append(filed, triage.Filed{ID: k.ID, Title: k.Title, Body: k.Body})
			known = max(known, k.ID)
		}
		issues, err := triage.OpenTracker(dir, filed, time.Now)
		if err != nil {
			return r, fmt.Errorf("triage: %w", err)
		}
		reports, err := triage.LoadReports(filepath.Join("catalog/testdata/tasks", task.ID, "reports.jsonl"))
		if err != nil {
			return r, fmt.Errorf("triage: %w", err)
		}
		tenv, err := triage.NewEnv(ws, orig, reports, issues)
		if err != nil {
			return r, fmt.Errorf("triage: %w", err)
		}
		defer tenv.Close()
		res, runErr = agent7.Run(ctx, c, triage.Tools(tenv), []agent7.Message{
			{Role: "system", Content: triage.Prompt},
			{Role: "user", Content: "Triage the crash reports: group them by cause, " +
				"then investigate, fix, and file each cause that isn't filed yet."},
		}, lim)
		for _, is := range issues.Search(tracker.Query{}) {
			if is.ID > known {
				sub.Issues = append(sub.Issues, catalog.Issue{Title: is.Title, Body: is.Body})
			}
		}
		if res.Final != nil {
			var answer struct {
				Groups []catalog.Diagnosis `json:"groups"`
			}
			if err := json.Unmarshal(res.Final, &answer); err == nil {
				sub.Diagnoses = answer.Groups
			}
		}
	}
	r.Seconds = since(start)
	if ctx.Err() != nil {
		return r, fmt.Errorf("triage: %w", ctx.Err())
	}
	if runErr != nil {
		r.Stopped = runErr.Error()
	}
	for _, m := range res.Messages {
		if m.Role == "assistant" {
			r.Turns++
		}
	}
	u := res.Usage
	r.Total = usage{u.PromptTokens, u.CompletionTokens, u.CachedTokens, u.ReasoningTokens, u.Cost}
	r.Issues = len(sub.Issues)
	g, err := catalog.GradeSubmission(ctx, task, "remind", sub)
	if err != nil {
		return r, fmt.Errorf("triage: grade: %w", err)
	}
	r.Passed = g.Passed()
	r.FixRight = true
	for _, ch := range g.Failed() {
		r.Failed = append(r.Failed, strings.TrimSpace(ch.Name+" "+ch.Group))
		if ch.Name == "hidden tests and suite" {
			r.FixRight = false
		}
	}
	return r, t.err()
}

// loadFS reads a workspace into chapter 6's in-memory file system,
// leaving out git's files.
func loadFS(dir string) (agent7.FS, error) {
	files := agent7.FS{}
	err := fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		files[p] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}
	return files, nil
}
