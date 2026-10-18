package triage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
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
	Title       string   `json:"title" desc:"one line naming the problem, such as \"store.Get returns a deleted reminder\""`
	Body        string   `json:"body" desc:"the cause, the evidence, the fix, and the reports it covers"`
	Fingerprint string   `json:"fingerprint" desc:"the crash group's fingerprint, from crash_list"`
	Reproducer  Location `json:"reproducer,omitzero" desc:"the test that reproduces the crash, for a fixed group; leave out when you couldn't find the cause"`
}

func issueFile(e *Env) agent.Tool {
	t := agent.NewTool("issue_file",
		"File a new issue for a crash group, once you know its cause. "+
			"Search the open issues first; don't file a problem that is "+
			"already filed. File one issue per cause. For a group you "+
			"fixed, name its reproducer test; an issue without one is "+
			"filed as needing a person.",
		func(ctx context.Context, a issueFileArgs) (string, error) {
			fp, err := e.fingerprint(a.Fingerprint) // chapter 15
			if err != nil {
				return "", err
			}
			a.Fingerprint = fp
			labels := []string{"crash"}
			switch {
			case a.Reproducer.Function == "":
				labels = append(labels, "needs-human")
				if e.Gate && e.refusedGroup(a.Fingerprint) {
					// submit must say needs_human
					if err := e.refiled(a.Fingerprint); err != nil {
						return "", err
					}
				}
			case e.Gate: // chapter 13
				refusal, err := e.gate(ctx, a.Fingerprint, a.Reproducer)
				if err != nil {
					return "", fmt.Errorf("not filed: %w", err)
				}
				if err := e.gated(a.Fingerprint, refusal); err != nil {
					return "", err
				}
				if refusal != "" {
					return "", fmt.Errorf("not filed: %s", refusal)
				}
			}
			upgrade := a.Reproducer.Function != "" // needs-human to fixed
			if n, ok := e.filedBefore(ctx, a.Fingerprint, a.Title,
				upgrade); ok {
				return "", fmt.Errorf("not filed: this run already "+
					"filed #%d for this cause; comment on it with "+
					"issue_comment", n)
			}
			is, _, err := e.Issues.CreateOnce(e.key(ctx), a.Title,
				a.Body, labels, a.Fingerprint) // chapter 15
			if err != nil {
				return "", fmt.Errorf("issue_file: %w", err)
			}
			if err := e.recordFiled(is); err != nil {
				return "", err
			}
			return fmt.Sprintf("filed #%d", is.ID), nil
		})
	t.Keyed = e.Keys // without keys, filing twice files two
	return t
}

// end::issue-file[]

type issueCommentArgs struct {
	ID   int    `json:"id" desc:"the issue number, such as 101"`
	Body string `json:"body" desc:"the comment, such as new reports of the same crash"`
}

func issueComment(e *Env) agent.Tool {
	t := agent.NewTool("issue_comment",
		"Add a comment to an issue, such as new crash reports that match "+
			"one already filed.",
		func(ctx context.Context, a issueCommentArgs) (string, error) {
			_, _, err := e.Issues.CommentOnce(a.ID, e.key(ctx), a.Body)
			if errors.Is(err, tracker.ErrNotFound) {
				return "", fmt.Errorf("no issue #%d; issue_search lists "+
					"the open issues", a.ID)
			}
			if err != nil {
				return "", fmt.Errorf("issue_comment: %w", err)
			}
			return fmt.Sprintf("commented on #%d", a.ID), nil
		})
	t.Keyed = e.Keys
	return t
}

// A filing is an issue this run filed: which, and for what cause.
type filing struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// filedBefore reports an open issue this run filed earlier, by another
// call, for the same fingerprint and title: the same cause filed again,
// by a model that lost track or by a new call after a failure, is
// refused rather than made (chapter 15). Two causes can share a
// fingerprint, and their titles differ, so each gets its issue. The
// issue this call's own key filed is not counted: that is a retry, and
// CreateOnce returns it. With upgrade, an issue filed as needing a
// person doesn't count, so a group fixed after the gate refused its
// first reproducer can be filed with it, as chapter 13 allows.
func (e *Env) filedBefore(ctx context.Context, fp, title string,
	upgrade bool) (int, bool) {
	e.mu.Lock()
	filed := slices.Clone(e.filed[fp])
	e.mu.Unlock()
	mine := e.key(ctx)
	for _, f := range filed {
		is, err := e.Issues.Get(f.ID)
		switch {
		case err != nil, is.Status != "open",
			!sameTitle(f.Title, title),
			mine != "" && is.Key == mine,
			upgrade && slices.Contains(is.Labels, "needs-human"):
			continue
		}
		return f.ID, true
	}
	return 0, false
}

func sameTitle(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "),
		strings.Join(strings.Fields(b), " "))
}

// recordFiled remembers an issue this run filed, for filedBefore.
func (e *Env) recordFiled(is tracker.Issue) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.filed == nil {
		e.filed = map[string][]filing{}
	}
	for _, f := range e.filed[is.Fingerprint] {
		if f.ID == is.ID {
			return nil // a retry returned the same issue
		}
	}
	e.filed[is.Fingerprint] = append(e.filed[is.Fingerprint],
		filing{is.ID, is.Title})
	return e.persist()
}

// fingerprint checks and normalizes a filing's fingerprint: trimmed and
// in lower case, as crash_list prints it, never empty, and, when the
// Env has the reports, the fingerprint of one of their groups.
func (e *Env) fingerprint(fp string) (string, error) {
	fp = strings.ToLower(strings.TrimSpace(fp))
	if fp == "" {
		return "", errors.New("not filed: give the crash group's " +
			"fingerprint, from crash_list")
	}
	if len(e.Reports) == 0 {
		return fp, nil // a tracker server has no reports to check
	}
	known := map[string]bool{}
	for _, r := range e.Reports {
		known[Fingerprint(r)] = true
	}
	if !known[fp] {
		return "", fmt.Errorf("not filed: no crash group has "+
			"fingerprint %q; the groups are %s", fp,
			strings.Join(slices.Sorted(maps.Keys(known)), ", "))
	}
	return fp, nil
}

// key returns the idempotency key the run gave this call, if the
// tracker is to be sent keys (chapter 15).
func (e *Env) key(ctx context.Context) string {
	if !e.Keys {
		return ""
	}
	k, _ := agent.KeyFrom(ctx)
	return k
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
			out, err := e.test(ctx, e.Dir, 3*time.Minute, args...)
			if err != nil {
				return "", fmt.Errorf("run_tests: %w", err)
			}
			return out, nil
		})
}

// end::run-tests[]
