package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// tag::denied[]

// ErrDenied means a person, or the lack of an answer in time, refused a
// call. Like the rest of the ErrTool family it never leaves the loop:
// the model reads the reason and tries something else (chapter 14).
var ErrDenied = fmt.Errorf("%w: denied", ErrTool)

// Denial returns an error wrapping ErrDenied whose message, the text
// the model reads, gives the reason.
func Denial(why string) error {
	return &toolError{ErrDenied, "denied: " + why}
}

// end::denied[]

// ArgsError returns an error wrapping ErrToolArgs whose message, the
// text the model reads, says what is wrong with the arguments, for
// middleware that reads them before the tool does (chapter 14).
func ArgsError(why string) error {
	return &toolError{ErrToolArgs, why}
}

// ErrBadResume means a caller resumed a paused conversation it had
// changed: a message follows calls that have no answer yet. It is a
// mistake in the caller, not a bad model reply; the answers to the
// waiting calls have to come first.
var ErrBadResume = errors.New("agent: resumed conversation has " +
	"a message after calls that have no answer")

// ErrNoDecision is what an Approver returns when it has no decision yet
// and the run should pause until one arrives.
var ErrNoDecision = errors.New("agent: no decision yet")

// tag::types[]

// An ApprovalRequest is what a person is asked to decide: the call, why
// it needs a decision, the evidence to judge it by, and the hash that a
// decision has to name to count. The hash covers everything the person
// is shown, so a decision applies to what they saw.
type ApprovalRequest struct {
	Run      string // the agent's name, from WithName
	Call     ToolCall
	Why      string
	Evidence string   // raw: the diff, the report excerpt
	Targets  []string // what the call would change
	Hash     string
}

// end::types[]

// tag::decision[]

// An ApprovalDecision answers one request. It applies only to the
// request whose hash it names.
type ApprovalDecision struct {
	Approved bool
	Hash     string
	By       string // who decided, for the audit log
	Reason   string
}

// An Approver asks a person, or a script in tests, for a decision. It
// returns ErrNoDecision when the decision will come later, which pauses
// the run, and it must return when ctx is done.
type Approver interface {
	Decide(ctx context.Context, r ApprovalRequest) (ApprovalDecision,
		error)
}

// end::decision[]

// ApproverFunc is a function that is an Approver, such as a scripted
// approver in a test or an eval.
type ApproverFunc func(ctx context.Context,
	r ApprovalRequest) (ApprovalDecision, error)

// Decide calls f.
func (f ApproverFunc) Decide(ctx context.Context,
	r ApprovalRequest) (ApprovalDecision, error) {
	return f(ctx, r)
}

// tag::pending[]

// ApprovalPending is the error a run stops with when a call is waiting
// for a person. Result.Pending holds that call and the ones after it;
// running the conversation again, with Limits.Approver set to where the
// decisions come from, resumes the run with those calls.
type ApprovalPending struct {
	Requests []ApprovalRequest
}

func (p *ApprovalPending) Error() string {
	names := make([]string, len(p.Requests))
	for i, r := range p.Requests {
		names[i] = r.Call.Name
	}
	return "approval pending for " + strings.Join(names, ", ")
}

// end::pending[]

func isPending(err error) bool {
	_, ok := errors.AsType[*ApprovalPending](err)
	return ok
}

type approverKey struct{}

// WithApprover returns a context carrying the run's approver, for the
// approval middleware.
func WithApprover(ctx context.Context, a Approver) context.Context {
	return context.WithValue(ctx, approverKey{}, a)
}

// ApproverFrom returns the approver of the run the context belongs to:
// Limits.Approver, or nil if the run has none, in which case every call
// that needs a decision waits for one.
func ApproverFrom(ctx context.Context) Approver {
	a, _ := ctx.Value(approverKey{}).(Approver)
	return a
}

// tag::unanswered[]

// unanswered returns the last reply's calls that have no answer yet,
// those a paused run left waiting, or ErrBadResume if another message
// follows them, since the API would refuse that request.
func unanswered(msgs []Message) ([]ToolCall, error) {
	answered := map[string]bool{}
	later := false // a message other than an answer follows
	for _, m := range slices.Backward(msgs) {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			later = true
			continue
		}
		var out []ToolCall
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				out = append(out, c)
			}
		}
		if len(out) > 0 && later {
			return nil, fmt.Errorf("%w: %d calls have no answer, and "+
				"the conversation goes on after them", ErrBadResume,
				len(out))
		}
		return out, nil
	}
	return nil, nil
}

// end::unanswered[]
