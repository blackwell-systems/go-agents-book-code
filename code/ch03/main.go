// Command ch03 runs chapter 3's experiments on how a model works: a toy
// language model, a tokenizer, embeddings, attention, a chat template,
// and a real model's token probabilities. Name experiments to run only
// those (go run ./ch03 toy bpe); with no names it runs them all.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
)

//go:embed testdata/corpus.txt testdata/replies testdata/duration.go
var files embed.FS

// probeModel returns token probabilities through at least one provider
// on OpenRouter; Claude models don't report them.
const probeModel = "meta-llama/llama-3.1-8b-instruct"

// An experiment writes its results to p. Only the probe calls a model.
type experiment struct {
	name string
	run  func(ctx context.Context, p *printer) error
}

var experiments = []experiment{
	{"toy", runToy},
	{"bpe", runBPE},
	{"network", runNetwork},
	{"attention", runAttention},
	{"template", runTemplate},
	{"probe", runProbe},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	if err := run(ctx, os.Stdout, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// run runs the named experiments, or all of them, in order.
func run(ctx context.Context, w io.Writer, names []string) error {
	p := &printer{w: w}
	ran := 0
	for _, e := range experiments {
		if len(names) > 0 && !slices.Contains(names, e.name) {
			continue
		}
		if ran > 0 {
			p.printf("\n")
		}
		p.printf("== %s\n\n", e.name)
		if err := e.run(ctx, p); err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
		ran++
	}
	if ran == 0 {
		return fmt.Errorf("no experiment named %s", strings.Join(names, ", "))
	}
	return p.err
}

// printer writes formatted output and keeps the first write error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

// wrap joins words into lines of at most 70 characters.
func wrap(words []string) string {
	var b strings.Builder
	n := 0
	for i, w := range words {
		switch {
		case i == 0:
		case n+1+len(w) > 70:
			b.WriteByte('\n')
			n = 0
		default:
			b.WriteByte(' ')
			n++
		}
		b.WriteString(w)
		n += len(w)
	}
	return b.String()
}
