package triage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

type issueSearchArgs struct {
	Query       string `json:"query,omitzero" desc:"words to look for in titles and bodies, such as parseBearer index out of range"`
	Fingerprint string `json:"fingerprint,omitzero" desc:"a fingerprint from crash_list"`
}

// tag::issue-search[]

func issueSearch(e *Env) agent.Tool {
	return agent.NewTool("issue_search",
		"Search the open issues in the issue tracker, by words or by "+
			"fingerprint; with neither, list them all. Issues that contain "+
			"more of the words come first. Check here before filing, since "+
			"a crash may already be filed, perhaps by a person, with no "+
			"fingerprint and in other words.",
		func(_ context.Context, a issueSearchArgs) (string, error) {
			open := e.Issues.Search(tracker.Query{Status: "open",
				Fingerprint: a.Fingerprint})
			words := strings.Fields(strings.ToLower(a.Query))
			type hit struct {
				is tracker.Issue
				n  int // how many of the words it contains
			}
			var hits []hit
			for _, is := range open {
				text := strings.ToLower(is.Title + " " + is.Body)
				n := 0
				for _, w := range words {
					if strings.Contains(text, w) {
						n++
					}
				}
				if n > 0 || len(words) == 0 { // <1>
					hits = append(hits, hit{is, n})
				}
			}
			if len(hits) == 0 {
				return "no open issues match", nil
			}
			slices.SortStableFunc(hits, func(x, y hit) int { return y.n - x.n })
			var b strings.Builder
			for _, h := range hits[:min(len(hits), 10)] {
				fmt.Fprintf(&b, "#%d %s\n", h.is.ID, h.is.Title)
				if len(words) > 0 {
					fmt.Fprintf(&b, "  matches %d of %d words\n", h.n, len(words))
				}
				if h.is.Fingerprint != "" {
					fmt.Fprintf(&b, "  fingerprint: %s\n", h.is.Fingerprint)
				}
				fmt.Fprintf(&b, "  %s\n\n", short(h.is.Body, 400))
			}
			return b.String(), nil
		})
}

// end::issue-search[]

// tag::issue-file[]

type issueFileArgs struct {
	Title       string `json:"title" desc:"one line naming the problem, such as \"store.Get returns a deleted reminder\""`
	Body        string `json:"body" desc:"the cause, the evidence, the fix, and the reports it covers"`
	Fingerprint string `json:"fingerprint" desc:"the crash group's fingerprint, from crash_list"`
}

func issueFile(e *Env) agent.Tool {
	t := agent.NewTool("issue_file",
		"File a new issue for a crash group, once you know its cause. "+
			"Search the open issues first; don't file a problem that is "+
			"already filed. File one issue per cause.",
		func(_ context.Context, a issueFileArgs) (string, error) {
			is, err := e.Issues.Create(a.Title, a.Body, []string{"crash"},
				a.Fingerprint)
			if err != nil {
				return "", fmt.Errorf("issue_file: %w", err)
			}
			return fmt.Sprintf("filed #%d", is.ID), nil
		})
	return t // neither read-only nor idempotent: filing twice files two
}

// end::issue-file[]

type issueCommentArgs struct {
	ID   int    `json:"id" desc:"the issue number, such as 101"`
	Body string `json:"body" desc:"the comment, such as new reports of the same crash"`
}

func issueComment(e *Env) agent.Tool {
	return agent.NewTool("issue_comment",
		"Add a comment to an issue, such as new crash "+
			"reports that match one already filed.",
		func(_ context.Context, a issueCommentArgs) (string, error) {
			if _, err := e.Issues.Comment(a.ID, a.Body); err != nil {
				return "", fmt.Errorf("no issue #%d; issue_search lists "+
					"the open issues", a.ID)
			}
			return fmt.Sprintf("commented on #%d", a.ID), nil
		})
}

// tag::run-tests[]

type testArgs struct {
	Package string `json:"package" desc:"package patterns, such as ./store or ./store ./api or ./..."`
	Run     string `json:"run,omitzero" desc:"run only tests matching this regular expression"`
	Race    bool   `json:"race,omitzero" desc:"run with the race detector"`
}

func runTests(e *Env) agent.Tool {
	return agent.NewTool("run_tests",
		"Run go test on remind's code, as it is now, and report failed "+
			"tests, build errors, and each package's result. Run one "+
			"package while you work; run ./... before you finish.",
		func(ctx context.Context, a testArgs) (string, error) {
			var args []string
			if a.Race {
				args = append(args, "-race")
			}
			if a.Run != "" {
				args = append(args, "-run", a.Run) // <1>
			}
			for pat := range strings.FieldsSeq(a.Package) {
				p, err := clean(pat)
				if err != nil {
					return "", err
				}
				args = append(args, "./"+p)
			}
			out, err := agent.GoTest(ctx, e.Dir, 3*time.Minute, args...)
			if err != nil {
				return "", fmt.Errorf("run_tests: %w", err)
			}
			return out, nil
		})
}

// end::run-tests[]
