// Package webhook delivers signed event payloads to subscriber URLs and
// retries failed deliveries with backoff.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrMalformedHeader means a signature header could not be parsed.
var ErrMalformedHeader = errors.New("webhook: malformed signature header")

// ErrBadSignature means no signature in the header matches the body.
var ErrBadSignature = errors.New("webhook: signature mismatch")

// ErrStale means the signed timestamp is outside the allowed tolerance.
var ErrStale = errors.New("webhook: timestamp outside tolerance")

// Sign returns the X-Remind-Signature header value for body sent at ts:
// "t=<unix seconds>,v1=<hex HMAC-SHA256 of "<unix>.<body>">".
func Sign(secret []byte, ts time.Time, body []byte) string {
	unix := ts.Unix()
	return "t=" + strconv.FormatInt(unix, 10) + ",v1=" + hex.EncodeToString(mac(secret, unix, body))
}

// Verify checks a signature header produced by Sign. It accepts the
// header if any v1 signature matches and the timestamp is within
// tolerance of now.
func Verify(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) error {
	unix, sigs, err := parseHeader(header)
	if err != nil {
		return err
	}
	want := mac(secret, unix, body)
	ok := false
	for _, sig := range sigs {
		if hmac.Equal(sig, want) {
			ok = true
		}
	}
	if !ok {
		return ErrBadSignature
	}
	if age := now.Sub(time.Unix(unix, 0)); age.Abs() > tolerance {
		return fmt.Errorf("%w: signed %s ago", ErrStale, age)
	}
	return nil
}

func mac(secret []byte, unix int64, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(strconv.AppendInt(nil, unix, 10))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// parseHeader splits "t=...,v1=...[,v1=...]" into its timestamp and
// decoded signatures. Unknown keys are ignored.
func parseHeader(header string) (int64, [][]byte, error) {
	var (
		unix  int64
		haveT bool
		sigs  [][]byte
	)
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return 0, nil, fmt.Errorf("%w: %q", ErrMalformedHeader, part)
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("%w: timestamp %q", ErrMalformedHeader, v)
			}
			unix, haveT = n, true
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil {
				return 0, nil, fmt.Errorf("%w: signature %q", ErrMalformedHeader, v)
			}
			sigs = append(sigs, b)
		}
	}
	if !haveT {
		return 0, nil, fmt.Errorf("%w: no timestamp", ErrMalformedHeader)
	}
	if len(sigs) == 0 {
		return 0, nil, fmt.Errorf("%w: no v1 signature", ErrMalformedHeader)
	}
	return unix, sigs, nil
}
