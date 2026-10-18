package anthropic

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// tag::api-error[]

// APIError is an error the Messages API returned: a status that wasn't
// 200, or an error event in the middle of a stream.
type APIError struct {
	Status     int           // HTTP status, or the one its Type implies
	Type       string        // such as "overloaded_error"
	Message    string        // the API's own explanation
	RetryAfter time.Duration // the wait the API asked for, or 0
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anthropic: %d %s: %s", e.Status, e.Type,
		e.Message)
}

// Retryable reports whether the same request might succeed later: a
// timeout, too many requests, an overloaded API (529, which OpenRouter
// also sends for an overloaded provider), or trouble on the server's
// side. It has the same name and meaning as the OpenRouter client's,
// so chapter 12's retries treat both alike.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// RetryDelay is the wait the API asked for, which it sends with 429 and
// 529, or 0 (chapter 12).
func (e *APIError) RetryDelay() time.Duration { return e.RetryAfter }

// StatusCode is the error's HTTP status, for traces (chapter 12).
func (e *APIError) StatusCode() int { return e.Status }

// end::api-error[]

// errorBody is the API's error JSON:
// {"type": "error", "error": {"type": "...", "message": "..."}}.
type errorBody struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// bodyError returns the APIError a 200 response carries in place of a
// message, or nil.
func bodyError(body []byte) *APIError {
	var b struct {
		Type string `json:"type"`
		errorBody
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil //nolint:nilerr // a body that isn't JSON isn't an error object
	}
	if b.Type != "error" {
		return nil
	}
	e := &APIError{Type: b.Error.Type, Message: b.Error.Message,
		Status: typeStatus[b.Error.Type]}
	if e.Status == 0 {
		e.Status = http.StatusBadGateway
	}
	return e
}

// statusError builds the APIError for a response that wasn't 200.
func statusError(resp *http.Response, body []byte) *APIError {
	e := &APIError{Status: resp.StatusCode, Message: http.StatusText(
		resp.StatusCode)}
	var b errorBody
	if json.Unmarshal(body, &b) == nil && b.Error.Message != "" {
		e.Type, e.Message = b.Error.Type, b.Error.Message
	}
	e.RetryAfter = agent.RetryAfter(resp.Header, time.Now()) // chapter 12
	return e
}

// typeStatus is the HTTP status each error type comes with, for an
// error that arrives as a stream event, which has no status of its own.
var typeStatus = map[string]int{
	"invalid_request_error": 400,
	"authentication_error":  401,
	"billing_error":         402,
	"permission_error":      403,
	"not_found_error":       404,
	"conflict_error":        409,
	"request_too_large":     413,
	"rate_limit_error":      429,
	"api_error":             500,
	"timeout_error":         504,
	"overloaded_error":      529,
}
