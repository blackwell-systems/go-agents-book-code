// Package evals holds chapter 13's eval pieces that don't depend on the
// agent package: verdicts, the statistics for reading a small eval set,
// and the tiers of the crash catalog. The rest of the chapter's code is
// built on chapter 12's agent and folds these in.
package evals

import "fmt"

// tag::verdict[]

// A Verdict is what one run counts as in an eval's results.
type Verdict int

const (
	// Fail means the agent stopped without a good answer, or its
	// answer failed a check. Running out of turns or money, a bad
	// reply, and a refusal are the agent's failures too.
	Fail Verdict = iota
	// Pass means the run ended with an answer that passed every check.
	Pass
	// Unscored means the run says nothing about the agent, because the
	// harness or the provider failed (a provider error that outlasted
	// the retries, a tool that couldn't run, an outcome nobody knows).
	// It is counted, reported, and run again; it is never a pass.
	Unscored
)

// end::verdict[]

// tag::judge[]

// An Outcome is how one run ended: the error it stopped with, if any;
// whether its answer was graded and passed; and whether a check failed
// for the grader's own reason, such as a build cache losing files under
// the test run, rather than the agent's.
type Outcome struct {
	Err         error
	Graded      bool
	Passed      bool
	GraderFault bool
}

// Judge returns a run's verdict. unscored reports whether a stop error
// is the harness's or the provider's fault rather than the agent's; the
// harness supplies it, since only the harness knows its error types.
// A run passes only when it stopped without an error and its answer
// passed every check.
func Judge(o Outcome, unscored func(error) bool) Verdict {
	switch {
	case o.GraderFault, o.Err != nil && unscored(o.Err): // <1>
		return Unscored
	case o.Err == nil && o.Graded && o.Passed:
		return Pass
	}
	return Fail
}

// end::judge[]

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Unscored:
		return "unscored"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}
