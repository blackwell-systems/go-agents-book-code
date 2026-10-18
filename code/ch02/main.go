// Command ch02 is a chat program for a real model through OpenRouter.
// With OPENROUTER_API_KEY set, it talks to the live API. Without it, it
// replays a saved conversation, so it runs offline and costs nothing.
package main

import (
	"bufio"
	"bytes"
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
	"unicode/utf8"

	"github.com/blackwell-systems/go-agents-book/code/ch02/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch02/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch02/replay"
)

//go:embed testdata/replies
var saved embed.FS

const (
	defaultModel = "anthropic/claude-haiku-5.5"
	systemPrompt = "You are a helpful assistant for Go programmers. " +
		"Answer in a few sentences."
	replayNotice = "No OPENROUTER_API_KEY: replaying a saved conversation."
)

func main() {
	record := flag.String("record", "",
		"save each call to this directory, to replay later")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	err := run(ctx, os.Getenv, os.Stdin, os.Stdout, *record)
	if err != nil {
		log.Fatal(err)
	}
}

// tag::setup[]

// run sets up the client: live with a key, replaying without one.
func run(ctx context.Context, getenv func(string) string,
	in io.Reader, out io.Writer, record string) error {
	client := &openrouter.Client{
		APIKey:  getenv("OPENROUTER_API_KEY"),
		Model:   cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
		BaseURL: getenv("OPENROUTER_BASE_URL"),
	}
	ctx = replay.WithScript(ctx, "chat") // <1>
	echo := false
	switch {
	case client.APIKey == "":
		tr, inputs, err := savedChat()
		if err != nil {
			return err
		}
		client.HTTP = &http.Client{Transport: tr} // <2>
		in, echo = inputs, true
		if _, err := fmt.Fprintln(out, replayNotice); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	case record != "":
		rec := &replay.Recorder{Dir: record} // <3>
		client.HTTP = &http.Client{Transport: rec}
	}
	return chat(ctx, client, in, out, echo)
}

// end::setup[]

// savedChat returns a transport that replays the saved conversation,
// and the lines that were typed in it.
func savedChat() (http.RoundTripper, io.Reader, error) {
	files, err := fs.Sub(saved, "testdata/replies")
	if err != nil {
		return nil, nil, fmt.Errorf("saved replies: %w", err)
	}
	inputs, err := fs.ReadFile(files, "chat-inputs.txt")
	if err != nil {
		return nil, nil, fmt.Errorf("saved replies: %w", err)
	}
	return &replay.Transport{Files: files}, bytes.NewReader(inputs), nil
}

// tag::chat[]

// chat reads lines from in, sends the whole conversation to the model
// each time, and prints each reply with what it cost.
func chat(ctx context.Context, m agent.Model, in io.Reader,
	out io.Writer, echo bool) error {
	p := &printer{w: out}
	msgs := []agent.Message{{Role: "system", Content: systemPrompt}}
	var total float64
	lines := readLines(ctx, in)
	for {
		p.printf("> ")
		text, err := next(ctx, lines) // <1>
		if errors.Is(err, io.EOF) {
			p.printf("\n")
			return p.err
		}
		if err != nil {
			return err
		}
		if echo {
			p.printf("%s\n", text)
		}
		// end::chat[]
		// tag::chat-send[]
		msgs = append(msgs, agent.Message{Role: "user", Content: text})
		req := agent.Request{Messages: msgs}
		reply, err := m.Generate(ctx, req) // <1>
		if err != nil {
			return fmt.Errorf("chat: %w", err)
		}
		msgs = append(msgs, reply.Message) // <2>
		total += reply.Usage.Cost
		printReply(p, reply, total)
	}
}

// end::chat-send[]

// tag::print-reply[]

// printReply prints the model's answer and what the call cost.
func printReply(p *printer, r agent.Reply, total float64) {
	p.printf("%s\n", wrap(r.Message.Content, 70))
	p.printf("[%d tokens in, %d out, $%.6f; total $%.6f]\n",
		r.Usage.PromptTokens, r.Usage.CompletionTokens,
		r.Usage.Cost, total)
	if r.FinishReason != "stop" && r.FinishReason != "" {
		p.printf("[finish reason: %s]\n", r.FinishReason)
	}
}

// end::print-reply[]

// tag::read-lines[]

// A line is one line of input, or the error that ended the input.
type line struct {
	text string
	err  error
}

// readLines reads in on its own goroutine and sends each non-blank
// line on the returned channel, closing it at the end of input. The
// goroutine stops at the end of input or once ctx is done; a read in
// progress can't be interrupted, so it stops at the next line.
func readLines(ctx context.Context, in io.Reader) <-chan line {
	lines := make(chan line)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			text := strings.TrimSpace(sc.Text())
			if text == "" {
				continue
			}
			// end::read-lines[]
			// tag::read-lines-send[]
			select {
			case lines <- line{text: text}:
			case <-ctx.Done(): // <1>
				return
			}
		}
		if err := sc.Err(); err != nil {
			select {
			case lines <- line{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return lines
}

// end::read-lines-send[]

// tag::next[]

// next waits for the next line of input or for ctx to be done,
// whichever comes first. At the end of input it returns io.EOF.
func next(ctx context.Context, lines <-chan line) (string, error) {
	select {
	case <-ctx.Done():
		err := fmt.Errorf("wait for input: %w", ctx.Err())
		if cause := context.Cause(ctx); !errors.Is(err, cause) {
			err = fmt.Errorf("%w: %w", err, cause) // <1>
		}
		return "", err
	case l, ok := <-lines:
		if !ok {
			return "", io.EOF
		}
		if l.err != nil {
			return "", fmt.Errorf("read input: %w", l.err)
		}
		return l.text, nil
	}
}

// end::next[]

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

// wrap breaks lines longer than width at spaces, keeping the line breaks
// the text already has.
func wrap(text string, width int) string {
	var b strings.Builder
	for i, ln := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := 0
		for j, word := range strings.Split(ln, " ") {
			w := utf8.RuneCountInString(word)
			if j > 0 && n+1+w > width {
				b.WriteByte('\n')
				n = 0
			} else if j > 0 {
				b.WriteByte(' ')
				n++
			}
			b.WriteString(word)
			n += w
		}
	}
	return b.String()
}
