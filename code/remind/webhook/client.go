package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ErrPermanent marks a failed attempt that retrying will not fix, such
// as a 404 from the subscriber.
var ErrPermanent = errors.New("webhook: permanent failure")

var defaultHTTP = &http.Client{Timeout: 10 * time.Second}

// A Client sends deliveries over HTTP.
type Client struct {
	HTTP   *http.Client     // nil means a client with a 10-second timeout
	Secret []byte           // signing key shared with subscribers
	Now    func() time.Time // nil means time.Now
	Log    *slog.Logger     // nil means no logging
}

// Post makes one attempt at d and records the status and error on it.
// A 2xx response is success. A 4xx response other than 408 and 429
// wraps ErrPermanent. Any other failure may be retried. Post marks d
// done on success and on permanent failure.
func (c *Client) Post(ctx context.Context, d *Delivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Payload))
	if err != nil {
		return record(d, 0, fmt.Errorf("webhook: %s: %w: %w", d.ID, ErrPermanent, err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Remind-Event", d.Event)
	req.Header.Set("X-Remind-Delivery", d.ID)
	req.Header.Set("X-Remind-Signature", Sign(c.Secret, c.now(), d.Payload))

	resp, err := c.client().Do(req)
	if err != nil {
		return record(d, 0, fmt.Errorf("webhook: %s: %w", d.ID, err))
	}
	defer resp.Body.Close()

	code := resp.StatusCode
	c.logger().Debug("webhook: attempt", "id", d.ID, "event", d.Event, "status", code)
	switch {
	case code >= 200 && code < 300:
		return record(d, code, nil)
	case code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests:
		return record(d, code, fmt.Errorf("webhook: %s: %s returned %s: %w", d.ID, d.URL, resp.Status, ErrPermanent))
	default:
		return record(d, code, fmt.Errorf("webhook: %s: %s returned %s", d.ID, d.URL, resp.Status))
	}
}

// record stores the outcome of an attempt on d and returns err.
func record(d *Delivery, status int, err error) error {
	d.LastStatus = status
	d.LastError = ""
	if err != nil {
		d.LastError = err.Error()
	}
	d.Done = err == nil || errors.Is(err, ErrPermanent)
	return err
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.New(slog.DiscardHandler)
}
