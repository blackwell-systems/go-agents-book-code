package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func keys(t *testing.T) (Issuer, ed25519.PublicKey) {
	t.Helper()
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	return Issuer{Key: key}, key.Public().(ed25519.PublicKey)
}

func claims() Claims {
	return Claims{
		ID: "cred-1", User: "u-42", Agent: "support", Run: "run-7",
		Scopes:  []string{"refund:42", "read:orders"},
		Expires: t0.Add(10 * time.Minute),
	}
}

func TestVerify(t *testing.T) {
	is, pub := keys(t)
	tok, err := is.Mint(claims())
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	forged, err := Issuer{Key: other}.Mint(claims())
	if err != nil {
		t.Fatal(err)
	}
	body, sig, _ := strings.Cut(tok, ".")
	tests := []struct {
		name  string
		token string
		at    time.Time
		want  error
	}{
		{"valid", tok, t0, nil},
		{"expired", tok, t0.Add(10 * time.Minute), ErrExpired},
		{"another key", forged, t0, ErrBadSignature},
		{"swapped signature", body + "." + sig[1:] + "A", t0, ErrBadSignature},
		{"no dot", body, t0, ErrMalformed},
		{"not base64", "!!." + sig, t0, ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Verifier{Key: pub, Now: func() time.Time { return tt.at }}
			c, err := v.Verify(tt.token)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Verify: got %v, want %v", err, tt.want)
			}
			if err == nil && c.User != "u-42" {
				t.Errorf("User = %q, want u-42", c.User)
			}
		})
	}
}

func TestNarrow(t *testing.T) {
	parent := claims()
	tests := []struct {
		name    string
		g       Grant
		want    error
		expires time.Time
	}{
		{"subset, short", Grant{"c2", "reader", []string{"read:orders"},
			time.Minute}, nil, t0.Add(time.Minute)},
		{"subset, capped", Grant{"c2", "reader", []string{"read:orders"},
			time.Hour}, nil, parent.Expires},
		{"wider", Grant{"c2", "reader", []string{"refund:43"},
			time.Minute}, ErrScope, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Narrow(parent, tt.g, t0)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Narrow: got %v, want %v", err, tt.want)
			}
			if err != nil {
				return
			}
			if c.User != parent.User || c.Run != parent.Run {
				t.Errorf("user, run = %s, %s; want the parent's", c.User, c.Run)
			}
			if !c.Expires.Equal(tt.expires) {
				t.Errorf("Expires = %v, want %v", c.Expires, tt.expires)
			}
		})
	}
}

func TestAttachLeavesRequestAlone(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
		}))
	defer srv.Close()
	client := &http.Client{Transport: attach{"tok", http.DefaultTransport}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "Bearer tok" {
		t.Errorf("server saw %q, want Bearer tok", got)
	}
	if h := req.Header.Get("Authorization"); h != "" {
		t.Errorf("caller's request changed: Authorization = %q", h)
	}
}

func TestRefundService(t *testing.T) {
	is, pub := keys(t)
	audit := &auditLog{}
	now := func() time.Time { return t0 }
	svc := &service{verify: Verifier{Key: pub, Now: now}, log: audit}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /refund", svc.refund)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tok, err := is.Mint(claims())
	if err != nil {
		t.Fatal(err)
	}
	tool := refunder{base: srv.URL, client: &http.Client{
		Transport: attach{tok, http.DefaultTransport}}}
	anon := refunder{base: srv.URL, client: http.DefaultClient}
	ctx := context.Background()
	if _, err := tool.Refund(ctx, "42"); err != nil {
		t.Errorf("own refund: %v", err)
	}
	if _, err := tool.Refund(ctx, "43"); !errors.Is(err, errRefused) {
		t.Errorf("other customer: got %v, want refused", err)
	}
	if _, err := anon.Refund(ctx, "42"); !errors.Is(err, errRefused) {
		t.Errorf("no credential: got %v, want refused", err)
	}
	recs := audit.all()
	if len(recs) != 3 {
		t.Fatalf("audit has %d records, want 3", len(recs))
	}
	if !recs[0].Allowed || recs[1].Allowed || recs[2].Allowed {
		t.Errorf("allowed = %t %t %t, want true false false",
			recs[0].Allowed, recs[1].Allowed, recs[2].Allowed)
	}
	if recs[1].Credential != "cred-1" || recs[1].User != "u-42" {
		t.Errorf("refused record = %+v, want cred-1 for u-42", recs[1])
	}
}

func TestRun(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), &out, time.Now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"refund 42: refunded customer 42",
		"refund 43: refund: refused: identity: scope not held: refund:43",
		"narrow to refund:43: identity: scope not held: parent lacks refund:43",
		"expires with its parent: true",
		"allowed=false",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
