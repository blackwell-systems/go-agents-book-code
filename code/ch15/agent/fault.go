package agent

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
)

// tag::fault[]

// A Fault is what a Faulty transport does to one request.
type Fault struct {
	Status     int    // answer with this status instead of sending
	RetryAfter string // the Retry-After header to answer Status with

	Drop     bool // fail before the request leaves
	Lost     bool // send, let the server answer, then fail
	Reset    int  // send, then reset the body after this many bytes
	Truncate int  // send, then end the body early after this many bytes
}

// Faulty is an http.RoundTripper that injects one Fault per request,
// in order, and sends requests on to Next once its faults run out or
// when a fault is the zero Fault. It makes the failures chapters 8
// to 10 met by chance happen on purpose, in a test.
type Faulty struct {
	Next   http.RoundTripper // http.DefaultTransport if nil
	Faults []Fault

	mu sync.Mutex
	n  int // requests seen
}

// badRecordMAC is the error chapter 8's run stopped with: a TLS alert
// from the server, as crypto/tls reports it.
var badRecordMAC = &net.OpError{Op: "remote error",
	Err: tls.AlertError(20)} // <1>

// end::fault[]

// tag::fault-trip[]

// RoundTrip applies the next fault to req, or sends it on.
func (f *Faulty) RoundTrip(req *http.Request) (*http.Response, error) {
	fault := f.next()
	if fault.Drop || fault.Status != 0 {
		return fault.instead(req)
	}
	resp, err := f.transport().RoundTrip(req)
	switch {
	case err != nil:
		return nil, err //nolint:wrapcheck // passed on as is
	case fault.Lost:
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, badRecordMAC
	case fault.Reset > 0:
		resp.Body = &cutBody{resp.Body, fault.Reset, resetErr}
	case fault.Truncate > 0:
		resp.Body = &cutBody{resp.Body, fault.Truncate,
			io.ErrUnexpectedEOF}
	}
	return resp, nil
}

// end::fault-trip[]

// instead answers req without sending it: with fault's status, or as
// a dropped connection. A RoundTripper always closes the body it took.
func (fault Fault) instead(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if fault.Drop {
		return nil, badRecordMAC
	}
	h := http.Header{}
	if fault.RetryAfter != "" {
		h.Set("Retry-After", fault.RetryAfter)
	}
	body := io.NopCloser(strings.NewReader(""))
	return &http.Response{StatusCode: fault.Status, Header: h,
		Body: body, Request: req}, nil
}

// resetErr is how a connection reset reaches a reader.
var resetErr = &net.OpError{Op: "read", Net: "tcp",
	Err: syscall.ECONNRESET}

// next returns the fault for the next request.
func (f *Faulty) next() Fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.n <= len(f.Faults) {
		return f.Faults[f.n-1]
	}
	return Fault{}
}

func (f *Faulty) transport() http.RoundTripper {
	if f.Next == nil {
		return http.DefaultTransport
	}
	return f.Next
}

// Requests returns how many requests f has seen.
func (f *Faulty) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// cutBody passes through left bytes of a body, then fails with err.
type cutBody struct {
	io.ReadCloser
	left int
	err  error
}

func (c *cutBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, c.err
	}
	n, err := c.ReadCloser.Read(p[:min(len(p), c.left)])
	c.left -= n
	return n, err //nolint:wrapcheck // a Reader passes errors on
}
