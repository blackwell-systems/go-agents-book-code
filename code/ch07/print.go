package main

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
)

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
	s = strings.ReplaceAll(strings.TrimSpace(s), "\t", " ")
	lines := strings.Split(s, "\n")
	if len(lines) <= 3 && len(s) < 50 {
		return strings.Join(lines, ", ")
	}
	first := lines[0]
	if r := []rune(first); len(r) > 40 {
		first = string(r[:40]) + "..."
	}
	if len(lines) == 1 {
		return first
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

// printResult prints a tool message.
func printResult(p *printer, m agent.Message) {
	if m.IsError {
		p.printf("  tool error:  %s\n", wrap(m.Content, 15))
		return
	}
	p.printf("  tool result: %s\n", summary(m.Content))
}

// tag::text-writer[]

// A textWriter prints streamed text as it arrives, breaking lines at
// spaces. A word can arrive in pieces, so it holds the last partial
// word until the next space shows where it ends.
type textWriter struct {
	p       *printer
	started bool   // printed anything since the last end
	col     int    // the column the next word starts at
	partial string // the start of a word still arriving
}

func (t *textWriter) write(s string) {
	t.partial += s
	for {
		i := strings.IndexAny(t.partial, " \n")
		if i < 0 {
			return
		}
		t.word(t.partial[:i])
		if t.partial[i] == '\n' && t.started {
			t.p.printf("\n")
			t.col = 0 // the indent waits for the next word
		}
		t.partial = t.partial[i+1:]
	}
}

// word prints one complete word, starting a new line if it won't fit.
func (t *textWriter) word(w string) {
	if w == "" {
		return
	}
	if !t.started {
		t.p.printf("  model says:  ")
		t.started, t.col = true, 15
	}
	if t.col == 0 {
		t.p.printf("%15s", "")
		t.col = 15
	}
	switch {
	case t.col > 15 && t.col+1+len(w) > 75:
		t.p.printf("\n%15s", "")
		t.col = 15
	case t.col > 15:
		t.p.printf(" ")
		t.col++
	}
	t.p.printf("%s", w)
	t.col += len(w)
}

// end ends the current text, printing any partial word, and reports
// whether there was any text.
func (t *textWriter) end() bool {
	t.word(t.partial)
	was := t.started
	if was {
		t.p.printf("\n")
	}
	*t = textWriter{p: t.p}
	return was
}

// end::text-writer[]
