// Command identity shows an agent's three identities, the user it acts
// for, the agent itself, and the run, carried by a short-lived, scoped
// credential: the harness mints it, a tool's transport attaches it, a
// service verifies it and checks its scope, a sub-agent gets a
// narrower one, and every attempt lands in an audit log. It needs no
// model, no API key, and no network beyond the local machine.
//
//	go run ./identity
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Stdout, time.Now); err != nil {
		log.Fatal(err)
	}
}

// run plays one support run: a refund the credential allows, one it
// doesn't, and a sub-agent that can't do what its parent could.
func run(ctx context.Context, w io.Writer, now func() time.Time) error {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	issuer := Issuer{Key: key}
	audit := &auditLog{}
	svc := &service{verify: Verifier{Key: pub, Now: now}, log: audit}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /refund", svc.refund)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		srv.Close() // Serve then returns http.ErrServerClosed
		<-served
	}()
	base := "http://" + ln.Addr().String()

	// tag::start[]
	// The run starts: user u-42 asked the support agent for a refund on
	// their own order, so the harness mints a ten-minute credential
	// with exactly that scope.
	parent := Claims{
		ID: "cred-1", User: "u-42", Agent: "support", Run: "run-7",
		Scopes:  []string{"refund:42", "read:orders"},
		Expires: now().Add(10 * time.Minute),
	}
	token, err := issuer.Mint(parent)
	if err != nil {
		return err
	}
	tool := refunder{base: base, client: &http.Client{
		Transport: attach{token: token, next: http.DefaultTransport},
	}}
	// end::start[]

	fmt.Fprintf(w, "run %s: agent %s acting for %s, scopes %s\n",
		parent.Run, parent.Agent, parent.User,
		strings.Join(parent.Scopes, " "))
	for _, customer := range []string{"42", "43"} {
		out, err := tool.Refund(ctx, customer)
		fmt.Fprintf(w, "  refund %s: %s\n", customer, result(out, err))
	}

	// tag::sub-agent[]
	// A sub-agent that only reads orders gets a narrower credential.
	// Asking for a scope the parent lacks fails before any token exists.
	_, err = Narrow(parent, Grant{
		ID: "cred-2", Agent: "reader", Scopes: []string{"refund:43"},
		TTL: time.Hour,
	}, now())
	fmt.Fprintf(w, "narrow to refund:43: %s\n", result("", err))
	child, err := Narrow(parent, Grant{
		ID: "cred-2", Agent: "reader", Scopes: []string{"read:orders"},
		TTL: time.Hour,
	}, now())
	if err != nil {
		return err
	}
	childToken, err := issuer.Mint(child)
	if err != nil {
		return err
	}
	// end::sub-agent[]
	sub := refunder{base: base, client: &http.Client{
		Transport: attach{token: childToken, next: http.DefaultTransport},
	}}
	fmt.Fprintf(w, "sub-agent %s, scopes %s, expires with its parent: %t\n",
		child.Agent, strings.Join(child.Scopes, " "),
		child.Expires.Equal(parent.Expires))
	out, err := sub.Refund(ctx, "42")
	fmt.Fprintf(w, "  refund 42: %s\n", result(out, err))

	fmt.Fprintln(w, "audit log:")
	for _, r := range audit.all() {
		fmt.Fprintf(w, "  %-7s %-7s %-6s %-6s %-9s allowed=%t\n",
			r.User, r.Agent, r.Run, r.Credential, r.Action, r.Allowed)
	}
	return nil
}

// result formats a tool's outcome for the demo's output.
func result(out string, err error) string {
	if err != nil {
		return err.Error()
	}
	return out
}
