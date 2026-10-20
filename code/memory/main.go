// Command memory gives an assistant a memory between sessions and
// compares three ways of carrying what a user said from one session
// to the next: nothing (none), the whole conversation (history), and
// notes the agent saves and recalls by meaning (memory). The assistant
// answers questions about shortlink, a small made-up service, from its
// documentation; across eight sessions the user mentions their own
// setup, changes it, and pastes a log line that asks to be remembered.
//
//	go run ./memory                      compare, from a recorded run
//	go run ./memory -store notes.json "Our proxy is Caddy at 10.0.0.2."
package main

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
)

const (
	defaultModel = "xiaomi/mimo-v2.6-pro"
	embedModel   = "voyageai/voyage-4-lite"
)

// The documents the assistant answers from, and the recorded run that
// lets the comparison run offline.
//
//go:embed testdata/docs testdata/run.json
var testdata embed.FS

func main() {
	store := flag.String("store", "",
		"run one session with the notes in this file, saving them after")
	record := flag.String("record", "",
		"with a key, save the run's API calls to this file")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Getenv, *store, strings.Join(flag.Args(), " "),
		*record, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, getenv func(string) string, store,
	say, record string, out io.Writer) error {
	c := &Client{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  getenv("OPENROUTER_API_KEY"),
		Model:   cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
	}
	var rec *recorder
	switch {
	case c.APIKey == "" && store != "":
		return errors.New("set OPENROUTER_API_KEY to run a session; " +
			"without it, the program replays the recorded comparison")
	case c.APIKey == "":
		r, err := loadReplay(testdata, "testdata/run.json")
		if err != nil {
			return err
		}
		c.HTTP = &http.Client{Transport: r}
	case record != "":
		rec = &recorder{next: http.DefaultTransport}
		c.HTTP = &http.Client{Transport: rec}
	}
	docs, err := fs.Sub(testdata, "testdata/docs")
	if err != nil {
		return fmt.Errorf("docs: %w", err)
	}
	if store != "" {
		err = oneSession(ctx, c, docs, store, say, out)
	} else {
		err = compare(ctx, c, docs, out)
	}
	if rec != nil {
		err = errors.Join(err, rec.save(record))
	}
	return err
}

// oneSession runs a session with the notes in a file, as a real
// assistant would between conversations, and saves them after.
func oneSession(ctx context.Context, c *Client, docs fs.FS, path,
	say string, out io.Writer) error {
	if say == "" {
		return errors.New("-store needs a message")
	}
	s, err := loadStore(path)
	if err != nil {
		return err
	}
	m := &memory{c: c, model: embedModel, store: s, session: 1}
	for _, n := range s.Notes {
		m.session = max(m.session, n.Session+1)
	}
	answer, err := askWithMemory(ctx, c, m, docs, say)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n\n%d notes; %d calls, $%.4f\n", answer,
		len(s.Notes), c.Calls, c.Spent.Cost)
	return s.save(path)
}

// tag::compare[]

// compare runs every session with every approach, in order, and
// charges each approach for its own calls. It reports the input tokens
// of each session too, which show what carrying history costs as it
// grows.
func compare(ctx context.Context, c *Client, docs fs.FS,
	out io.Writer) error {
	list, mem := approaches(c, docs)
	fmt.Fprintf(out, "%-8s %7s %6s %10s %7s %9s\n", "approach",
		"correct", "calls", "tokens in", "out", "cost")
	perSession := make([][]int, len(list))
	for ai, a := range list {
		calls, spent := c.Calls, c.Spent
		var correct int
		var missed []int
		for i, s := range sessions {
			before := c.Spent.PromptTokens
			answer, err := a.Ask(ctx, i+1, s.Say)
			if err != nil {
				return fmt.Errorf("%s, session %d: %w", a.Name, i+1, err)
			}
			perSession[ai] = append(perSession[ai],
				c.Spent.PromptTokens-before)
			if grade(s.Want, answer) {
				correct++
			} else {
				missed = append(missed, i+1)
			}
		}
		fmt.Fprintf(out, "%-8s %4d/%-2d %6d %10d %7d %9s",
			a.Name, correct, len(sessions), c.Calls-calls,
			c.Spent.PromptTokens-spent.PromptTokens,
			c.Spent.CompletionTokens-spent.CompletionTokens,
			fmt.Sprintf("$%.4f", c.Spent.Cost-spent.Cost))
		if len(missed) > 0 {
			fmt.Fprintf(out, "  missed %v", missed)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "\ninput tokens in each session:\n")
	for ai, a := range list {
		fmt.Fprintf(out, "%-8s", a.Name)
		for _, n := range perSession[ai] {
			fmt.Fprintf(out, " %6d", n)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "\nmemory's notes after session %d:\n", len(sessions))
	for _, n := range mem.store.Notes {
		fmt.Fprintf(out, "  note %d (session %d): %s\n", n.ID, n.Session,
			n.Text)
	}
	stale, planted := storeChecks(mem.store)
	fmt.Fprintf(out, "a note still gives the old proxy address: %s\n",
		yesNo(stale))
	fmt.Fprintf(out, "the log line's instruction was saved: %s\n",
		yesNo(planted))
	return nil
}

// end::compare[]

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
