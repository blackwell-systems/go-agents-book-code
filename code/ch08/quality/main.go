// Command quality is chapter 8's prompt-quality check: it runs the
// triage agent on five catalog tasks, several times each, under each
// version of the system prompt, and reports how often each version
// found the right root cause and passed the catalog's grade. One
// version changes one thing, so a difference can be traced to it.
//
// With OPENROUTER_API_KEY set it calls the live API (and -record saves
// the calls); without it, it replays the saved calls, while the tools,
// including go test, run for real.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch08/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch08/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch08/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch08/triage"
)

// defaultTasks are the check's five tasks: one where the fix at the
// crash site is wrong (m2), one misleading (m1), one with several
// causes in one batch (g3), one crash from an older version (f2), and
// one direct (d1).
var defaultTasks = []string{"m2-unknown-zone-list", "m1-done-after-delete",
	"g3-mixed-batch", "f2-old-digest-eighth-day", "d1-empty-snooze"}

const instruction = "Triage the crash reports: group them by cause, " +
	"then investigate, fix, and file each cause that isn't filed yet."

// config holds the program's settings, from its flags.
type config struct {
	repo     string
	model    string
	record   string
	replays  string // the directory of saved calls, when replaying
	out      string // a JSON Lines file of results, if set
	variants []string
	tasks    []string
	runs     int
	maxTurns int
	maxCost  float64
}

func main() {
	var c config
	var variants, tasks string
	flag.StringVar(&c.repo, "repo", ".", "the book's code directory")
	flag.StringVar(&c.model, "model", "anthropic/claude-haiku-5.5", "the model")
	flag.StringVar(&c.record, "record", "", "save each call to this directory")
	flag.StringVar(&c.replays, "replays", "ch08/quality/testdata/replies",
		"saved calls to replay when no key is set")
	flag.StringVar(&c.out, "out", "", "append each run's result to this JSON Lines file")
	flag.StringVar(&variants, "variants", strings.Join(variantNames(), ","),
		"prompt versions to compare, comma-separated")
	flag.StringVar(&tasks, "tasks", strings.Join(defaultTasks, ","),
		"catalog tasks, comma-separated")
	flag.IntVar(&c.runs, "runs", 5, "runs per task and version")
	flag.IntVar(&c.maxTurns, "max-turns", 40, "turn limit per run")
	flag.Float64Var(&c.maxCost, "max-cost", 0.25, "cost budget per run, in dollars")
	flag.Parse()
	c.variants, c.tasks = strings.Split(variants, ","), strings.Split(tasks, ",")
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	if err := check(ctx, os.Getenv("OPENROUTER_API_KEY"), os.Stdout, c); err != nil {
		log.Fatal(err)
	}
}

// tag::result[]

// A result is one run's outcome.
type result struct {
	Variant   string   `json:"variant"`
	Task      string   `json:"task"`
	Run       int      `json:"run"`
	RootCause bool     `json:"root_cause"` // every root cause right
	Passed    bool     `json:"passed"`     // every check made
	Turns     int      `json:"turns"`
	Cost      float64  `json:"cost"`
	Err       string   `json:"err,omitzero"`    // stopped early
	Failed    []string `json:"failed,omitzero"` // checks failed

	network bool // the run stopped because a connection failed
}

// end::result[]

// check runs every task under every version, runs times, and prints a
// table comparing the versions.
func check(ctx context.Context, key string, w io.Writer, c config) error {
	for _, v := range c.variants {
		if _, ok := variants[v]; !ok {
			return fmt.Errorf("no prompt version %q; the versions are %s",
				v, strings.Join(variantNames(), ", "))
		}
	}
	client := newClient(key, c)
	tasks, err := taskrun.Load(c.repo)
	if err != nil {
		return fmt.Errorf("quality check: %w", err)
	}
	var results []result
	for _, v := range c.variants {
		for _, id := range c.tasks {
			task, err := taskrun.Find(tasks, id)
			if err != nil {
				return fmt.Errorf("quality check: %w", err)
			}
			for run := 1; run <= c.runs; run++ {
				r, err := runOnce(ctx, client, c, task, v, run)
				for try := 1; err == nil && r.network && try < 3; try++ {
					// A connection that failed says nothing about the
					// prompt, so the run starts over (chapter 12 retries
					// the call instead). A new client starts the saved
					// calls over too.
					client = newClient(key, c)
					r, err = runOnce(ctx, client, c, task, v, run)
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "%s %s %d: root cause %v, passed %v, %d turns, $%.4f %s\n",
					v, id, run, r.RootCause, r.Passed, r.Turns, r.Cost, r.Err)
				results = append(results, r)
				if err := appendResult(c.out, r); err != nil {
					return err
				}
			}
		}
	}
	_, err = io.WriteString(w, "\n"+summary(results, c.variants))
	if err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

// tag::run-once[]

// runOnce runs the agent once on a task under one prompt version and
// grades what it did. A run that stops without an answer is a result,
// graded as having diagnosed nothing; only a failure to set up or grade
// is an error.
func runOnce(ctx context.Context, m agent.StreamModel, c config,
	task catalog.Task, variant string, run int) (result, error) {
	r := result{Variant: variant, Task: task.ID, Run: run}
	j, err := taskrun.SetUp(ctx, c.repo, task)
	if err != nil {
		return r, fmt.Errorf("%s: %w", task.ID, err)
	}
	defer j.Close()
	ctx = replay.WithScript(ctx, scriptName(variant, task.ID, run))
	msgs := []agent.Message{
		{Role: "system", Content: variants[variant](basePrompt)},
		{Role: "user", Content: instruction},
	}
	res, runErr := agent.RunStream(ctx, m, triage.Tools(j.Env), msgs,
		agent.Limits{MaxTurns: c.maxTurns, MaxCost: c.maxCost})
	if errors.Is(runErr, context.Canceled) {
		return r, fmt.Errorf("%s: %w", task.ID, runErr)
	}
	if runErr != nil {
		r.Err = runErr.Error()
		r.network = connectionFailed(runErr)
	}
	r.Cost = res.Usage.Cost
	for _, msg := range res.Messages {
		if msg.Role == "assistant" {
			r.Turns++
		}
	}
	out, err := j.Grade(ctx, res.Final)
	if err != nil {
		return r, fmt.Errorf("%s: %w", task.ID, err)
	}
	for _, c := range out.Grade.Failed() {
		r.Failed = append(r.Failed, c.Name)
	}
	r.Passed = res.Final != nil && out.Grade.Passed()
	r.RootCause = res.Final != nil && rootCausesRight(out.Grade.Checks)
	return r, nil
}

// end::run-once[]

// connectionFailed reports whether err is a failed connection. An
// *url.Error also wraps a call with no saved response and a timeout,
// and running again would only repeat those.
func connectionFailed(err error) bool {
	_, ok := errors.AsType[*url.Error](err)
	return ok && !errors.Is(err, replay.ErrNoRecording) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, context.Canceled)
}

// scriptName names a run's saved calls.
func scriptName(variant, task string, run int) string {
	return fmt.Sprintf("%s-%s-%02d", variant, task, run)
}

// newClient returns a client for the live API with a key, recording if
// asked, or one that replays saved calls without one.
func newClient(key string, c config) *openrouter.Client {
	client := &openrouter.Client{APIKey: key, Model: c.model}
	switch {
	case key == "":
		client.HTTP = &http.Client{Transport: &replay.Transport{
			Files: os.DirFS(filepath.Join(c.repo, c.replays))}}
	case c.record != "":
		client.HTTP = &http.Client{Transport: &replay.Recorder{Dir: c.record}}
	}
	return client
}

// appendResult adds a result to a JSON Lines file, if one was named.
func appendResult(file string, r result) error {
	if file == "" {
		return nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("save result: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save result: %w", err)
	}
	return nil
}

// tag::summary[]

// summary is a table with a row per prompt version: of its runs, how
// many found every root cause and how many passed every check, with
// the median turns and the mean cost.
func summary(results []result, order []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-12s %-11s %-11s %6s %9s\n", "version",
		"root cause", "passed", "turns", "cost")
	for _, v := range order {
		var n, cause, pass int
		var turns []int
		var cost float64
		for _, r := range results {
			if r.Variant != v {
				continue
			}
			n++
			cause += boolInt(r.RootCause)
			pass += boolInt(r.Passed)
			turns = append(turns, r.Turns)
			cost += r.Cost
		}
		if n == 0 {
			continue
		}
		slices.Sort(turns)
		fmt.Fprintf(&b, "%-12s %-11s %-11s %6d %9s\n", v,
			fmt.Sprintf("%d of %d", cause, n), fmt.Sprintf("%d of %d", pass, n),
			turns[len(turns)/2], fmt.Sprintf("$%.4f", cost/float64(n)))
	}
	return b.String()
}

// end::summary[]

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// variantNames lists the prompt versions, in the order they're reported.
func variantNames() []string {
	return []string{"v1", "duplicates", "old-versions", "crash-site", "all"}
}
