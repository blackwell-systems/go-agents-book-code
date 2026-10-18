package main

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch05/agent"
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
			if m.Content != "" {
				p.printf("  model says:  %s\n", wrap(m.Content, 15))
			}
			for _, c := range m.ToolCalls {
				printCall(p, c)
			}
		case m.IsError:
			p.printf("  tool error:  %s\n", wrap(m.Content, 15))
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
