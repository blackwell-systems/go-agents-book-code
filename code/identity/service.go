package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// service is a stand-in for the system an agent acts on: it refunds
// orders, and it trusts no caller, only credentials it can verify.
type service struct {
	verify Verifier
	log    *auditLog
}

// tag::refund[]

// refund handles POST /refund?customer=ID. It verifies the credential
// and checks its scope before acting, and records the attempt either
// way, so a refused call leaves a trace too.
func (s *service) refund(w http.ResponseWriter, r *http.Request) {
	customer := r.URL.Query().Get("customer")
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	c, err := s.verify.Verify(tok)
	status := http.StatusUnauthorized
	if err == nil && !c.Has("refund:"+customer) {
		err = fmt.Errorf("%w: refund:%s", ErrScope, customer)
		status = http.StatusForbidden
	}
	rec := Record{
		Time: s.verify.Now(), User: c.User, Agent: c.Agent, Run: c.Run,
		Credential: c.ID, Action: "refund:" + customer, Allowed: err == nil,
	}
	if err != nil {
		rec.Reason = err.Error()
	}
	s.log.add(rec)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	fmt.Fprintf(w, "refunded customer %s\n", customer)
}

// end::refund[]

// errRefused is the tool's error when the service says no; its message
// is the service's, so the model learns why.
var errRefused = errors.New("refused")

// tag::tool[]

// refunder is the agent's refund tool. Its client was built by the
// harness for this run, with the run's credential in its transport.
type refunder struct {
	client *http.Client
	base   string // the service's URL
}

// Refund asks the service to refund customer. The call names the
// customer and nothing else; the model never sees a credential.
func (t refunder) Refund(
	ctx context.Context, customer string,
) (string, error) {
	u := t.base + "/refund?customer=" + url.QueryEscape(customer)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", fmt.Errorf("refund: %w", err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("refund: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return "", fmt.Errorf("refund: read reply: %w", err)
	}
	msg := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refund: %w: %s", errRefused, msg)
	}
	return msg, nil
}

// end::tool[]
