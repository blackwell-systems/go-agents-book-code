package safety

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// tag::egress-types[]

// A Finding is one thing in outgoing text that shouldn't leave. Text
// names it without repeating it: a link's host, or the first few
// characters of a secret.
type Finding struct {
	Kind string // "link", "secret", or "word"
	Text string
}

// Egress is the policy for text the agent sends out of the run, such as
// an issue's body: the hosts a link may point at, the harness's own
// secrets, which must not leave in any encoding, and other words that
// must not leave, such as the names of those secrets.
type Egress struct {
	Hosts   []string
	Secrets []string
	Words   []string
}

// end::egress-types[]

// tag::egress[]

// Check returns everything in text that shouldn't leave. It reads the
// text as a person's screen would: invisible characters removed, and
// letters written in a wider form read as the plain ones. Every link
// must point at an allowed host, since a URL's path and query string
// can carry anything out; anything shaped like a credential, and the
// harness's own secrets in plain, base64, hex, or URL-escaped form,
// are reported wherever they are, with spaces between their characters
// or not.
func (e Egress) Check(text string) []Finding {
	text = normalize(text) // <1>
	out := e.links(text)
	found := e.secrets(text)
	for _, f := range e.secrets(squeeze(text)) { // <2>
		if !slices.Contains(found, f) {
			found = append(found, f)
		}
	}
	return append(out, found...)
}

// end::egress[]

// links returns every link in text to a host the policy doesn't allow.
func (e Egress) links(text string) []Finding {
	var out []Finding
	for _, c := range linkLike.FindAllString(text, -1) {
		host := hostOf(c)
		if host == "" || !slices.Contains(e.Hosts, host) {
			to := "to " + cmp(host, "an unknown host")
			out = append(out, Finding{"link", to})
		}
	}
	return out
}

// secrets returns the credential shapes and the harness's own secrets
// and words in text.
func (e Egress) secrets(text string) []Finding {
	var out []Finding
	for _, re := range credentials {
		for _, m := range re.FindAllString(text, -1) {
			out = append(out, Finding{"secret", prefix(m)})
		}
	}
	return append(out, e.own(text)...)
}

// own returns the harness's own secrets and words found in text.
func (e Egress) own(text string) []Finding {
	var out []Finding
	for _, s := range e.Secrets {
		if containsEncoded(text, s) {
			own := "one of the harness's own"
			out = append(out, Finding{"secret", own})
		}
	}
	for _, w := range e.Words {
		if strings.Contains(strings.ToLower(text), strings.ToLower(w)) {
			out = append(out, Finding{"word", w})
		}
	}
	return out
}

// tag::patterns[]

// linkLike matches anything a renderer or a person might follow: a
// scheme with or without slashes (https:x, https:/\x, mailto:, data:),
// a bare www. name, and a protocol-relative //host.
var linkLike = regexp.MustCompile(`(?i)` +
	`(?:\b|_)(?:https?|ftp|wss?|file|mailto|data|javascript):` +
	`[^\s<>"'` + "`" + `]+|` +
	`(?:^|[^\w.-])www\.[^\s<>"']+|` +
	`(?:^|[\s"'(=])//[a-z0-9.-]+\.[a-z]{2,}[^\s<>"']*`)

// credentials are shapes no issue body needs.
var credentials = []*regexp.Regexp{
	regexp.MustCompile(`\brk_(?:live|admin|test)_[0-9A-Za-z]{6,}`),
	regexp.MustCompile(`\bsk-[\w-]{16,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\beyJ[\w-]{8,}\.[\w-]{8,}\.[\w-]{8,}`), // a JWT
	regexp.MustCompile(`(?i)\bbearer\s+[\w.~+/=-]*\d[\w.~+/=-]{7,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
}

// end::patterns[]

// squeeze removes every space and line break, so a key written with
// spaces between its characters reads as the key.
func squeeze(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// normalize removes invisible characters, which can split a key so no
// pattern matches it, and folds fullwidth letters and digits, which
// render like ASCII, onto ASCII.
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case invisible(r):
			return -1
		case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
			return r - 0xFF01 + '!'
		}
		return r
	}, s)
}

// invisible reports whether a person can't see r: control and format
// characters, variation selectors, tag characters, and the blank
// fillers some fonts draw as nothing.
func invisible(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) ||
		r >= 0xE0000 && r <= 0xE007F || r == 0x2028 || r == 0x2029 ||
		r == 0x2800 || r == 0x115F || r == 0x1160 || r == 0x3164 || r == 0xFFA0
}

// hostOf returns the host a link-like candidate points at, lower-cased,
// or "" for schemes that have none worth allowing (mailto, data,
// javascript, file).
func hostOf(c string) string {
	c = strings.TrimLeft(c, "_ \t\"'(=")
	c = strings.TrimRight(c, ".,;:!?)]'\"")
	lower := strings.ToLower(c)
	for _, s := range []string{"mailto:", "data:", "javascript:", "file:"} {
		if strings.HasPrefix(lower, s) {
			return ""
		}
	}
	if i := strings.Index(c, ":"); i >= 0 && !strings.HasPrefix(lower, "www.") {
		c = c[i+1:]
	}
	c = strings.TrimLeft(strings.ReplaceAll(c, `\`, "/"), "/")
	u, err := url.Parse("//" + c)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// containsEncoded reports whether text holds secret as is, URL-escaped,
// in hex, or in base64 at any of the three alignments a prefix can give
// it.
func containsEncoded(text, secret string) bool {
	if secret == "" {
		return false
	}
	forms := []string{secret, url.QueryEscape(secret),
		hex.EncodeToString([]byte(secret))}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.RawURLEncoding} {
		for pad := range 3 {
			b := enc.EncodeToString([]byte(strings.Repeat("\x00", pad) + secret))
			if len(b) > 8 {
				forms = append(forms, b[4:len(b)-4]) // the part a prefix can't change
			}
		}
	}
	lower := strings.ToLower(text)
	for _, f := range forms {
		if strings.Contains(text, f) || strings.Contains(lower, strings.ToLower(f)) &&
			isHex(f) {
			return true
		}
	}
	return false
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func prefix(s string) string {
	if len(s) > 6 {
		return s[:4] + "..."
	}
	return "..."
}

func cmp(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
