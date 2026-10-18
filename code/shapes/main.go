// Command shapes runs the four workflow shapes from chapter 5's table
// that the book describes but doesn't build: a fixed chain, routing,
// parallel calls with a vote, and check and revise. Each runs offline
// on scripted models, on crash reports from the remind service.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"unicode/utf8"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt)
	defer stop()
	if err := run(ctx, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// run runs each shape on its scripted models and prints what each one
// returned and how many calls it made.
func run(ctx context.Context, w io.Writer) error {
	p := &printer{w: w}
	for i, demo := range []func(context.Context, *printer) error{
		chainDemo, routeDemo, voteDemo, reviseDemo,
	} {
		if i > 0 {
			p.printf("\n")
		}
		if err := demo(ctx, p); err != nil {
			return err
		}
	}
	if p.err != nil {
		return fmt.Errorf("print: %w", p.err)
	}
	return nil
}

// crash is the report the demos work on: the empty snooze duration
// from the crash catalog's task d1.
const crash = `panic: runtime error: index out of range [-1]

goroutine 31 [running]:
example.com/remind/duration.Parse({0x0, 0x0})
	/app/remind/duration/duration.go:24 +0x1f4
example.com/remind/api.(*API).snooze(...)
	/app/remind/api/reminders.go:131 +0x2b8`

// tag::chain-demo[]

// triageSteps turns a crash report into an issue title in two steps,
// each with a check written in code.
var triageSteps = []Step{{
	Name: "summarize",
	Prompt: func(report string) string {
		return "Say in one sentence what this crash is:\n" + report
	},
	Check: oneLine(200),
}, {
	Name: "title",
	Prompt: func(summary string) string {
		return "Write an issue title for this crash:\n" + summary
	},
	Check: oneLine(72),
}}

// oneLine returns a check that a reply is one line of at most n
// characters.
func oneLine(n int) func(string) error {
	return func(s string) error {
		if strings.Contains(s, "\n") || utf8.RuneCountInString(s) > n {
			return fmt.Errorf("want one line of at most %d characters,"+
				" got %q", n, s)
		}
		return nil
	}
}

// end::chain-demo[]

// chainScript is the large model's replies to triageSteps.
var chainScript = []string{
	"Snoozing a reminder with an empty duration panics, because " +
		"duration.Parse reads the last byte of an empty string.",
	"duration.Parse panics on an empty string",
}

func chainDemo(ctx context.Context, p *printer) error {
	p.printf("A fixed chain: crash report, summary, issue title\n")
	m := script(chainScript...)
	title, err := Chain(ctx, m, crash, triageSteps)
	if err != nil {
		return fmt.Errorf("chain: %w", err)
	}
	p.printf("  title: %s\n  calls: %d\n", title, m.calls())
	return nil
}

func routeDemo(ctx context.Context, p *printer) error {
	p.printf("Routing: a small model picks the handler\n")
	small := script("crash", "question", // the classifier
		"Send \"for\": \"1w\" to snooze for a week.", // the answer
		"other") // the classifier again
	large := script(chainScript...)
	r := Router{
		Classifier: small,
		Handlers: map[string]Handler{
			"crash": func(ctx context.Context, in string) (
				string, error) {
				return Chain(ctx, large, in, triageSteps)
			},
			"question": func(ctx context.Context, in string) (
				string, error) {
				return ask(ctx, small, "Answer briefly:\n"+in)
			},
		},
		Fallback: func(context.Context, string) (string, error) {
			return "dropped without a model call", nil
		},
	}
	for _, in := range []string{crash,
		"How do I snooze a reminder for a week?",
		"WIN A FREE CRUISE, click here"} {
		kind, out, err := r.Route(ctx, in)
		if err != nil {
			return fmt.Errorf("route: %w", err)
		}
		p.printf("  %-9s %s\n", cmp.Or(kind, "(other)"), out)
	}
	p.printf("  calls: %d to the small model, %d to the large one\n",
		small.calls(), large.calls())
	return nil
}

func voteDemo(ctx context.Context, p *printer) error {
	p.printf("Parallel calls with a vote: 3 of 5 must agree\n")
	const q = "Is this crash the same bug as issue #12, \"snooze " +
		"with no duration panics\"? Answer yes or no.\n" + crash
	for _, answers := range [][]string{
		{"yes", "no", "yes", "Yes", " yes\n"},
		{"yes", "no", "unsure", "no", "yes"},
	} {
		voters := make([]agent.Model, len(answers))
		for i, a := range answers {
			voters[i] = script(a)
		}
		answer, err := Vote(ctx, voters, q, 3)
		p.printf("  votes: %q\n", answers)
		switch {
		case errors.Is(err, ErrNoAgreement):
			p.printf("  error: %v\n", err)
		case err != nil:
			return fmt.Errorf("vote: %w", err)
		default:
			p.printf("  answer: %s\n", answer)
		}
	}
	return nil
}

// tag::revise-demo[]

// issueCritic checks a drafted issue body: a length limit in code
// first, then a model against a rubric.
func issueCritic(m agent.Model) Critic {
	return AllOf(maxLength(600), ModelCritic(m, rubric))
}

const rubric = "- names the input that triggers the crash\n" +
	"- names the function to fix\n" +
	"- says nothing the crash report doesn't show"

// maxLength returns a Critic that fails drafts over n characters.
func maxLength(n int) Critic {
	return func(_ context.Context, draft string) (Verdict, error) {
		if c := utf8.RuneCountInString(draft); c > n {
			return Verdict{Notes: fmt.Sprintf(
				"Cut it to %d characters; it has %d.", n, c)}, nil
		}
		return Verdict{Pass: true}, nil
	}
}

// end::revise-demo[]

func reviseDemo(ctx context.Context, p *printer) error {
	p.printf("Check and revise: draft an issue body\n")
	writer := script(
		"The snooze endpoint crashes. Something in the duration "+
			"code is wrong.",
		"POST /v1/reminders/{id}/snooze with an empty \"for\" "+
			"panics in duration.Parse, which reads s[len(s)-1] "+
			"before checking for an empty string.")
	critic := script(
		`{"pass": false, "notes": "Name the input and the function."}`,
		"```json\n{\"pass\": true}\n```")
	body, err := Revise(ctx, writer, issueCritic(critic),
		"Write an issue body for this crash:\n"+crash, 2)
	if err != nil {
		return fmt.Errorf("revise: %w", err)
	}
	p.printf("  body:  %s\n", body)
	p.printf("  calls: %d to the writer, %d to the critic\n",
		writer.calls(), critic.calls())
	return nil
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
