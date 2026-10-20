// Command rag answers questions about a small set of documents three
// ways and compares them on the same questions: retrieval-augmented
// generation in one call (rag), retrieval as a tool the agent calls
// (rag-tool), and an agent that lists and reads the files (files). It
// is standard-library Go; the chunks are embedded through OpenRouter's
// embeddings endpoint, with the same key as the chat calls.
//
//	go run ./rag                      compare, from a recorded run
//	go run ./rag -ask rag "Which release added link expiry?"
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

// The documents the questions are about, and the recorded run that
// lets the comparison run offline.
//
//go:embed testdata/docs testdata/run.json
var testdata embed.FS

func main() {
	ask := flag.String("ask", "",
		"answer one question with this approach: rag, rag-tool, or files")
	record := flag.String("record", "",
		"with a key, save the run's API calls to this file")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Getenv, *ask, strings.Join(flag.Args(), " "),
		*record, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, getenv func(string) string, ask, question,
	record string, out io.Writer) error {
	c := &Client{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  getenv("OPENROUTER_API_KEY"),
		Model:   cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
	}
	var rec *recorder
	switch {
	case c.APIKey == "" && ask != "":
		return errors.New("set OPENROUTER_API_KEY to ask a question; " +
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
	if ask != "" {
		err = askOne(ctx, c, docs, ask, question, out)
	} else {
		err = compare(ctx, c, docs, out)
	}
	if rec != nil {
		err = errors.Join(err, rec.save(record))
	}
	return err
}

// askOne answers a single question with one approach.
func askOne(ctx context.Context, c *Client, docs fs.FS, name,
	question string, out io.Writer) error {
	if question == "" {
		return errors.New("-ask needs a question")
	}
	ix, err := buildIndex(ctx, c, embedModel, docs)
	if err != nil {
		return err
	}
	for _, a := range approaches(c, ix, embedModel, docs) {
		if a.Name == name {
			answer, err := a.Ask(ctx, question)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\n\n%d calls, $%.4f\n", answer, c.Calls,
				c.Spent.Cost)
			return nil
		}
	}
	return fmt.Errorf("no approach %q: use rag, rag-tool, or files", name)
}

// tag::compare[]

// compare builds the index once, then asks every question with every
// approach and charges each approach for its own calls.
func compare(ctx context.Context, c *Client, docs fs.FS,
	out io.Writer) error {
	ix, err := buildIndex(ctx, c, embedModel, docs)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "index: %d chunks, $%.5f to embed\n\n",
		len(ix.Chunks), c.Spent.Cost)
	fmt.Fprintf(out, "%-9s %7s %6s %10s %8s %10s\n",
		"approach", "correct", "calls", "tokens in", "out", "cost")
	for _, a := range approaches(c, ix, embedModel, docs) {
		calls, spent := c.Calls, c.Spent
		correct, missed := 0, []int{}
		for i, q := range questions {
			answer, err := a.Ask(ctx, q.Q)
			if err != nil {
				return fmt.Errorf("%s, question %d: %w", a.Name, i+1, err)
			}
			if grade(q, answer) {
				correct++
			} else {
				missed = append(missed, i+1)
			}
		}
		fmt.Fprintf(out, "%-9s %4d/%-2d %6d %10d %8d %10s",
			a.Name, correct, len(questions), c.Calls-calls,
			c.Spent.PromptTokens-spent.PromptTokens,
			c.Spent.CompletionTokens-spent.CompletionTokens,
			fmt.Sprintf("$%.4f", c.Spent.Cost-spent.Cost))
		if len(missed) > 0 {
			fmt.Fprintf(out, "  missed %v", missed)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// end::compare[]
