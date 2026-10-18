// Command ch05 runs the hardened agent: a model with four tools fixes
// the failing test in the duration project. With OPENROUTER_API_KEY set
// it calls the live API; without it, it replays a saved run, while the
// tools, including go test, still run for real.
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
	"path"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch05/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch05/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch05/replay"
)

//go:embed testdata/duration testdata/replies
var embedded embed.FS

const (
	defaultModel = "anthropic/claude-haiku-5.5"
	task         = "The tests in this project fail. Find out why and fix it."
)

func main() {
	var c config
	flag.StringVar(&c.record, "record", "",
		"save each call to this directory, to replay later")
	flag.StringVar(&c.script, "replay", "fix",
		"the saved run to replay when no key is set; see testdata/replies")
	flag.StringVar(&c.save, "save", "",
		"write the conversation to this file as JSON")
	flag.IntVar(&c.maxTurns, "max-turns", 20,
		"stop the run after this many turns")
	flag.Float64Var(&c.maxCost, "max-cost", 0.05,
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
	script   string  // the saved run to replay, and the name to record under
	record   string  // a directory to record calls to
	save     string  // a file to save the conversation to
	maxTurns int     // the run's turn limit
	maxCost  float64 // the run's cost budget, in dollars

	// wrap, if set, wraps the model; tests use it to act partway through
	// a run, such as cancelling it.
	wrap func(agent.Model) agent.Model
}

// tag::run[]

// run sets up the model, runs the agent on the duration project, and
// prints the conversation, why the run stopped, and what it cost.
func run(ctx context.Context, getenv func(string) string, w io.Writer,
	c config) error {
	model, err := newClient(getenv, w, c.record)
	if err != nil {
		return err
	}
	var m agent.Model = model
	if c.wrap != nil {
		m = c.wrap(m)
	}
	ctx = replay.WithScript(ctx, c.script)
	lim := agent.Limits{
		MaxTurns: cmp.Or(c.maxTurns, 20),
		MaxCost:  c.maxCost,
	}
	// end::run[]
	// tag::run-agent[]
	res, files, runErr := fix(ctx, m, lim)
	if err := printConversation(w, res.Messages); err != nil {
		return err
	}
	if c.save != "" {
		if err := saveConversation(c.save, res.Messages); err != nil {
			return err
		}
	}
	// end::run-agent[]
	// tag::run-report[]
	p := &printer{w: w}
	p.printf("\nstopped: %s\n", whyStopped(runErr)) // <1>
	if runErr != nil {
		p.printf("error: %v\n", runErr)
	}
	u := res.Usage // <2>
	p.printf("%d tokens in, %d out, $%.4f\n", u.PromptTokens,
		u.CompletionTokens, u.Cost)
	if files["duration.go"] != files0()["duration.go"] {
		p.printf("duration.go was changed.\n")
	}
	if p.err != nil {
		return p.err
	}
	if runErr != nil {
		return &stoppedError{runErr}
	}
	return nil
}

// A stoppedError is a run that ended early, which run has already
// reported; main only sets the exit status.
type stoppedError struct{ err error }

func (e *stoppedError) Error() string { return e.err.Error() }
func (e *stoppedError) Unwrap() error { return e.err }

// end::run-report[]

// tag::fix[]

// fix runs the agent on a fresh copy of the duration project and
// returns the run and the files as the agent left them.
func fix(ctx context.Context, m agent.Model, lim agent.Limits) (
	agent.Result, agent.FS, error) {
	files := files0()
	msgs := []agent.Message{{Role: "user", Content: task}}
	res, err := agent.Run(ctx, m, agent.Tools(files), msgs, lim)
	if err != nil {
		return res, files, fmt.Errorf("run agent: %w", err)
	}
	return res, files, nil
}

// end::fix[]

// files0 returns the duration project, with its bug.
func files0() agent.FS {
	files := agent.FS{}
	entries, err := fs.ReadDir(embedded, "testdata/duration")
	if err != nil {
		panic(err) // embedded at build time, so this is a programmer error
	}
	for _, e := range entries {
		b, err := fs.ReadFile(embedded, path.Join("testdata/duration", e.Name()))
		if err != nil {
			panic(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

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
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, openrouter.ErrCallTimeout):
		return "the model call timed out, worth retrying"
	case errors.Is(err, context.DeadlineExceeded):
		return "a deadline passed"
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
