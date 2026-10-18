package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Instrument counts requests to next by method and status code in
// http_requests_total, and records their durations by method in
// http_request_duration_seconds. It times requests with r.Now.
func Instrument(r *Registry, next http.Handler) http.Handler {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	requests := r.Counter("http_requests_total", "HTTP requests served, by method and status code.", "method", "code")
	durations := r.Histogram("http_request_duration_seconds", "Time taken to serve HTTP requests.", nil, "method")

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		requests.With(req.Method, strconv.Itoa(sw.status)).Inc()
		durations.With(req.Method).Observe(now().Sub(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		return n, fmt.Errorf("write response: %w", err)
	}
	return n, nil
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
