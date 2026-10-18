package agent

import (
	"errors"
	"fmt"
)

// tag::tree[]

// The errors Run returns are grouped into families, so a caller can ask
// "was the budget the problem?" with errors.Is(err, ErrBudget) and not
// care which limit it was.
var (
	// ErrBudget: the run used up a limit the caller set.
	ErrBudget         = errors.New("agent: budget exhausted")
	ErrMaxTurns       = fmt.Errorf("%w: max turns reached", ErrBudget)
	ErrBudgetExceeded = fmt.Errorf("%w: cost budget reached", ErrBudget)

	// ErrBadReply: the model's reply can't be used as it is.
	ErrBadReply        = errors.New("agent: bad reply")
	ErrBadToolCall     = fmt.Errorf("%w: bad tool call", ErrBadReply)
	ErrOutputTruncated = fmt.Errorf("%w: cut off at the length limit",
		ErrBadReply)
	ErrOutputFiltered = fmt.Errorf("%w: blocked by a content filter",
		ErrBadReply)
	ErrProviderFailed = fmt.Errorf("%w: provider failed partway",
		ErrBadReply)
)

// end::tree[]

// tag::tool-errors[]

// ErrTool and its members name problems with one tool call. Run never
// returns them: they become tool messages, and the model tries again.
var (
	ErrTool        = errors.New("agent: tool call failed")
	ErrUnknownTool = fmt.Errorf("%w: unknown tool", ErrTool)
	ErrToolArgs    = fmt.Errorf("%w: bad arguments", ErrTool)
)

// A toolError is a problem with one tool call. Its message is what the
// model reads; its kind is for errors.Is. A tool that wraps a kind with
// fmt.Errorf instead would send the model the kind's text as well.
type toolError struct {
	kind error
	msg  string
}

func (e *toolError) Error() string { return e.msg }
func (e *toolError) Unwrap() error { return e.kind }

// end::tool-errors[]
