package safety

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// tag::terminal[]

// A LineApprover asks a person at a terminal. One goroutine reads lines
// for as long as the approver lives, so a Decide that gives up, when
// its context ends, leaves no read behind it; the next Decide gets the
// next line instead. Because a line typed late would then answer the
// next request, approving takes "yes" and a code from the request's
// hash: a stale yes names the wrong code and denies, which is the safe
// way to be wrong. Close closes the input, the one way to end the
// reading goroutine's blocked read, as chapter 2 described.
type LineApprover struct {
	In  io.ReadCloser
	Out io.Writer

	once  sync.Once
	lines chan string
	done  chan struct{}
	shut  sync.Once
}

// end::terminal[]

// tag::terminal-decide[]

// Decide shows the request, its arguments and evidence raw, and waits
// for a line or for ctx to end. A question about an unknown outcome
// asks whether the call took effect, which needs its own answers.
func (a *LineApprover) Decide(ctx context.Context,
	r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	a.once.Do(a.start)
	code := r.Hash[:6]
	args, h1 := Display(string(r.Call.Arguments))
	evidence, h2 := Display(r.Evidence)
	targets := DisplayPath(strings.Join(r.Targets, ", "))
	if r.Kind == agent.KindUnknownOutcome { // chapter 15
		fmt.Fprintf(a.Out, "%s: did %s take effect?\n%s\n%s\n%s\n",
			r.Run, DisplayPath(r.Call.Name), r.Why, args, evidence)
	} else {
		fmt.Fprintf(a.Out, "%s wants to run %s on %s\nwhy: %s\n%s\n%s\n",
			r.Run, DisplayPath(r.Call.Name), targets, r.Why, args, evidence)
	}
	if h1 || h2 {
		fmt.Fprintln(a.Out, "(hidden characters shown as <U+...>)")
	}
	if r.Kind == agent.KindUnknownOutcome {
		fmt.Fprintf(a.Out, "type \"rerun %s\" if it didn't, or \"done "+
			"%s what you found\" if it did: ", code, code)
	} else {
		fmt.Fprintf(a.Out, "type \"yes %s\" to approve: ", code)
	}
	select {
	case line, ok := <-a.lines:
		if !ok { // input closed: ask later
			return agent.ApprovalDecision{}, agent.ErrNoDecision
		}
		if r.Kind == agent.KindUnknownOutcome {
			return outcomeAnswer(r, code, strings.TrimSpace(line))
		}
		d := agent.ApprovalDecision{Hash: r.Hash, By: "terminal"}
		d.Approved = strings.TrimSpace(line) == "yes "+code
		if !d.Approved {
			d.Reason = "denied at the terminal"
		}
		return d, nil
	case <-ctx.Done():
		return agent.ApprovalDecision{}, fmt.Errorf("decide: %w",
			ctx.Err())
	}
}

// outcomeAnswer reads the answer to an unknown-outcome question: "rerun CODE"
// runs the call again, "done CODE what you found" settles it as done.
// Anything else, a slip of the keyboard included, decides nothing, and
// the question is asked again later.
func outcomeAnswer(r agent.ApprovalRequest, code, line string) (
	agent.ApprovalDecision, error) {
	d := agent.ApprovalDecision{Hash: r.Hash, By: "terminal"}
	switch {
	case line == "rerun "+code:
		d.Approved = true
		return d, nil
	case strings.HasPrefix(line, "done "+code+" "):
		d.Reason = strings.TrimSpace(strings.TrimPrefix(line,
			"done "+code))
		if d.Reason != "" {
			return d, nil
		}
	}
	return agent.ApprovalDecision{}, agent.ErrNoDecision
}

// end::terminal-decide[]

// tag::terminal-read[]

func (a *LineApprover) start() {
	a.lines, a.done = make(chan string), make(chan struct{})
	go func() {
		defer close(a.lines)
		sc := bufio.NewScanner(a.In)
		for sc.Scan() {
			select {
			case a.lines <- sc.Text():
			case <-a.done:
				return
			}
		}
	}()
}

// Close ends the approver's reading goroutine: it closes the input,
// which ends a blocked read, and tells a goroutine waiting to hand over
// a line to stop.
func (a *LineApprover) Close() error {
	a.once.Do(a.start)
	a.shut.Do(func() { close(a.done) })
	if err := a.In.Close(); err != nil {
		return fmt.Errorf("close approver input: %w", err)
	}
	return nil
}

// end::terminal-read[]
