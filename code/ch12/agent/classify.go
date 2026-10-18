package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"syscall"
	"time"
)

// tag::decision[]

// A Decision says whether to send a failed call again, and how long
// the server asked us to wait first.
type Decision struct {
	Retry bool
	After time.Duration // the server's requested delay, or 0
	Why   string        // a word for logs and traces
}

// ErrAttemptTimeout is the cause set on one attempt's own deadline, so
// an attempt that ran out of time can be told apart from a caller who
// gave up: the first is worth another try, the second is not.
var ErrAttemptTimeout = errors.New("reliability: attempt timed out")

// end::decision[]

// tag::reports[]

// Errors that report on themselves: retryable, such as chapter 5's
// *APIError and this chapter's *RetryError; delayed, an error that
// carries the server's requested wait; and timeout, such as a call's
// own deadline or chapter 6's idle stream, and net.Error.
type (
	retryable interface {
		error
		Retryable() bool
	}
	delayed interface {
		error
		RetryDelay() time.Duration
	}
	timeout interface {
		error
		Timeout() bool
	}
)

// end::reports[]

// tag::classify[]

// Classify decides whether err, from a call made under ctx, the
// caller's context rather than the attempt's, is worth retrying.
func Classify(ctx context.Context, err error) Decision {
	switch {
	case err == nil:
		return Decision{}
	case ctx.Err() != nil: // <1>
		return Decision{Why: "cancelled"}
	case errors.Is(err, ErrToolFatal): // <2>
		return Decision{Why: "fatal"}
	}
	if r, ok := errors.AsType[retryable](err); ok { // <3>
		d := Decision{Retry: r.Retryable(), Why: "status"}
		if w, ok := errors.AsType[delayed](err); ok {
			d.After = w.RetryDelay()
		}
		return d
	}
	if timedOut(err) { // <4>
		return Decision{Retry: true, Why: "timed out"}
	}
	if transient(err) {
		return Decision{Retry: true, Why: "network"}
	}
	return Decision{Why: "permanent"}
}

// end::classify[]

// tag::timed-out[]

// timedOut reports whether any error in err's chain is a timeout. It
// looks at every one, not the first that has a Timeout method, because
// a *url.Error answers for the error directly inside it only, and the
// client's own timeout is usually wrapped deeper.
func timedOut(err error) bool {
	if errors.Is(err, ErrAttemptTimeout) {
		return true
	}
	for err != nil {
		//nolint:errorlint // this walks the chain one error at a time
		if t, ok := err.(timeout); ok && t.Timeout() {
			return true
		}
		switch u := err.(type) { //nolint:errorlint // the same walk
		case interface{ Unwrap() []error }:
			return slices.ContainsFunc(u.Unwrap(), timedOut)
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}

// end::timed-out[]

// tag::transient[]

// transient reports whether err is a connection that failed or broke,
// which a second attempt on a new connection may not hit: a reset, a
// body cut short, a kept-alive connection the server had closed (EOF,
// or net/http's "server closed idle connection"), an HTTP/2 stream
// reset, or a TLS alert such as "bad record MAC". A certificate the
// client rejects fails the same way every time.
func transient(err error) bool {
	_, cert := errors.AsType[*tls.CertificateVerificationError](err)
	if cert {
		return false
	}
	if d, ok := errors.AsType[*net.DNSError](err); ok {
		return d.IsTemporary || d.IsTimeout
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := err.Error() // <1>
	if strings.Contains(msg, "stream error: stream ID") ||
		strings.Contains(msg, "server closed idle connection") {
		return true
	}
	_, ok := errors.AsType[*net.OpError](err)
	return ok
}

// end::transient[]
