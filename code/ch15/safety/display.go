package safety

import (
	"fmt"
	"strings"
)

// tag::display[]

// Display makes untrusted text safe to show a person in a terminal. An
// escape sequence can move the cursor and rewrite lines above it, and
// invisible or direction-changing characters can hide or reorder text,
// so each such character is shown as a visible <U+XXXX> instead, and
// hidden reports whether there were any, which a request should say.
func Display(s string) (shown string, hidden bool) {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || !invisible(r) {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, "<U+%04X>", r)
		hidden = true
	}
	return b.String(), hidden
}

// end::display[]

// DisplayPath shows a path with every character outside printable ASCII
// escaped, so a look-alike letter, such as a Cyrillic a in "auth",
// can't pass for the real one.
func DisplayPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 0x20 && r < 0x7F {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, "<U+%04X>", r)
	}
	return b.String()
}
