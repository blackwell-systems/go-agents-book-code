package triage

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// tag::fingerprint[]

// Fingerprint names the likely cause of a crash, so reports with the
// same cause can be grouped before anyone reads them: the kind of
// panic, and the function inside remind where it happened. It never
// uses the panic message, which can carry text from the request, and
// so an attacker's words. It is a first filter: two causes in one
// function share a fingerprint, and one cause that crashes in two
// functions gets two.
func Fingerprint(r Report) string { return Remind.Fingerprint(r) }

// Fingerprint is the fingerprint of a crash in the target's code
// (chapter 17).
func (t Target) Fingerprint(r Report) string {
	return t.fingerprintAt(r, depth)
}

// fingerprintAt is a fingerprint over n of the target's frames.
func (t Target) fingerprintAt(r Report, n int) string {
	key := panicKind(r) + "\n" + strings.Join(t.frames(r, n), "\n")
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("fp_%x", sum[:3])
}

// depth is how many of the target's frames a fingerprint uses. More frames
// split one cause reached from two callers into two fingerprints; on
// the crash catalog, one frame grouped best (see the tests).
const depth = 1

// end::fingerprint[]

// tag::panic-kind[]

// panicKind reduces a panic to its kind, dropping the values in it:
// "runtime error: index out of range [3] with length 2" becomes "index
// out of range". Only the runtime's own messages are read, by how they
// begin; a panic remind raised itself, with a message of its own, is
// just "panic", since its text is the part an attacker can shape.
func panicKind(r Report) string {
	if r.Kind != "panic" {
		return r.Kind // "stuck", or "race" from the catalog's race builds
	}
	msg := strings.TrimPrefix(r.Panic, "runtime error: ")
	if strings.HasPrefix(msg, "invalid memory address") {
		return "nil pointer dereference"
	}
	for _, k := range runtimeKinds {
		if strings.HasPrefix(msg, k) {
			return k
		}
	}
	return "panic"
}

// runtimeKinds are how the Go runtime's panic messages begin.
var runtimeKinds = []string{
	"index out of range",
	"slice bounds out of range",
	"assignment to entry in nil map",
	"close of closed channel",
	"close of nil channel",
	"send on closed channel",
	"integer divide by zero",
	"interface conversion",
	"concurrent map",
	"all goroutines are asleep",
}

// end::panic-kind[]

// tag::frames[]

// frames returns up to n functions from the stack that belong to
// the target, outermost last, skipping the crash reporter that recovered
// the panic, and normalized so the same code always gives the same
// name: closures lose their numbers (func1, func2), generic functions
// lose their type arguments, and a closure counts as the function it
// is in, so a closure and its caller in a row count once.
func (t Target) frames(r Report, n int) []string {
	var out []string
	for line := range strings.Lines(t.stackOf(r)) {
		if !strings.HasPrefix(line, t.prefix()) {
			continue // runtime, standard library, and file:line lines
		}
		fn := t.normalize(line)
		if strings.HasPrefix(fn, "crash.") || fn == "" {
			continue // the reporter, not the crash
		}
		if len(out) > 0 && out[len(out)-1] == fn {
			continue // a closure called by the function it is in
		}
		out = append(out, fn)
		if len(out) == n {
			break
		}
	}
	return out
}

// end::frames[]

var (
	closureRE = regexp.MustCompile(`(\.func\d+)(\.\d+)*`) // .func1.2
	genericRE = regexp.MustCompile(`\[[^\]]*\]`)          // [...] type args
)

// normalize turns a stack line such as
// "example.com/remind/api.(*API).owned.func1({0x1, 0x2})" into
// "api.(*API).owned".
func (t Target) normalize(line string) string {
	fn := strings.TrimPrefix(strings.TrimSpace(line), t.prefix())
	fn = stripArgs(fn)
	fn = genericRE.ReplaceAllString(fn, "")
	return closureRE.ReplaceAllString(fn, "")
}

// stripArgs removes the argument list from the end of a stack line,
// the last parenthesized part, leaving receivers such as (*API) alone.
func stripArgs(fn string) string {
	if !strings.HasSuffix(fn, ")") {
		return fn
	}
	open := 0
	for i := len(fn) - 1; i >= 0; i-- {
		switch fn[i] {
		case ')':
			open++
		case '(':
			open--
			if open == 0 {
				return fn[:i]
			}
		}
	}
	return fn
}

// stackOf returns the part of a report's stack that locates the crash.
// For a goroutine dump, that is the first goroutine stuck inside the
// target's own code, other than the main goroutine.
func (t Target) stackOf(r Report) string {
	if r.Kind != "stuck" {
		return r.Stack
	}
	for g := range strings.SplitSeq(r.Stack, "\n\n") {
		if strings.Contains(g, "[running]") || strings.Contains(g, "main.main") {
			continue
		}
		for line := range strings.Lines(g) {
			if strings.HasPrefix(line, t.prefix()) &&
				!strings.HasPrefix(line, t.prefix()+"crash.") {
				return g
			}
		}
	}
	return r.Stack
}
