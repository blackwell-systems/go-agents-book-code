package triage

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
)

// tag::redact[]

// A secret is a pattern for one kind of credential. Its one group, if
// it has one, is a label to keep, such as "Bearer ", so the model can
// still see that a credential was sent. A token-shaped secret is only
// redacted if its value has a digit, so ordinary words after "bearer"
// are left alone.
type secret struct {
	re     *regexp.Regexp
	tokens bool
}

var secrets = []secret{
	{regexp.MustCompile(`(?i)\b(bearer[ \t]+)[\w.~+/=-]{8,}`), true},
	{regexp.MustCompile(`(?i)(authorization"?:[ \t]*"?basic[ \t]+)[^\s"]+`), false},
	{regexp.MustCompile(`\brk_(?:live|admin|test)_[0-9A-Za-z]+`), false},
	{regexp.MustCompile(`\bsk-[\w-]{16,}`), true},
	{regexp.MustCompile(`(?i)("?(?:secret|password|passwd|api[_-]?key)` +
		`"?\s*[=:]\s*"?)[^\s"',&]+`), false},
	{regexp.MustCompile(`(?i)("?(?:set-)?cookie"?:[ \t]*"?)[^"\n]+`), false},
}

// Redact replaces each credential in s with [REDACTED].
func Redact(s string) string {
	for _, sec := range secrets {
		s = sec.re.ReplaceAllStringFunc(s, func(m string) string {
			label := ""
			if sec.re.NumSubexp() > 0 {
				label = sec.re.FindStringSubmatch(m)[1]
			}
			if sec.tokens && !strings.ContainsAny(m[len(label):], "0123456789") {
				return m // a word, not a token
			}
			return label + "[REDACTED]"
		})
	}
	return s
}

// end::redact[]

// tag::redacted[]

// redacted wraps a tool so that its output, its errors, and the value
// of any panic pass through Redact before the model sees them. An error
// keeps its kind, so errors.Is still works on it.
func redacted(t agent.Tool) agent.Tool {
	run := t.Run
	t.Run = func(ctx context.Context, args jsontext.Value) (string, error) {
		defer func() {
			if p := recover(); p != nil {
				panic(Redact(fmt.Sprint(p))) // the loop recovers it
			}
		}()
		out, err := run(ctx, args)
		if err != nil {
			return "", &redactedError{err}
		}
		return Redact(out), nil
	}
	return t
}

type redactedError struct{ err error }

func (e *redactedError) Error() string { return Redact(e.err.Error()) }
func (e *redactedError) Unwrap() error { return e.err }

// end::redacted[]
