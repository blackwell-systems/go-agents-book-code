// Command regrade grades chapter 17's saved eval records again under
// the policy-aware rule (taskrun.Held, PolicyGrade), offline: no model
// is called. For a record of a task with held groups, it reads each
// run's final answer from its recordings; where a check needs the
// agent's workspace, it rebuilds it from the task's tree and the run's
// code_patch calls that went through, in order. Every other check keeps
// the record's result.
//
// It grades the confirmation records (t1-MODEL.jsonl) and the batch
// (t2-haiku.jsonl) two ways: by the final rule, where a group handed to
// a person needs a reproducer that fails on the original code, and by a
// looser one that waives the reproducer. It prints the pass rates under
// the original grading and both rules, with every moved verdict, and
// writes the final rule's records beside the originals, as
// t1-MODEL-regraded.jsonl and t2-haiku-regraded.jsonl. From the book's
// code directory:
//
//	go run ./ch17/cmd/regrade -live DIR
package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/evals"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

func main() {
	live := flag.String("live", "", "the live runs' directory, holding "+
		"t1-MODEL.jsonl and rec-t1-MODEL for each model, and "+
		"t2-haiku.jsonl and rec-t2-haiku")
	models := flag.String("models", "haiku,luna,deepseek",
		"the models' short names, comma-separated")
	repo := flag.String("repo", ".", "the book's code directory")
	flag.Parse()
	tasks, err := taskrun.Load(*repo)
	if err != nil {
		log.Fatal(err)
	}
	g := grader{repo: *repo, tasks: map[string]catalog.Task{}}
	for _, t := range tasks {
		g.tasks[t.ID] = t
	}
	if g.tasks["batch"], err = catalog.Batch(tasks); err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	for m := range strings.SplitSeq(*models, ",") {
		if err := g.file(ctx, *live, "t1-"+m, "rec-t1-"+m); err != nil {
			log.Fatal(err)
		}
	}
	if err := g.file(ctx, *live, "t2-haiku", "rec-t2-haiku"); err != nil {
		log.Fatal(err)
	}
}

type grader struct {
	repo  string
	tasks map[string]catalog.Task
}

// file grades the records in DIR/name.jsonl both ways and writes the
// final rule's to DIR/name-regraded.jsonl.
func (g grader) file(ctx context.Context, live, name, rec string) error {
	rs, err := evals.Load(filepath.Join(live, name+".jsonl"))
	if err != nil {
		return fmt.Errorf("regrade: %w", err)
	}
	fmt.Printf("%s: original %s\n", name, rate(rs))
	for _, looser := range []bool{true, false} {
		rule := "final rule"
		if looser {
			rule = "looser rule (no reproducer)"
		}
		var out []evals.Record
		moved := 0
		for _, r := range rs {
			before := r.Verdict
			if r, err = g.record(ctx, r, filepath.Join(live, rec),
				looser); err != nil {
				return fmt.Errorf("%s run %d: %w", r.Task, r.Run, err)
			}
			if r.Verdict != before {
				moved++
				fmt.Printf("  %s: %s run %d %s -> %s\n", rule, r.Task, r.Run,
					before, r.Verdict)
			}
			out = append(out, r)
		}
		fmt.Printf("%s: %s %s, %d verdicts moved\n", name, rule, rate(out),
			moved)
		if !looser {
			err := write(filepath.Join(live, name+"-regraded.jsonl"), out)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// record grades one record again, if its task has held groups.
func (g grader) record(ctx context.Context, r evals.Record, recordings string,
	looser bool) (evals.Record, error) {
	t, ok := g.tasks[r.Task]
	held := taskrun.Held(t, triage.Protected.Covers)
	if !ok || len(held) == 0 || r.Verdict == "unscored" {
		return r, nil
	}
	prefix := fmt.Sprintf("%s-%s-%s-%02d-", r.Set, r.Version, r.Task, r.Run)
	scripts := []string{prefix}
	if r.Task == "batch" { // one script per group
		scripts = groupScripts(recordings, prefix)
	}
	var sub catalog.Submission
	var calls []patchCall
	for _, s := range scripts {
		cs, final, err := readRun(recordings, s)
		if err != nil {
			return r, err
		}
		calls = append(calls, cs...)
		var answer struct {
			Groups []catalog.Diagnosis `json:"groups"`
		}
		if final != "" {
			if err := json.Unmarshal([]byte(final), &answer); err != nil {
				return r, fmt.Errorf("read the answer: %w", err)
			}
		}
		sub.Diagnoses = append(sub.Diagnoses, answer.Groups...)
	}
	before := catalog.Grade{Checks: []catalog.Check{
		{Name: "recorded checks", Pass: true}}}
	for _, f := range r.Failed {
		i := strings.LastIndex(f, " ")
		before.Checks = append(before.Checks,
			catalog.Check{Name: f[:i], Group: f[i+1:]})
	}
	for _, label := range held { // passed checks the rule may replace
		for _, name := range []string{"status", "reproducer"} {
			if !slices.Contains(r.Failed, name+" "+label) {
				before.Checks = append(before.Checks,
					catalog.Check{Name: name, Group: label, Pass: true})
			}
		}
	}
	if !slices.Contains(r.Failed, "hidden tests and suite ") {
		before.Checks = append(before.Checks, catalog.Check{
			Name: "hidden tests and suite", Pass: true})
	}
	run, done := g.runner(ctx, t, calls, r.Task == "batch")
	defer done()
	got, err := taskrun.PolicyGrade(t, held, sub, before, run)
	if err != nil {
		return r, fmt.Errorf("regrade: %w", err)
	}
	_, handed := taskrun.Handed(t, held, sub)
	var failed []string
	for _, c := range got.Failed() {
		if looser && c.Name == "reproducer" && handed[c.Group] {
			continue // the looser rule waives it
		}
		failed = append(failed, c.Name+" "+c.Group)
	}
	verdict := "pass"
	if len(failed) > 0 {
		verdict = "fail"
	}
	if verdict != r.Verdict || !slices.Equal(failed, r.Failed) {
		r.Regraded = fmt.Sprintf("policy-aware grading, held %v: was %s, "+
			"failed %q", held, r.Verdict, r.Failed)
		r.Verdict, r.Failed = verdict, failed
	}
	return r, nil
}

// groupScripts returns the batch run's per-group script prefixes.
func groupScripts(dir, prefix string) []string {
	names, _ := filepath.Glob(filepath.Join(dir, prefix+"fp_*.json"))
	var out []string
	for _, n := range names {
		fp := strings.TrimPrefix(filepath.Base(n), prefix)
		fp, _, _ = strings.Cut(fp, "-")
		if s := prefix + fp + "-"; !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// runner rebuilds the run's workspace, once, when a check needs it;
// done removes it.
func (g grader) runner(ctx context.Context, t catalog.Task,
	calls []patchCall, batch bool) (run taskrun.Runner, done func()) {
	var tmp, ws, orig string
	build := func() error {
		if batch {
			return errors.New("the batch's merged workspace isn't rebuilt")
		}
		if ws != "" {
			return nil
		}
		var err error
		if tmp, err = os.MkdirTemp("", "regrade-"); err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		ws, orig = filepath.Join(tmp, "remind"), filepath.Join(tmp, "original")
		for _, dir := range []string{ws, orig} {
			if err := catalog.BuildRepo(ctx, t,
				filepath.Join(g.repo, "remind"), dir); err != nil {
				return fmt.Errorf("workspace: %w", err)
			}
		}
		for _, c := range calls {
			if err := patch(ws, c); err != nil {
				return err
			}
		}
		return nil
	}
	run = taskrun.Runner{
		Suite: func() (catalog.Check, error) {
			if err := build(); err != nil {
				return catalog.Check{}, err
			}
			c, err := taskrun.Suite(ctx, ws, orig, t.Race)
			if err != nil {
				return c, fmt.Errorf("suite: %w", err)
			}
			return c, nil
		},
		Reproduces: func(l catalog.Location) (catalog.Check, error) {
			if err := build(); err != nil {
				return catalog.Check{}, err
			}
			c, err := taskrun.Reproduces(ctx, ws, orig, l, t.Race)
			if err != nil {
				return c, fmt.Errorf("reproducer: %w", err)
			}
			return c, nil
		},
	}
	return run, func() {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
	}
}

// rate is the pass rate over tasks, as the table computes it, with the
// passes and scored runs.
func rate(rs []evals.Record) string {
	runs := evals.Scored(rs, nil)
	k, n := 0, 0
	for _, task := range runs {
		for _, ok := range task {
			n++
			if ok {
				k++
			}
		}
	}
	e := runs.PassAt(1, rand.NewPCG(1, 1))
	return fmt.Sprintf("%.1f%% over %d tasks (%d of %d runs)", 100*e.Mean,
		e.Tasks, k, n)
}

func write(name string, rs []evals.Record) error {
	var b strings.Builder
	for _, r := range rs {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("write record: %w", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write records: %w", err)
	}
	return nil
}

// A patchCall is one code_patch call that went through.
type patchCall struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// patch applies a code_patch call as the tool does: a new file when
// Old is empty, otherwise Old's one occurrence replaced by New.
func patch(ws string, c patchCall) error {
	name := filepath.Join(ws, filepath.FromSlash(c.Path))
	if c.Old == "" {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return fmt.Errorf("patch %s: %w", c.Path, err)
		}
		return writeFile(name, c.New)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return fmt.Errorf("patch %s: %w", c.Path, err)
	}
	if strings.Count(string(b), c.Old) != 1 {
		return fmt.Errorf("patch %s: old text found %d times", c.Path,
			strings.Count(string(b), c.Old))
	}
	return writeFile(name, strings.Replace(string(b), c.Old, c.New, 1))
}

func writeFile(name, text string) error {
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		return fmt.Errorf("patch: %w", err)
	}
	return nil
}

// A recording is one model call as the replaying transport saved it.
type recording struct {
	Request struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	} `json:"request"`
	Response string `json:"response_text"`
}

var callFile = regexp.MustCompile(`-(\d+)\.json$`)

// readRun returns the code_patch calls that went through, in order,
// and the arguments of the last submit_diagnosis, from every recorded
// call whose script starts with prefix: a context policy may have left
// early turns out of the later requests, or cleared their results, so
// a call's first real result is the one kept.
func readRun(dir, prefix string) ([]patchCall, string, error) {
	names, err := filepath.Glob(filepath.Join(dir, prefix+"*.json"))
	if err != nil {
		return nil, "", fmt.Errorf("recordings: %w", err)
	}
	type indexed struct {
		n    int
		name string
	}
	var files []indexed
	for _, name := range names {
		m := callFile.FindStringSubmatch(filepath.Base(name))
		if m == nil || strings.Contains(name, "-review-") {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		files = append(files, indexed{n, name})
	}
	if len(files) == 0 {
		return nil, "", errors.New("no recordings")
	}
	slices.SortFunc(files, func(a, b indexed) int { return cmp.Compare(a.n, b.n) })
	var order []string
	args := map[string]string{}
	results := map[string]string{}
	var last recording
	for _, f := range files {
		b, err := os.ReadFile(f.name)
		if err != nil {
			return nil, "", fmt.Errorf("recordings: %w", err)
		}
		last = recording{}
		if err := json.Unmarshal(b, &last); err != nil {
			return nil, "", fmt.Errorf("%s: %w", f.name, err)
		}
		for _, m := range last.Request.Messages {
			for _, tc := range m.ToolCalls {
				if tc.Function.Name != "code_patch" {
					continue
				}
				if _, seen := args[tc.ID]; !seen {
					order = append(order, tc.ID)
					args[tc.ID] = tc.Function.Arguments
				}
			}
			text := fmt.Sprint(m.Content)
			if _, seen := results[m.ToolCallID]; m.Role == "tool" && !seen &&
				!strings.Contains(text, "[cleared to save space:") {
				results[m.ToolCallID] = text
			}
		}
	}
	var calls []patchCall
	for _, id := range order {
		res, ok := results[id]
		if !ok || !applied.MatchString(res) {
			continue // refused, failed, or never answered
		}
		var c patchCall
		if err := json.Unmarshal([]byte(args[id]), &c); err != nil {
			return nil, "", fmt.Errorf("code_patch arguments: %w", err)
		}
		calls = append(calls, c)
	}
	final, err := submitted(last.Response)
	return calls, final, err
}

// applied matches code_patch's answer when it wrote the file: "changed
// PATH;" or "changed PATH, but", after the data marker's first line.
var applied = regexp.MustCompile(`(?m)^changed \S+(;|, but)`)

// submitted assembles the last streamed reply's submit_diagnosis
// arguments from its chunks, or "" if it made no such call.
func submitted(stream string) (string, error) {
	type delta struct {
		Index    int `json:"index"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	names := map[int]string{}
	parts := map[int]*strings.Builder{}
	sc := bufio.NewScanner(strings.NewReader(stream))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []delta `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // a keep-alive or an error chunk
		}
		for _, c := range chunk.Choices {
			for _, d := range c.Delta.ToolCalls {
				if d.Function.Name != "" {
					names[d.Index] = d.Function.Name
				}
				if parts[d.Index] == nil {
					parts[d.Index] = &strings.Builder{}
				}
				parts[d.Index].WriteString(d.Function.Arguments)
			}
		}
	}
	for _, i := range slices.Backward(slices.Sorted(maps.Keys(names))) {
		if names[i] == "submit_diagnosis" { // the last, if there are two
			return parts[i].String(), nil
		}
	}
	return "", nil // no answer: the record's own checks failed it
}
