package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// A row is one line of the comparison: a measure and its value for
// each version.
type row struct {
	Measure string
	Values  []string
}

// table runs every version on every script and writes the comparison
// as an AsciiDoc table. root is the module's directory.
func table(ctx context.Context, out io.Writer, root string) error {
	rows := []row{
		{Measure: "Lines of code for the task"},
		{Measure: "Lines of code under it, outside the standard library " +
			"and the shared task code"},
		{Measure: "Largest modules under it"},
		{Measure: "Modules under it, besides the book's"},
		{Measure: "Model calls to finish"},
		{Measure: "First request (bytes)"},
		{Measure: "Tool schemas marked strict"},
		{Measure: "A tool's text result is sent as"},
		{Measure: "A tool's error"},
		{Measure: "A 503 on the first call, as ported"},
		{Measure: "Waiting for the person"},
	}
	for _, v := range versions {
		values, err := measure(ctx, root, v)
		if err != nil {
			return fmt.Errorf("measure %s: %w", v.Name, err)
		}
		for i := range rows {
			rows[i].Values = append(rows[i].Values, values[i])
		}
	}
	return write(out, rows[:4], rows[4:])
}

// measure runs v on every script and returns its column of the table.
func measure(ctx context.Context, root string, v version) ([]string,
	error) {
	runs := map[string]run{}
	for _, s := range scripts {
		r := replayed(ctx, v, s)
		if errors.Is(r.Err, replay.ErrNoRecording) {
			return nil, fmt.Errorf("script %s: %w", s, r.Err)
		}
		runs[s] = r
	}
	ok := runs["diagnose"]
	if ok.Err != nil {
		return nil, fmt.Errorf("diagnose: %w", ok.Err)
	}
	own, err := lines(filepath.Join(root, v.Dir))
	if err != nil {
		return nil, err
	}
	fp, err := compiled(ctx, root, "./"+v.Dir)
	if err != nil {
		return nil, err
	}
	first, err := decode(ok.Bodies[0])
	if err != nil {
		return nil, err
	}
	second, err := decode(ok.Bodies[1])
	if err != nil {
		return nil, err
	}
	form, err := resultForm(second)
	if err != nil {
		return nil, err
	}
	return []string{
		comma(own),
		under(fp),
		largest(fp),
		strconv.Itoa(len(fp.Modules)),
		strconv.Itoa(len(ok.Bodies)),
		comma(len(ok.Bodies[0])),
		strictness(first),
		form,
		toolError(runs["tool-error"]),
		retried(runs["retry"]),
		waiting(ok.Outcome),
	}, nil
}

// under says how much code a footprint holds, and how much of it was
// generated.
func under(fp footprint) string {
	if fp.Generated == 0 {
		return comma(fp.Lines)
	}
	return fmt.Sprintf("%s (%s generated)", comma(fp.Lines),
		comma(fp.Generated))
}

// largest names the two largest modules and their share of the code.
func largest(fp footprint) string {
	if len(fp.Modules) == 0 {
		return "none"
	}
	var names []string
	for _, m := range fp.Modules[:min(2, len(fp.Modules))] {
		names = append(names, fmt.Sprintf("`%s` %d%%",
			strings.TrimPrefix(m.Path, "github.com/"),
			100*m.Lines/fp.Lines))
	}
	return strings.Join(names, ", ")
}

// write prints the rows as an AsciiDoc table body, for the chapter to
// include inside its own table block: the header and the code rows in
// tag code, the behavior rows in tag behavior, so the recorded binary
// sizes (sizes.go) can go between them.
func write(out io.Writer, code, behavior []row) error {
	var b bytes.Buffer
	b.WriteString("// tag::code[]\n| Measure")
	for _, v := range versions {
		b.WriteString(" | " + v.Name)
	}
	b.WriteString("\n")
	rows(&b, code)
	b.WriteString("// end::code[]\n\n// tag::behavior[]\n")
	rows(&b, behavior)
	b.WriteString("// end::behavior[]\n")
	if _, err := out.Write(b.Bytes()); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}

// rows writes rows as AsciiDoc table cells, one per line.
func rows(b *bytes.Buffer, rs []row) {
	for _, r := range rs {
		b.WriteString("\n| " + r.Measure)
		for _, v := range r.Values {
			b.WriteString("\n| " + v)
		}
		b.WriteString("\n")
	}
}

// A request is the part of a chat completions request body the table
// looks at.
type request struct {
	Messages []struct {
		Role    string         `json:"role"`
		Content jsontext.Value `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Strict *bool `json:"strict"`
		} `json:"function"`
	} `json:"tools"`
}

// decode reads the parts of a request body the table looks at.
func decode(body []byte) (request, error) {
	var r request
	if err := json.Unmarshal(body, &r); err != nil {
		return r, fmt.Errorf("decode request: %w", err)
	}
	return r, nil
}

// strictness says whether the first request marks its tools strict.
func strictness(r request) string {
	if len(r.Tools) == 0 || r.Tools[0].Function.Strict == nil {
		return "not sent"
	}
	if *r.Tools[0].Function.Strict {
		return "yes"
	}
	return "no (`strict: false` sent)"
}

// resultForm looks at the first tool result sent back, the output of
// run_tests, and says whether it went as the text or as a JSON string
// holding the text.
func resultForm(r request) (string, error) {
	for _, m := range r.Messages {
		if m.Role != "tool" {
			continue
		}
		var text string
		if err := json.Unmarshal(m.Content, &text); err != nil {
			return "", fmt.Errorf("decode tool result: %w", err)
		}
		if strings.HasPrefix(text, `"`) {
			return "a JSON string, quotes and escapes included", nil
		}
		return "the text", nil
	}
	return "", errors.New("no tool result in the second request")
}

// toolError says what a run did with run_tests' error.
func toolError(r run) string {
	if r.Err == nil {
		return fmt.Sprintf("sent to the model, which corrected the "+
			"call; %d calls", len(r.Bodies))
	}
	is := "no"
	if errors.Is(r.Err, task.ErrNoPackage) {
		is = "yes"
	}
	return "ends the run; `errors.Is` finds the tool's error: " + is
}

// retried says what a run did when the first model call got a 503.
func retried(r run) string {
	if r.Err != nil {
		return "ends the run"
	}
	return fmt.Sprintf("retried; %d calls in all", len(r.Bodies))
}

// waiting says how the run waited for the person's decision.
func waiting(o task.Outcome) string {
	if o.Pauses == 0 {
		return "the tool asks and waits; the run doesn't stop"
	}
	return fmt.Sprintf("the run stops; resuming needs %s bytes kept",
		comma(o.Saved))
}

// comma formats n with thousands separators.
func comma(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
