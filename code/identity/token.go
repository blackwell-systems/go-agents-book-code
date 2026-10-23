package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Errors a service checks with errors.Is.
var (
	ErrMalformed    = errors.New("identity: malformed token")
	ErrBadSignature = errors.New("identity: bad signature")
	ErrExpired      = errors.New("identity: token expired")
	ErrScope        = errors.New("identity: scope not held")
)

// tag::claims[]

// Claims is what a credential says: who the agent acts for, which
// agent and which run hold it, what it may do, and until when.
type Claims struct {
	ID      string    `json:"jti"`    // credential ID, for the audit log
	User    string    `json:"sub"`    // who the agent acts for
	Agent   string    `json:"agent"`  // which software
	Run     string    `json:"run"`    // which session or task
	Scopes  []string  `json:"scopes"` // what it may do
	Expires time.Time `json:"exp"`
}

// end::claims[]

// Has reports whether c grants scope.
func (c Claims) Has(scope string) bool {
	return slices.Contains(c.Scopes, scope)
}

// tag::mint[]

// Issuer mints credentials. Only the harness holds one: the model,
// the tools, and sub-agents never see its key.
type Issuer struct {
	Key ed25519.PrivateKey
}

// Mint signs c. The token is the claims as JSON and their signature,
// each base64url-encoded, joined by a dot.
func (is Issuer) Mint(c Claims) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}
	sig := ed25519.Sign(is.Key, body)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(sig), nil
}

// end::mint[]

// tag::verify[]

// Verifier checks credentials. A service holds one, with the issuer's
// public key.
type Verifier struct {
	Key ed25519.PublicKey
	Now func() time.Time
}

// Verify checks token's signature and expiry and returns its claims.
// It checks no scope: the caller does, for the action it is taking.
func (v Verifier) Verify(token string) (Claims, error) {
	var c Claims
	b, s, ok := strings.Cut(token, ".")
	if !ok {
		return c, ErrMalformed
	}
	enc := base64.RawURLEncoding
	body, err1 := enc.DecodeString(b)
	sig, err2 := enc.DecodeString(s)
	if err := errors.Join(err1, err2); err != nil {
		return c, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if !ed25519.Verify(v.Key, body, sig) {
		return c, ErrBadSignature
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return c, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if !v.Now().Before(c.Expires) {
		return c, ErrExpired
	}
	return c, nil
}

// end::verify[]
