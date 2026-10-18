// Command ch07 runs the crash-triage agent on one task from the crash
// catalog: it builds a copy of remind at the task's version, gives the
// agent the task's crash reports and a tracker holding the issues
// already filed, and prints what the agent did, its diagnosis, and how
// the catalog grades it. With OPENROUTER_API_KEY set it calls the live
// API; without it, it replays a saved run, while the tools, including
// go test, still run for real.
package main

import (
	"cmp"
	"context"
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch07/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch07/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch07/triage"
)

//go:embed testdata/replies
var embedded embed.FS

const (
	defaultModel = "anthropic/claude-haiku-5.5"
	instruction  = "Triage the crash reports: group them by cause, " +
		"then investigate, fix, and file each cause that isn't filed yet."
)

func main() {
	var c config
	flag.StringVar(&c.task, "task", "m1-done-after-delete",
		"the catalog task to run; see catalog/testdata/tasks")
	flag.StringVar(&c.repo, "repo", ".",
		"the directory holding remind and catalog (the book's code directory)")
	flag.StringVar(&c.record, "record", "",
		"save each call to this directory, to replay later")
	flag.StringVar(&c.save, "save", "",
		"write the conversation to this file as JSON")
	flag.IntVar(&c.maxTurns, "max-turns", 40,
		"stop the run after this many turns")
	flag.Float64Var(&c.maxCost, "max-cost", 0.25,
		"stop the run once it has spent this many dollars; 0 for no limit")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	err := run(ctx, os.Getenv, os.Stdout, c)
	if _, ok := errors.AsType[*stoppedError](err); ok {
		os.Exit(1) // run has printed why
	}
	if err != nil {
		log.Fatal(err)
	}
}

// config holds the program's settings, from its flags.
type config struct {
	task     string  // the catalog task, and the saved run's name
	repo     string  // the directory holding remind and catalog
	record   string  // a directory to record calls to
	save     string  // a file to save the conversation to
	maxTurns int     // the run's turn limit
	maxCost  float64 // the run's cost budget, in dollars
}

// tag::run[]

// run sets up the task, runs the agent on it, and prints what it did,
// its answer, the issues it filed, and the catalog's grade.
func run(ctx context.Context, getenv func(string) string, w io.Writer,
	c config) error {
	model, err := newClient(getenv, w, c.record)
	if err != nil {
		return err
	}
	j, err := setUp(ctx, c.repo, c.task)
	if err != nil {
		return err
	}
	defer j.close()
	ctx = replay.WithScript(ctx, c.task)
	lim := agent.Limits{MaxTurns: cmp.Or(c.maxTurns, 40), MaxCost: c.maxCost}
	msgs := []agent.Message{
		{Role: "system", Content: triage.Prompt}, // <1>
		{Role: "user", Content: instruction},
	}
	p := &printer{w: w}
	p.printf("task: %s, %d crash reports, %d issues already filed\n\nturn 1\n",
		j.task.ID, len(j.env.Reports), len(j.task.Known))
	res, runErr := watch(ctx, model, triage.Tools(j.env), msgs, lim, p)
	if c.save != "" {
		if err := saveConversation(c.save, res.Messages); err != nil {
			return err
		}
	}
	p.printf("\nstopped: %s\n", whyStopped(runErr))
	if runErr != nil {
		p.printf("error: %v\n", runErr)
	}
	u := res.Usage
	p.printf("%d tokens in, %d out, $%.4f\n", u.PromptTokens,
		u.CompletionTokens, u.Cost)
	if res.Final != nil { // <2>
		if err := j.report(ctx, p, res.Final); err != nil {
			return err
		}
	}
	if p.err != nil {
		return p.err
	}
	if runErr != nil {
		return &stoppedError{runErr}
	}
	return nil
}

// end::run[]

// A stoppedError is a run that ended early, which run has already
// reported; main only sets the exit status.
type stoppedError struct{ err error }

func (e *stoppedError) Error() string { return e.err.Error() }
func (e *stoppedError) Unwrap() error { return e.err }

// tag::watch[]

// watch runs the agent and prints what happens as it happens: the
// model's text as it is written, each tool call, and each result.
func watch(ctx context.Context, m agent.StreamModel, tools []agent.Tool,
	msgs []agent.Message, lim agent.Limits, p *printer) (
	agent.Result, error) {
	text := &textWriter{p: p}
	next := 0 // a turn whose heading waits for its first event
	for ev := range agent.Events(ctx, m, tools, msgs, lim) {
		if next > 0 && (ev.Kind == agent.TextEvent ||
			ev.Kind == agent.CallEvent) {
			p.printf("\nturn %d\n", next)
			next = 0
		}
		switch ev.Kind {
		case agent.TextEvent:
			text.write(ev.Text) // <1>
		case agent.CallEvent:
			text.end()
			printCall(p, ev.Call)
		case agent.ToolResultEvent:
			printResult(p, ev.Tool)
		case agent.TurnEvent:
			next = ev.Turn + 1 // ev.Turn turns have ended
		case agent.StoppedEvent:
			if text.end() && ev.Err != nil { // <2>
				p.printf("  (cut off: not in the conversation)\n")
			}
			if ev.Err != nil {
				return ev.Result, fmt.Errorf("run agent: %w", ev.Err)
			}
			return ev.Result, nil
		}
	}
	return agent.Result{}, errors.New("run agent: no stop event")
}

// end::watch[]

// newClient returns a client for the live API with a key, or one that
// replays the saved runs without one.
func newClient(getenv func(string) string, w io.Writer,
	record string) (*openrouter.Client, error) {
	c := &openrouter.Client{
		APIKey:  getenv("OPENROUTER_API_KEY"),
		Model:   cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
		BaseURL: getenv("OPENROUTER_BASE_URL"),
	}
	switch {
	case c.APIKey == "":
		files, err := fs.Sub(embedded, "testdata/replies")
		if err != nil {
			return nil, fmt.Errorf("saved replies: %w", err)
		}
		c.HTTP = &http.Client{Transport: &replay.Transport{Files: files}}
		if _, err := fmt.Fprintln(w, "No OPENROUTER_API_KEY: "+
			"replaying a saved run. The tools run for real."); err != nil {
			return nil, fmt.Errorf("write output: %w", err)
		}
	case record != "":
		c.HTTP = &http.Client{Transport: &replay.Recorder{Dir: record}}
	}
	return c, nil
}

// tag::save[]

// saveConversation writes msgs to a file as indented JSON, to read or
// load later. It works after a failed run too: the messages up to the
// failure are often what you need to see.
func saveConversation(file string, msgs []agent.Message) error {
	b, err := json.Marshal(savable(msgs), jsontext.Multiline(true),
		json.OmitZeroStructFields(true))
	if err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	return nil
}

// end::save[]

// savable returns msgs with any tool-call arguments that aren't valid
// JSON turned into a JSON string, so the file is valid JSON: a model can
// write broken arguments, and the conversation keeps them as sent.
func savable(msgs []agent.Message) []agent.Message {
	out := slices.Clone(msgs)
	for i, m := range out {
		copied := false
		for j, c := range m.ToolCalls {
			if len(c.Arguments) == 0 || c.Arguments.IsValid() {
				continue
			}
			if !copied { // never change the caller's messages
				out[i].ToolCalls, copied = slices.Clone(m.ToolCalls), true
			}
			// Invalid UTF-8 comes back replaced, which is fine here.
			q, _ := jsontext.AppendQuote(nil, c.Arguments)
			out[i].ToolCalls[j].Arguments = q
		}
	}
	return out
}

// tag::why-stopped[]

// whyStopped says in a few words why a run ended, from its error.
func whyStopped(err error) string {
	switch {
	case err == nil:
		return "the model answered"
	case errors.Is(err, agent.ErrMaxTurns):
		return "the turn limit"
	case errors.Is(err, agent.ErrBudgetExceeded):
		return "the cost budget"
	case errors.Is(err, openrouter.ErrStreamIdle): // before Canceled
		return "the stream went quiet, worth retrying"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, openrouter.ErrCallTimeout):
		return "the model call timed out, worth retrying"
	case errors.Is(err, context.DeadlineExceeded):
		return "a deadline passed"
	case errors.Is(err, agent.ErrNoFinalAnswer):
		return "the model answered without submitting a diagnosis"
	case errors.Is(err, agent.ErrBadReply):
		return "a reply the loop can't use"
	}
	if e, ok := errors.AsType[*openrouter.APIError](err); ok {
		if e.Retryable() {
			return fmt.Sprintf("API error %d, worth retrying", e.Status)
		}
		return fmt.Sprintf("API error %d", e.Status)
	}
	return "an error"
}

// end::why-stopped[]
