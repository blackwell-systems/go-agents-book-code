package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// tag::key[]

// Key returns the idempotency key for one tool call of one run. The
// call ID was saved with the reply before the call ran, so every retry
// of the call, in this process or after a restart, gets the same key,
// and no other call gets it.
func Key(run, call string) string {
	h := sha256.New()
	for _, s := range []string{run, call} { // <1>
		h.Write(strconv.AppendInt(nil, int64(len(s)), 10))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// end::key[]

// tag::key-context[]

type keyKey struct{}

// WithKey returns a context carrying the idempotency key of the call a
// tool runs for. The loop sets it for a run with an ID; a tool that
// calls a service sends it along, such as in an Idempotency-Key header.
func WithKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyKey{}, key)
}

// KeyFrom returns the idempotency key in ctx, if there is one.
func KeyFrom(ctx context.Context) (string, bool) {
	k, ok := ctx.Value(keyKey{}).(string)
	return k, ok && k != ""
}

// end::key-context[]

type runKey struct{}

// withRun returns a context carrying the run's ID, from which runTool
// derives each call's key.
func withRun(ctx context.Context, run string) context.Context {
	return context.WithValue(ctx, runKey{}, run)
}

func runFrom(ctx context.Context) string {
	r, _ := ctx.Value(runKey{}).(string)
	return r
}

// tag::rung[]

// A Rung is a step of the resolution ladder, tried in order.
type Rung int

// The rungs.
const (
	Retry   Rung = iota + 1 // the call is safe to run again
	Checked                 // the other side's record answers
	Asked                   // a person said what happened
)

// A Resolution is what the ladder decided for a call whose outcome is
// unknown: run it again, or answer it with what the record or the
// person said.
type Resolution struct {
	Rung   Rung
	Run    bool
	Answer Message // set when Run is false
}

// end::rung[]

// tag::resolve[]

// Resolve settles a call that has an attempt marker and no answer. It
// retries a tool that is safe to run again, asks the other side by key
// if the tool can, and otherwise asks a person through ap. With no
// decision available, it returns *ApprovalPending, and the run waits.
func Resolve(ctx context.Context, t Tool, run string, call ToolCall,
	a Attempt, ap Approver) (Resolution, error) {
	if t.ReadOnly || t.Idempotent || keyHeld(t, a) { // <1>
		return Resolution{Rung: Retry, Run: true}, nil
	}
	var why string
	if t.Lookup != nil { // <2>
		out, found, err := t.Lookup(ctx, call, a.Key)
		switch {
		case err == nil && found:
			return Resolution{Rung: Checked, Answer: toolAnswer(call,
				out)}, nil
		case err == nil:
			return Resolution{Rung: Checked, Run: true}, nil
		}
		why = "; the lookup failed: " + err.Error()
	}
	return ask(ctx, run, call, a, why, ap) // <3>
}

// end::resolve[]

// keyHeld reports whether the other side still holds the key a keyed
// call was sent with, so a repeat with it is recognized.
func keyHeld(t Tool, a Attempt) bool {
	return t.Keyed && a.Key != "" &&
		(t.KeyTTL == 0 || time.Since(a.Started) < t.KeyTTL)
}

// tag::ask[]

// ask puts the call to a person: approving runs it again; denying
// keeps it as done, and the reason, such as "filed as #12", becomes
// the call's answer.
func ask(ctx context.Context, run string, call ToolCall, a Attempt,
	why string, ap Approver) (Resolution, error) {
	r := ApprovalRequest{Kind: KindUnknownOutcome, Run: run, Call: call,
		Why: unknownWhy(a), Evidence: evidence(a) + why}
	r.Hash = outcomeHash(r, a)
	pending := &ApprovalPending{Requests: []ApprovalRequest{r}}
	if ap == nil {
		return Resolution{}, pending // <1>
	}
	d, err := ap.Decide(ctx, r)
	switch {
	case errors.Is(err, ErrNoDecision),
		err == nil && d.Hash != r.Hash,
		err == nil && !d.Approved && strings.TrimSpace(d.Reason) == "":
		return Resolution{}, pending // <2>
	case err != nil:
		return Resolution{}, fmt.Errorf("ask about %s: %w", call.Name,
			err)
	case d.Approved:
		return Resolution{Rung: Asked, Run: true}, nil
	}
	return Resolution{Rung: Asked, Answer: toolAnswer(call,
		"not run again: "+d.By+" checked and said: "+d.Reason)}, nil
}

// end::ask[]

// unknownWhy is what a person reads about a call whose outcome is
// unknown.
func unknownWhy(a Attempt) string {
	return "outcome unknown: " + a.Tool + " started at " +
		a.Started.Format(time.RFC3339) + " and the run stopped before " +
		"its result was saved. Approve to run it again; deny if it " +
		"took effect, saying what you found"
}

func toolAnswer(call ToolCall, out string) Message {
	return Message{Role: "tool", ToolCallID: call.ID, Content: out}
}

func evidence(a Attempt) string {
	if a.Key == "" {
		return "no idempotency key was sent"
	}
	return "idempotency key " + a.Key
}

// outcomeHash names what a person is asked about: the run, the call and
// its arguments, and the attempt. A lookup's error, shown to the person,
// is left out, so a decision still matches when the error's text
// changes between restarts.
func outcomeHash(r ApprovalRequest, a Attempt) string {
	h := sha256.New()
	for _, s := range []string{r.Run, r.Call.ID, r.Call.Name,
		string(r.Call.Arguments), a.Key,
		a.Started.Format(time.RFC3339Nano)} {
		fmt.Fprintf(h, "%d:%s", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}
