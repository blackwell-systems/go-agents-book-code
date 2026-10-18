// Command compare runs chapter 16's task three ways, on the book's
// agent package, on Eino, and on Genkit, against the same scripted
// replies, and prints what each did.
//
// Usage:
//
//	go run ./compare [-bodies] [-cancel] [-sizes] [-live dir]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/eino"
	"github.com/blackwell-systems/go-agents-book/code/ch16/genkit"
	"github.com/blackwell-systems/go-agents-book/code/ch16/harness"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// A version is one way of running the task.
type version struct {
	Name string
	Dir  string // its package, and its command under cmd/
	Run  func(context.Context, task.Config, *task.Env) (task.Outcome,
		error)
}

var versions = []version{
	{"Book's agent", "harness", harness.Run},
	{"Eino", "eino", eino.Run},
	{"Genkit", "genkit", genkit.Run},
}

var scripts = []string{"diagnose", "tool-error", "retry"}

// A run is what one version did with one script.
type run struct {
	Bodies  [][]byte // request bodies, in order
	Outcome task.Outcome
	Report  task.Report
	Err     error
}

// tag::replayed[]

// replayed runs v on one script and keeps what it sent.
func replayed(ctx context.Context, v version, script string) run {
	c, w := task.Replay()
	env := &task.Env{}
	out, err := v.Run(replay.WithScript(ctx, script), c, env)
	return run{Bodies: w.Bodies(), Outcome: out, Report: env.Report(),
		Err: err}
}

// end::replayed[]

func main() {
	bodies := flag.Bool("bodies", false,
		"print each version's requests for the diagnose script")
	record := flag.String("live", "",
		"run each version once against OpenRouter, recording to this "+
			"directory")
	cancel := flag.Bool("cancel", false,
		"cancel each version 100ms into the retry script and time it")
	sizesFlag := flag.Bool("sizes", false,
		"build each version for linux/amd64 and print the binary sizes")
	flag.Parse()
	ctx := context.Background()
	var err error
	switch {
	case *record != "":
		err = live(ctx, os.Stdout, *record)
	case *sizesFlag:
		err = sizes(ctx, os.Stdout, ".")
	case *cancel:
		err = cancelled(ctx, os.Stdout, 100*time.Millisecond)
	case *bodies:
		err = printBodies(ctx, os.Stdout)
	default:
		err = table(ctx, os.Stdout, ".")
	}
	if err != nil {
		log.Fatal(err)
	}
}

// printBodies prints every request each version sent for one script.
func printBodies(ctx context.Context, out io.Writer) error {
	for _, v := range versions {
		r := replayed(ctx, v, "diagnose")
		for i, b := range r.Bodies {
			fmt.Fprintf(out, "--- %s, request %d\n%s\n", v.Name, i+1, b)
		}
	}
	return nil
}
