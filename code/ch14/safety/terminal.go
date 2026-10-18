package safety

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
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
// for a line or for ctx to end.
func (a *LineApprover) Decide(ctx context.Context,
	r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
	a.once.Do(a.start)
	code := r.Hash[:6]
	args, h1 := Display(string(r.Call.Arguments))
	evidence, h2 := Display(r.Evidence)
	targets := DisplayPath(strings.Join(r.Targets, ", "))
	fmt.Fprintf(a.Out, "%s wants to run %s on %s\nwhy: %s\n%s\n%s\n",
		r.Run, DisplayPath(r.Call.Name), targets, r.Why, args, evidence)
	if h1 || h2 {
		fmt.Fprintln(a.Out, "(hidden characters shown as <U+...>)")
	}
	fmt.Fprintf(a.Out, "type \"yes %s\" to approve: ", code)
	d := agent.ApprovalDecision{Hash: r.Hash, By: "terminal"}
	select {
	case line, ok := <-a.lines:
		if !ok { // input closed: ask later
			return agent.ApprovalDecision{}, agent.ErrNoDecision
		}
		d.Approved = strings.TrimSpace(line) == "yes "+code
		if !d.Approved {
			d.Reason = "denied at the terminal"
		}
		return d, nil
	case <-ctx.Done():
		return d, fmt.Errorf("decide: %w", ctx.Err())
	}
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
