package openrouter

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// tag::api-error[]

// An APIError is a request the API refused, or a provider failure it
// reported. Callers inspect it with errors.AsType to decide what to do.
type APIError struct {
	Status     int           // HTTP status, or the code in the reply
	Message    string        // the API's own explanation
	RetryAfter time.Duration // the wait the API asked for, or 0
}

func (e *APIError) Error() string {
	status := strconv.Itoa(e.Status)
	if text := http.StatusText(e.Status); text != "" {
		status += " " + text
	}
	return fmt.Sprintf("openrouter: %s: %s", status, e.Message)
}

// Retryable reports whether the same request might succeed if sent
// again later: a timeout (408), too many requests (429), or trouble on
// the provider's side (5xx). Chapter 12 retries these.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// end::api-error[]

// tag::status-error[]

// statusError builds the APIError for a response that wasn't 200, using
// the API's message when the body has one.
func statusError(resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Status:     resp.StatusCode,
		Message:    "no details",
		RetryAfter: retryAfter(resp.Header),
	}
	var b struct {
		Error apiError `json:"error"`
	}
	switch {
	case json.Unmarshal(body, &b) == nil && b.Error.Message != "":
		e.Message = b.Error.Message
	case len(body) > 0: // not the API's JSON, such as a proxy's page
		e.Message = string(body[:min(len(body), 200)])
	}
	return e
}

// retryAfter reads a Retry-After header given in seconds, the form
// rate limits use.
func retryAfter(h http.Header) time.Duration {
	secs, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// end::status-error[]
