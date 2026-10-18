// Command ch04 runs the first real agent: a model with four tools fixes
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

	"github.com/blackwell-systems/go-agents-book/code/ch04/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch04/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch04/replay"
)

//go:embed testdata/duration testdata/replies
var embedded embed.FS

const (
	defaultModel = "anthropic/claude-haiku-5.5"
	task         = "The tests in this project fail. Find out why and fix it."
)

func main() {
	record := flag.String("record", "",
		"save each call to this directory, to replay later")
	saved := flag.String("replay", "fix",
		"the saved run to replay when no key is set: fix, fix-again, recover")
	save := flag.String("save", "",
		"write the conversation to this file as JSON")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	err := run(ctx, os.Getenv, os.Stdout, *saved, *record, *save)
	if err != nil {
		log.Fatal(err)
	}
}

// tag::run[]

// run sets up the model, runs the agent on the duration project, and
// prints the conversation and what it cost.
func run(ctx context.Context, getenv func(string) string, w io.Writer,
	script, record, save string) error {
	model, err := newClient(getenv, w, record)
	if err != nil {
		return err
	}
	ctx = replay.WithScript(ctx, script) // <1>
	m := &meter{Model: model}            // <2>
	res, files, err := fix(ctx, m)
	if perr := printConversation(w, res.Messages); perr != nil {
		return perr
	}
	if save != "" {
		if serr := saveConversation(save, res.Messages); serr != nil {
			return serr
		}
	}
	if err != nil {
		return err
	}
	p := &printer{w: w}
	p.printf("\n%d model calls, %d tokens in, %d out, $%.4f\n", m.calls,
		m.usage.PromptTokens, m.usage.CompletionTokens, m.usage.Cost)
	if files["duration.go"] != files0()["duration.go"] {
		p.printf("duration.go was changed.\n")
	}
	return p.err
}

// end::run[]

// tag::fix[]

// fix runs the agent on a fresh copy of the duration project and
// returns the run and the files as the agent left them.
func fix(ctx context.Context, m agent.Model) (agent.Result, agent.FS,
	error) {
	files := files0()
	msgs := []agent.Message{{Role: "user", Content: task}}
	lim := agent.Limits{MaxTurns: 20}
	res, err := agent.Run(ctx, m, agent.Tools(files), msgs, lim) // <1>
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

// tag::meter[]

// meter is a Model that passes each call on to another Model and adds
// up the calls, tokens, and cost.
type meter struct {
	agent.Model
	calls int
	usage agent.Usage
}

func (m *meter) Generate(ctx context.Context,
	req agent.Request) (agent.Reply, error) {
	reply, err := m.Model.Generate(ctx, req)
	if err != nil {
		return reply, err //nolint:wrapcheck // passed through unchanged
	}
	m.calls++
	m.usage.PromptTokens += reply.Usage.PromptTokens
	m.usage.CompletionTokens += reply.Usage.CompletionTokens
	m.usage.Cost += reply.Usage.Cost
	return reply, nil
}

// end::meter[]
