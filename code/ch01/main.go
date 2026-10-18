// Command ch01 runs chapter 1's agent loop on a scripted model: it
// replays the run shown at the start of the chapter, fixing a bug in
// the duration package, with no AI involved.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch01/agent"
)

//go:embed testdata/duration
var project embed.FS

const task = "The tests in this project fail. Find out why and fix it."

// tag::signal[]

func main() {
	// Ctrl-C cancels ctx, and the loop stops before its next turn.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	if err := run(ctx, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// end::signal[]

// tag::main[]
func run(ctx context.Context, w io.Writer) error {
	files, err := load(project, "testdata/duration")
	if err != nil {
		return err
	}
	msgs := []agent.Message{{Role: "user", Content: task}}
	model := &agent.Fake{Script: script}
	lim := agent.Limits{MaxTurns: 10}
	res, err := agent.Run(ctx, model, agent.Tools(files), msgs, lim)
	if perr := printConversation(w, res.Messages); perr != nil {
		return perr
	}
	if err != nil {
		return fmt.Errorf("run agent: %w", err)
	}
	return nil
}

// end::main[]

// load reads the files in dir into an FS.
func load(fsys fs.FS, dir string) (agent.FS, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("load project: %w", err)
	}
	files := agent.FS{}
	for _, e := range entries {
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("load project: %w", err)
		}
		files[e.Name()] = string(b)
	}
	return files, nil
}

// printer writes formatted lines and keeps the first write error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

// printConversation prints the run turn by turn: what the model asked
// for, and what the harness did about it.
func printConversation(w io.Writer, msgs []agent.Message) error {
	p := &printer{w: w}
	turn := 0
	for _, m := range msgs {
		switch {
		case m.Role == "user":
			p.printf("task: %s\n", m.Content)
		case m.Role == "assistant":
			turn++
			p.printf("\nturn %d\n", turn)
			for _, c := range m.ToolCalls {
				printCall(p, c)
			}
			if m.Content != "" {
				p.printf("  model says:  %s\n", wrap(m.Content, 15))
			}
		case m.IsError:
			p.printf("  tool error:  %s\n", summary(m.Content))
		default:
			p.printf("  tool result: %s\n", summary(m.Content))
		}
	}
	if p.err != nil {
		return fmt.Errorf("print conversation: %w", p.err)
	}
	return nil
}

// printCall prints a tool call: short arguments on the same line,
// longer ones one per line.
func printCall(p *printer, c agent.ToolCall) {
	v := slices.Clone(c.Arguments)
	if err := v.Compact(); err != nil || len(v) <= 40 {
		p.printf("  model asks:  %s %s\n", c.Name, c.Arguments)
		return
	}
	p.printf("  model asks:  %s\n", c.Name)
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil { // the opening brace
		p.printf("                 %s\n", v)
		return
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return
		}
		key := tok.String() // read now: the next call voids tok
		val, err := dec.ReadValue()
		if err != nil {
			return
		}
		p.printf("                 %s: %s\n", key, val)
	}
}

// summary shortens tool output to its first line and a line count.
func summary(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= 3 && len(s) < 50 {
		return strings.Join(lines, ", ")
	}
	first := lines[0]
	if r := []rune(first); len(r) > 40 {
		first = string(r[:40]) + "..."
	}
	return fmt.Sprintf("%s (%d lines)", first, len(lines))
}

// wrap breaks text into lines of about 60 characters, indenting every
// line after the first by indent spaces.
func wrap(text string, indent int) string {
	var b strings.Builder
	n := 0
	for i, word := range strings.Fields(text) {
		if i > 0 && n+len(word) > 60 {
			b.WriteString("\n" + strings.Repeat(" ", indent))
			n = 0
		} else if i > 0 {
			b.WriteByte(' ')
			n++
		}
		b.WriteString(word)
		n += len(word)
	}
	return b.String()
}
