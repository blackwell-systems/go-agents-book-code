package openrouter

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// tag::api-error[]

// An APIError is a request the API refused, or a provider failure it
// reported. Callers inspect it with errors.AsType to decide what to do.
type APIError struct {
	Status     int           // HTTP status, or the code in the reply
	Message    string        // the API's own explanation
	RetryAfter time.Duration // the wait the API asked for, or 0

	// LimitSource is the error's metadata.limit_source, which says which
	// limit a 402 or 429 hit (chapter 12).
	LimitSource string
}

func (e *APIError) Error() string {
	status := strconv.Itoa(e.Status)
	if text := http.StatusText(e.Status); text != "" {
		status += " " + text
	}
	return fmt.Sprintf("openrouter: %s: %s", status, e.Message)
}

// Retryable reports whether the same request might succeed if sent
// again later: a timeout (408), too many requests (429), trouble on
// the provider's side (5xx), or a 402 from the limit on requests in
// flight, which clears as they finish. Chapter 12 retries these.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests || e.Status >= 500 ||
		e.inFlight()
}

// end::api-error[]

// tag::in-flight[]

// inFlight reports whether a 402 is OpenRouter's limit on paid
// requests running at once, which clears as they finish, rather than
// an account out of credit, which doesn't (chapter 12).
func (e *APIError) inFlight() bool {
	return e.Status == http.StatusPaymentRequired &&
		e.LimitSource == "openrouter_in_flight_budget"
}

// RetryDelay is the wait the API asked for, or 0 (chapter 12).
func (e *APIError) RetryDelay() time.Duration { return e.RetryAfter }

// StatusCode is the error's HTTP status, for traces.
func (e *APIError) StatusCode() int { return e.Status }

// end::in-flight[]

// tag::status-error[]

// statusError builds the APIError for a response that wasn't 200, using
// the API's message when the body has one.
func statusError(resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Status:     resp.StatusCode,
		Message:    "no details",
		RetryAfter: agent.RetryAfter(resp.Header, time.Now()),
	}
	var b struct {
		Error apiError `json:"error"`
	}
	switch {
	case json.Unmarshal(body, &b) == nil && b.Error.Message != "":
		e.Message = b.Error.Message
		e.LimitSource = b.Error.Metadata.LimitSource
	case len(body) > 0: // not the API's JSON, such as a proxy's page
		e.Message = string(body[:min(len(body), 200)])
	}
	return e
}

// end::status-error[]
