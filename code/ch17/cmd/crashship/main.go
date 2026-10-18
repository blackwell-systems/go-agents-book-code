// Command crashship follows remind's crash file and posts each report
// to the triage service (chapter 17). Run it beside remind, with read
// access to remind's data directory:
//
//	TRIAGE_INGEST_SECRET=... crashship -file data/crashes.jsonl \
//		-url http://localhost:8417/crashes
//
// The secret comes from TRIAGE_INGEST_SECRET or from the file
// TRIAGE_INGEST_SECRET_FILE names, never a flag, since other users on
// the machine can read a process's arguments. The environment is
// enough here: crashship runs no code but its own.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/ship"
)

func main() {
	s := &ship.Shipper{}
	flag.StringVar(&s.File, "file", "data/crashes.jsonl",
		"remind's crash file")
	flag.StringVar(&s.Offset, "offset", "crashship.offset",
		"where to keep how far the file has been sent")
	flag.StringVar(&s.URL, "url", "http://localhost:8417/crashes",
		"the triage service's POST /crashes")
	flag.DurationVar(&s.Poll, "poll", time.Second,
		"how often to check the file")
	flag.Parse()
	if err := checkURL(s.URL); err != nil {
		log.Fatal(err)
	}
	secret, err := ingestSecret(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	s.Secret = secret
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt,
		syscall.SIGTERM)
	defer stop()
	// A signal ends Run with context.Canceled; the position is saved.
	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		stop()
		log.Fatal(err)
	}
}

// ingestSecret reads the secret from the file TRIAGE_INGEST_SECRET_FILE
// names, which only its owner may read, or else from
// TRIAGE_INGEST_SECRET.
func ingestSecret(getenv func(string) string) (string, error) {
	file := getenv("TRIAGE_INGEST_SECRET_FILE")
	if file == "" {
		if s := getenv("TRIAGE_INGEST_SECRET"); s != "" {
			return s, nil
		}
		return "", errors.New("set TRIAGE_INGEST_SECRET_FILE or " +
			"TRIAGE_INGEST_SECRET")
	}
	fi, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("ingest secret: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("ingest secret: %s can be read by others; "+
			"chmod 600 it", file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("ingest secret: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("ingest secret: %s is empty", file)
	}
	return s, nil
}

// checkURL refuses plain HTTP to anything but this machine, since the
// secret would cross the network readable, as the review command does.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("-url: %w", err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	local := host == "localhost" || ip != nil && ip.IsLoopback()
	switch {
	case u.Scheme == "https", u.Scheme == "http" && local:
		return nil
	case u.Scheme == "http":
		return fmt.Errorf("-url %s: use https for a service on another "+
			"machine", raw)
	}
	return fmt.Errorf("-url %s: not an http or https address", raw)
}
