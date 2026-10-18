// Package issuehttp is a keyed tool written from scratch, the way you
// would write one for a service of your own: it files issues through
// the tracker's HTTP API, sending each call's idempotency key in the
// Idempotency-Key header, and can look a filing up after a restart
// (chapter 15).
package issuehttp

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// Tracker is the tracker's HTTP API at URL, such as
// "http://localhost:8080".
type Tracker struct {
	HTTP *http.Client
	URL  string
}

type fileArgs struct {
	Title       string `json:"title" desc:"one line naming the crash"`
	Body        string `json:"body" desc:"the cause, the fix, and the reproducer"`
	Fingerprint string `json:"fingerprint" desc:"the crash group's fingerprint"`
}

type issue struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// tag::keyed-tool[]

// File returns an issue_file tool that files issues on the tracker. It
// is meant to run under chapter 12's RetryTools, which sends a keyed
// filing whose outcome is unknown again, with the same key.
func (tr Tracker) File() agent.Tool {
	t := agent.NewTool("issue_file", "File a new issue in the tracker.",
		func(ctx context.Context, a fileArgs) (string, error) {
			body, err := json.Marshal(a)
			if err != nil {
				return "", fmt.Errorf("issue_file: %w", err)
			}
			req, err := tr.request(ctx, http.MethodPost, "/issues",
				body)
			if err != nil {
				return "", fmt.Errorf("issue_file: %w", err)
			}
			k, keyed := agent.KeyFrom(ctx)
			if keyed { // <1>
				req.Header.Set("Idempotency-Key", strconv.Quote(k))
			}
			var is issue
			if err := tr.send(req, &is); err != nil {
				return "", filingError(err, keyed) // <2>
			}
			return fmt.Sprintf("filed #%d", is.ID), nil
		})
	t.Keyed = true            // <3>
	t.KeyTTL = 24 * time.Hour // <4>
	t.Lookup = tr.lookup      // <5>
	return t
}

// end::keyed-tool[]

// tag::keyed-unknown[]

// filingError is what a failed filing returns. An answer that refuses
// the issue, such as 400 or 422, is a tool error the model can act on.
// Anything else may have come after the tracker filed it: a 5xx, a 408
// or 429, a reset, a timeout, or a 201 whose body never arrived. The
// model, told the call failed, would file again with a new key, so the
// outcome is unknown instead.
func filingError(err error, keyed bool) error {
	if s, ok := errors.AsType[*statusError](err); ok && !s.unsure() {
		return fmt.Errorf("issue_file: %w", err)
	}
	return &unknownError{err: err, keyed: keyed}
}

// unknownError is ErrOutcomeUnknown, so the run stops with the call's
// marker in place and the ladder settles it. With a key it is also
// retryable: RetryTools sends the same request again, which the
// tracker recognizes by its key, and when the retries run out it
// reports the outcome unknown itself.
type unknownError struct {
	err   error // what happened, kept out of the chain
	keyed bool
}

// end::keyed-unknown[]

func (e *unknownError) Error() string {
	return "issue_file: outcome unknown: " + e.err.Error()
}

// Is makes the error ErrOutcomeUnknown.
func (e *unknownError) Is(target error) bool {
	return target == agent.ErrOutcomeUnknown
}

// Retryable reports whether sending the same request again is safe.
func (e *unknownError) Retryable() bool { return e.keyed }

// RetryDelay is the tracker's Retry-After, if it sent one.
func (e *unknownError) RetryDelay() time.Duration {
	if s, ok := errors.AsType[*statusError](e.err); ok {
		return s.after
	}
	return 0
}

// A statusError is an answer other than 200 or 201.
type statusError struct {
	status string
	code   int
	text   string
	after  time.Duration // from Retry-After, in seconds
}

func (e *statusError) Error() string {
	return e.status + ": " + e.text
}

// unsure reports whether the status leaves open whether the tracker
// acted: a timeout, too many requests, or a server or gateway failure.
func (e *statusError) unsure() bool {
	return e.code == http.StatusRequestTimeout ||
		e.code == http.StatusTooManyRequests || e.code >= 500
}

// errNotSure is the lookup's answer when it can't tell: over a network,
// a miss isn't proof, so the ladder asks a person.
var errNotSure = errors.New("no issue is certainly this call's, and " +
	"the stopped run's request may still arrive")

// tag::keyed-lookup[]

// lookup finds the issue a call filed before its run stopped. The
// ladder asks it only when running the call again isn't safe: no key
// was sent, or the key may have expired.
func (tr Tracker) lookup(ctx context.Context, call agent.ToolCall,
	key string) (string, bool, error) {
	var a fileArgs
	if err := json.Unmarshal(call.Arguments, &a); err != nil {
		return "", false, fmt.Errorf("issue_file lookup: %w", err)
	}
	if key != "" { // <1>
		found, err := tr.search(ctx, url.Values{"key": {key}})
		if err != nil || len(found) > 0 {
			return filed(found), err == nil, err
		}
	}
	found, err := tr.search(ctx, url.Values{ // <2>
		"fingerprint": {a.Fingerprint}, "status": {"open"}})
	if err != nil {
		return "", false, err
	}
	found = slices.DeleteFunc(found, func(is issue) bool {
		return is.Title != a.Title
	})
	if len(found) != 1 {
		return "", false, errNotSure // <3>
	}
	return filed(found), true, nil
}

// end::keyed-lookup[]

// filed is the call's answer for the first of issues, if there is one.
func filed(issues []issue) string {
	if len(issues) == 0 {
		return ""
	}
	return fmt.Sprintf("filed #%d (found after a restart)", issues[0].ID)
}

// search returns the issues that match q.
func (tr Tracker) search(ctx context.Context, q url.Values) ([]issue,
	error) {
	req, err := tr.request(ctx, http.MethodGet, "/issues?"+q.Encode(),
		nil)
	if err != nil {
		return nil, fmt.Errorf("issue_file lookup: %w", err)
	}
	var found struct {
		Issues []issue `json:"issues"`
	}
	if err := tr.send(req, &found); err != nil {
		return nil, fmt.Errorf("issue_file lookup: %w", err)
	}
	return found.Issues, nil
}

// request makes a request to the tracker; body may be nil.
func (tr Tracker) request(ctx context.Context, method, path string,
	body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, tr.URL+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// send makes the request and decodes a 200 or 201 answer into v. The
// tracker answers a repeated key with 200 and the first request's
// issue, and a key reused for a different request with 422; any other
// status is a *statusError.
func (tr Tracker) send(req *http.Request, v any) error {
	resp, err := tr.client().Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusCreated {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return fmt.Errorf("%s: read body: %w", resp.Status, err)
		}
		secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &statusError{status: resp.Status, code: resp.StatusCode,
			text:  string(bytes.TrimSpace(b)),
			after: time.Duration(max(secs, 0)) * time.Second}
	}
	if err := json.UnmarshalRead(resp.Body, v); err != nil {
		return fmt.Errorf("decode answer: %w", err)
	}
	return nil
}

// client is the HTTP client to use: HTTP, or the default if it is nil.
func (tr Tracker) client() *http.Client {
	if tr.HTTP == nil {
		return http.DefaultClient
	}
	return tr.HTTP
}
