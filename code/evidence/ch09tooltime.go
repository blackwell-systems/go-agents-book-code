package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch09/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch09/groups"
	"github.com/blackwell-systems/go-agents-book/code/ch09/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch09/triage"
)

// Chapter 9's second measurement (E2), offline and free: how long each
// triage tool takes. It replays the tool calls of chapter 9's recorded
// run of g3-mixed-batch, one agent per group, against a fresh copy of
// each group's workspace, in the order the agent made them, and times
// each call. The model's replies aren't needed, only the calls, so it
// needs no key.

// toolTime is one replayed tool call.
type toolTime struct {
	Group   string  `json:"group"`
	Tool    string  `json:"tool"`
	Seconds float64 `json:"seconds"`
	Failed  bool    `json:"failed,omitzero"`
}

const (
	toolTimeTask    = "g3-mixed-batch"
	toolTimeReplies = "ch09/testdata/replies"
)

// runToolTime replays the recorded calls and writes tooltime.jsonl.
func runToolTime(ctx context.Context, e *env) error {
	tasks, err := taskrun.Load(".")
	if err != nil {
		return fmt.Errorf("tool time: %w", err)
	}
	task, err := taskrun.Find(tasks, toolTimeTask)
	if err != nil {
		return fmt.Errorf("tool time: %w", err)
	}
	j, err := taskrun.SetUp(ctx, ".", task)
	if err != nil {
		return fmt.Errorf("tool time: %w", err)
	}
	gs := groups.Split(j.Env.Reports)
	j.Close()
	file := filepath.Join(e.dir, "tooltime.jsonl")
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tool time: %w", err)
	}
	out := &results{file: file}
	for _, g := range gs {
		calls, err := recordedCalls(g.Fingerprint)
		if err != nil {
			return err
		}
		if err := timeCalls(ctx, task, g, calls, out); err != nil {
			return err
		}
	}
	return nil
}

// recordedCalls returns the tool calls in a group's recorded run, from
// its last saved request, in the order the agent made them.
func recordedCalls(fp string) ([]agent.ToolCall, error) {
	files, err := filepath.Glob(filepath.Join(toolTimeReplies, fp+"-*.json"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("tool time: no recorded run for %s", fp)
	}
	slices.SortFunc(files, func(a, b string) int { return callIndex(a) - callIndex(b) })
	b, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		return nil, fmt.Errorf("tool time: %w", err)
	}
	var saved struct {
		Request struct {
			Messages []struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		} `json:"request"`
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, fmt.Errorf("tool time: %w", err)
	}
	var calls []agent.ToolCall
	for _, m := range saved.Request.Messages {
		for _, c := range m.ToolCalls {
			calls = append(calls, agent.ToolCall{ID: c.ID, Name: c.Function.Name,
				Arguments: jsontext.Value(c.Function.Arguments)})
		}
	}
	return calls, nil
}

// callIndex is the call number in a recording's name, fp_x-NN.json.
func callIndex(file string) int {
	m := callNumber.FindStringSubmatch(filepath.Base(file))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0
	}
	return n
}

// timeCalls runs a group's calls one at a time on a fresh workspace.
func timeCalls(ctx context.Context, task catalog.Task, g groups.Group,
	calls []agent.ToolCall, out *results) error {
	sub := task
	sub.Reports = slices.DeleteFunc(slices.Clone(task.Reports),
		func(r catalog.Report) bool { return !slices.Contains(g.Reports, r.ID) })
	j, err := taskrun.SetUp(ctx, ".", sub)
	if err != nil {
		return fmt.Errorf("tool time: %w", err)
	}
	defer j.Close()
	j.Env.FileLater = true
	byName := map[string]agent.Tool{}
	for _, t := range triage.GroupTools(j.Env) {
		byName[t.Def.Name] = t
	}
	for _, c := range calls {
		t, ok := byName[c.Name]
		if !ok {
			continue
		}
		start := time.Now()
		_, err := t.Run(ctx, c.Arguments)
		err = out.add(toolTime{Group: g.Fingerprint, Tool: c.Name,
			Seconds: time.Since(start).Seconds(), Failed: err != nil})
		if err != nil {
			return err
		}
	}
	return nil
}

// toolTimeSummary prints E2's table: per tool, how many calls and how
// long they took.
func toolTimeSummary(w io.Writer, dir string) error {
	runs, err := load[toolTime](filepath.Join(dir, "tooltime.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== tool time: the recorded g3-mixed-batch group run's tool "+
		"calls, replayed one at a time (chapter 9)")
	fmt.Fprintln(w, "tool\tcalls\tmedian ms\tlongest ms\ttotal s")
	tools := modelsIn(runs, func(r toolTime) string { return r.Tool })
	all := 0.0
	for _, name := range tools {
		var secs []float64
		for _, r := range runs {
			if r.Tool == name {
				secs = append(secs, r.Seconds)
			}
		}
		total := 0.0
		for _, s := range secs {
			total += s
		}
		all += total
		fmt.Fprintf(w, "%s\t%d\t%.0f\t%.0f\t%.1f\n", name, len(secs),
			1000*median(secs), 1000*slices.Max(secs), total)
	}
	fmt.Fprintf(w, "all\t%d\t\t\t%.1f\n", len(runs), all)
	fmt.Fprintln(w)
	return nil
}
