package triage

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// gateRuns is how many times the gate runs a reproducer on the
// original code: a test that fails once by chance shouldn't get an
// issue filed.
const gateRuns = 2

// gate decides whether an issue for a fingerprint group may be filed with
// test as its reproducer (chapter 13). The test must fail on the
// original code every time it runs there, either on its own check or
// with the group's own panic, and pass on the agent's fix. gate returns
// the refusal, the gate's verdict, or an error when it couldn't check.
// tag::gate[]
func (e *Env) gate(ctx context.Context, fingerprint string,
	test Location) (refusal string, err error) {
	group, err := e.groupOf(fingerprint)
	if err != nil {
		return "", err
	}
	args, why := reproducerArgs(test, isRace(group))
	if why != "" {
		return why, nil
	}
	for range gateRuns { // <1>
		out, err := e.onOriginal(ctx, args) // <2>
		if err != nil {
			return "", err
		}
		if r := failsRightly(test.Function, out, group); r != "" {
			return r, nil
		}
	}
	after, err := e.goTest(ctx, e.Dir, args)
	if err != nil {
		return "", err
	}
	if outcome(after) != "pass" {
		return fmt.Sprintf("%s doesn't pass on your fixed code:\n%s",
			test.Function, after), nil
	}
	return "", nil
}

// end::gate[]

// tag::fails-rightly[]

// failsRightly returns why a reproducer's run on the original code
// doesn't count, or "" when it failed as a reproducer should: on its
// own check, or with one of the group's panics.
func failsRightly(name, out string, group []Report) string {
	switch outcome(out) {
	case "pass":
		return name + " passes on the original code, so it doesn't " +
			"reproduce the crash:\n" + out
	case "no test":
		return "no test named " + name + " ran"
	case "no build":
		return "the tests don't compile on the original code:\n" + out
	}
	if panicLine.MatchString(out) && !ownPanic(out, group) { // <1>
		return name + " panics on the original code, but not with " +
			"the group's panic in the function that crashed:\n" + out
	}
	return ""
}

// end::fails-rightly[]

var (
	panicLine = regexp.MustCompile(`(?m)^\s*panic: `)
	digits    = regexp.MustCompile(`[0-9]+`)
)

// ownPanic reports whether out shows one of the group's panics. The
// numbers in a panic are data, so they are left out of the comparison:
// "index out of range [2] with length 2" matches "[1] with length 1".
// A panic with no data, such as a nil pointer dereference, could come
// from anywhere, so it matches only if the function that crashed in
// the report is on the test's stack too.
func ownPanic(out string, group []Report) bool {
	plain := digits.ReplaceAllString(out, "N")
	for _, r := range group {
		if r.Kind != "panic" || r.Panic == "" {
			continue
		}
		msg := digits.ReplaceAllString(firstLine(r.Panic), "N")
		if !strings.Contains(plain, msg) {
			continue
		}
		if strings.Contains(msg, "N") {
			return true // the message itself is specific
		}
		if fn := frames(r, 1); len(fn) == 1 && strings.Contains(out, fn[0]) {
			return true
		}
	}
	return false
}

// groupOf returns the reports with a fingerprint.
func (e *Env) groupOf(fingerprint string) ([]Report, error) {
	var out []Report
	for _, r := range e.Reports {
		if Fingerprint(r) == fingerprint {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no report has fingerprint %q; use one "+
			"from crash_list", fingerprint)
	}
	return out, nil
}

func isRace(group []Report) bool {
	for _, r := range group {
		if r.Kind == "race" {
			return true
		}
	}
	return false
}

// reproducerArgs returns go test's arguments for one reproducer, or
// why the test can't be one.
func reproducerArgs(test Location, race bool) ([]string, string) {
	file, err := clean(test.File)
	if err != nil || !strings.HasSuffix(file, "_test.go") ||
		test.Function == "" {
		return nil, "the reproducer must be a test in a _test.go " +
			"file, given as file and function"
	}
	args := []string{"-run", "^" + regexp.QuoteMeta(test.Function) + "$",
		"./" + path.Dir(file)}
	if race {
		args = append([]string{"-race"}, args...)
	}
	return args, ""
}

// onOriginal runs go test on the original code with the agent's test
// files added, and returns its report.
func (e *Env) onOriginal(ctx context.Context, args []string) (string, error) {
	orig, err := e.originalWithTests()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(orig)
	return e.goTest(ctx, orig, args)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// A GateEvent is something the gate did in a run, for an eval's record:
// a refusal, or an issue filed without a reproducer for a group whose
// reproducer the gate had refused.
type GateEvent struct {
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"` // "refused" or "refiled"
	Reason      string `json:"reason,omitzero"`
}

// GateEvents returns what the gate did in this run.
func (e *Env) GateEvents() []GateEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]GateEvent(nil), e.gateEvents...)
}

// gated records the gate's verdict on a group.
func (e *Env) gated(fingerprint, refusal string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refusedGroups == nil {
		e.refusedGroups = map[string]bool{}
	}
	if refusal == "" {
		e.refusedGroups[fingerprint] = false // a later pass clears it
		return
	}
	e.refusedGroups[fingerprint] = true
	e.gateEvents = append(e.gateEvents, GateEvent{fingerprint, "refused",
		firstLine(refusal)})
}

// refusedGroup reports whether the gate's last word on a group was a
// refusal.
func (e *Env) refusedGroup(fingerprint string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refusedGroups[fingerprint]
}

func (e *Env) refiled(fingerprint string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gateEvents = append(e.gateEvents, GateEvent{fingerprint, "refiled", ""})
}
