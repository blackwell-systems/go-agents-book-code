// Command firstagent is the smallest complete agent: it answers a
// question about the files in one directory, reading them with two
// tools, in standard-library Go. With OPENROUTER_API_KEY set it calls
// a live model; without it, it replays a recorded run, and the tools
// still read the files for real.
//
//	go run ./firstagent
//	go run ./firstagent -dir ~/notes "What did I decide about the cache?"
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
	defaultModel    = "xiaomi/mimo-v2.6-pro"
	defaultQuestion = "Which release added rate limiting, " +
		"and what is the default limit?"
)

// The sample project the default question is about, and the recorded
// run that answers it offline.
//
//go:embed testdata/project testdata/run.json
var testdata embed.FS

// tag::prompt[]

const systemPrompt = `You answer questions about the files in one
directory. Use list_files to see what is there and read_file to read
a file. Answer only from what the files say, name the files you used,
and if the files don't answer the question, say so.`

// end::prompt[]

func main() {
	dir := flag.String("dir", "",
		"the directory to read (default: a small sample project)")
	record := flag.String("record", "",
		"with a key, save the run's API calls to this file")
	flag.Parse()
	question := cmp.Or(strings.Join(flag.Args(), " "), defaultQuestion)

	// Ctrl-C cancels ctx, which stops the model call in progress.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Getenv, *dir, *record, question,
		os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// tag::main[]

// run sets up the client, the tools, and the conversation, and runs
// the agent.
func run(ctx context.Context, getenv func(string) string, dir, record,
	question string, out io.Writer) error {
	c := &Client{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  getenv("OPENROUTER_API_KEY"),
		Model:   cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
	}
	rec, err := setTransport(c, record, question)
	if err != nil {
		return err
	}
	files, closeFiles, err := openDir(dir)
	if err != nil {
		return err
	}
	defer closeFiles()

	msgs := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: question},
	}
	fmt.Fprintf(out, "question: %s\n", question)
	answer, msgs, err := Run(ctx, c, fileTools(files), msgs, 10, out)
	if rec != nil {
		err = errors.Join(err, rec.save(record))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s\n\n%d messages\n", answer, len(msgs))
	return nil
}

// end::main[]

// setTransport makes the client replay the recorded run when there is
// no key, and record the run when asked to. It returns the recorder,
// or nil.
func setTransport(c *Client, record, question string) (*recorder,
	error) {
	switch {
	case c.APIKey == "" && question != defaultQuestion:
		return nil, fmt.Errorf("set OPENROUTER_API_KEY to ask your " +
			"own question; the recorded run answers only the default one")
	case c.APIKey == "":
		r, err := loadReplay(testdata, "testdata/run.json")
		if err != nil {
			return nil, err
		}
		c.HTTP = &http.Client{Transport: r}
		return nil, nil
	case record != "":
		rec := &recorder{next: http.DefaultTransport}
		c.HTTP = &http.Client{Transport: rec}
		return rec, nil
	}
	return nil, nil
}

// openDir returns the files the agent may read: dir through an
// os.Root, or the embedded sample project when dir is empty.
func openDir(dir string) (fs.FS, func(), error) {
	if dir == "" {
		sub, err := fs.Sub(testdata, "testdata/project")
		if err != nil {
			return nil, nil, fmt.Errorf("sample project: %w", err)
		}
		return sub, func() {}, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return root.FS(), func() { root.Close() }, nil
}
