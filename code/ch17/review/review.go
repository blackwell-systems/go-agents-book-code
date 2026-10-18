// Package review is the person's side of the review queue: a client
// for the service's review routes, and the command, triage review, that
// lists what waits and sends decisions (chapter 17).
package review

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
)

// A StatusError is an answer from the service other than success, with
// the message it sent.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("service: %d %s: %s", e.Status,
		http.StatusText(e.Status), e.Message)
}

// tag::client[]

// A Client talks to a triage service's review routes as one reviewer.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// NewClient returns a client for the service at base, sending token.
// It refuses plain HTTP to anything but this machine, since the token
// would cross the network readable.
func NewClient(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("service address: %w", err)
	}
	switch {
	case token == "":
		return nil, errors.New("no review token; " +
			"set TRIAGE_REVIEW_TOKEN")
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return nil, fmt.Errorf("%s: use https for a service on "+
			"another machine", base)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%s: not an http or https address", base)
	}
	return &Client{base: u, token: token,
		http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// end::client[]

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// List returns the items waiting for a decision, oldest first.
func (c *Client) List(ctx context.Context) ([]service.Item, error) {
	var items []service.Item
	err := c.do(ctx, http.MethodGet, "review", nil, &items)
	return items, err
}

// Decide answers an item with verb (approve, deny, rerun, or done),
// giving the reason the model or the run will read.
func (c *Client) Decide(ctx context.Context, id, verb,
	reason string) (service.Item, error) {
	var it service.Item
	path := "review/" + url.PathEscape(id) + "/" + verb
	err := c.do(ctx, http.MethodPost, path,
		map[string]string{"reason": reason}, &it)
	return it, err
}

func (c *Client) do(ctx context.Context, method, path string, body,
	out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method,
		c.base.JoinPath(path).String(), rd)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e) // a body that isn't ours: no message
		return &StatusError{resp.StatusCode, e.Error}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// tag::main[]

// Main runs "triage review" with its arguments: list; show ID; approve
// or deny for approvals; rerun or done for unknown outcomes. The
// service's address comes from TRIAGE_URL and the reviewer's token
// from TRIAGE_REVIEW_TOKEN.
func Main(ctx context.Context, args []string,
	getenv func(string) string, w io.Writer) error {
	c, err := NewClient(cmp.Or(getenv("TRIAGE_URL"),
		"http://localhost:8417"), getenv("TRIAGE_REVIEW_TOKEN"))
	if err != nil {
		return err
	}
	out, err := command(ctx, c, args)
	if _, werr := io.WriteString(w, out); werr != nil && err == nil {
		return fmt.Errorf("print: %w", werr)
	}
	return err
}

// command runs one review command and returns what to print.
func command(ctx context.Context, c *Client, args []string) (string,
	error) {
	switch {
	case len(args) == 1 && args[0] == "list":
		items, err := c.List(ctx)
		if err != nil {
			return "", err
		}
		return list(items, time.Now()), nil
	case len(args) == 2 && args[0] == "show":
		items, err := c.List(ctx)
		if err != nil {
			return "", err
		}
		return show(items, args[1])
	case len(args) >= 2 && verbs[args[0]] != "":
		return decide(ctx, c, args[0], args[1:])
	}
	return "", errors.New(usage)
}

const usage = "usage: triage review list, show ID, approve [-m reason] " +
	"ID, deny -m reason ID (for approvals), rerun ID, or done -m " +
	"\"what you found\" ID (for unknown outcomes); several IDs allowed"

// verbs are the answers, each to its own kind of item: an unknown
// outcome isn't approved, it is run again or recorded as done.
var verbs = map[string]string{"approve": "approved", "deny": "denied",
	"rerun": "will run again", "done": "recorded as done"}

// decide gives each item named the answer verb, with one reason for
// all, and reports each; the service refuses an answer for the other
// kind of item, and decide stops at the first refusal, so the person
// sees which were decided.
func decide(ctx context.Context, c *Client, verb string,
	args []string) (string, error) {
	reason := ""
	if len(args) >= 2 && args[0] == "-m" {
		reason, args = args[1], args[2:]
	}
	if len(args) == 0 {
		return "", errors.New(usage)
	}
	var b strings.Builder
	for _, id := range args {
		it, err := c.Decide(ctx, id, verb, reason)
		if err != nil {
			return b.String(), fmt.Errorf("%s %s: %w", verb, id, err)
		}
		by := ""
		if it.Decision != nil {
			by = it.Decision.By
		}
		fmt.Fprintf(&b, "%s: %s by %s\n", it.ID, verbs[verb],
			printable(by))
	}
	return b.String(), nil
}

// end::main[]

// tag::list[]

// list returns two lines per item: its ID, kind, tool, and how long it
// has waited (marked stale for an unknown outcome nobody has checked),
// then, indented, why it waits.
func list(items []service.Item, now time.Time) string {
	if len(items) == 0 {
		return "nothing waiting\n"
	}
	var b strings.Builder
	for _, it := range items {
		age := now.Sub(it.Added).Round(time.Minute).String()
		if it.Stale {
			age += " STALE"
		}
		fmt.Fprintf(&b, "%s  %-15s  %-12s  %s\n  %s\n", it.ID, it.Kind,
			printable(it.Tool), age, printable(firstLine(it.Why)))
	}
	return b.String()
}

// end::list[]

// show returns one item in full, with the raw evidence a person judges
// it by.
func show(items []service.Item, id string) (string, error) {
	for _, it := range items {
		if it.ID != id {
			continue
		}
		answers := "approve or deny"
		if it.Kind == service.KindOutcome {
			answers = "rerun (run it again) or done (it took effect)"
		}
		return fmt.Sprintf("item:     %s (%s; answer with %s)\n"+
			"run:      %s\ncall:     %s %s\nargs:     %s\nwhy:      %s\n"+
			"targets:  %s\nwaiting:  since %s\nevidence:\n%s",
			it.ID, it.Kind, answers, oneLine(it.Run), oneLine(it.Tool),
			oneLine(it.CallID), oneLine(it.Args), oneLine(it.Why),
			oneLine(strings.Join(it.Targets, ", ")),
			it.Added.Format(time.RFC3339), block(it.Evidence)), nil
	}
	return "", fmt.Errorf("no item %s waiting", id)
}

// oneLine is printable with line breaks shown as \n, so text from the
// model or a report can't start a line that looks like one of show's
// own fields.
func oneLine(s string) string {
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(
		printable(s))
}

// block is printable with every line indented behind "  | ", so a
// multi-line field, such as a diff, stays visibly inside its field.
func block(s string) string {
	var b strings.Builder
	for line := range strings.Lines(printable(s)) {
		b.WriteString("  | " + strings.TrimRight(line, "\r\n") + "\n")
	}
	return b.String()
}

// tag::printable[]

// printable shows text from a crash report or the model safely in a
// terminal: control characters, such as the escape that starts a
// terminal command, and invisible formatting characters, such as the
// ones that reorder text, are shown as <U+XXXX> instead of acting.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r),
			r == unicode.ReplacementChar:
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// end::printable[]

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
