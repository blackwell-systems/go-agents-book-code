package safety

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// tag::audit[]

// An AuditEntry records one outcome at the gate: who decided what,
// when, about which exact change. MAC is a keyed hash of the entry and
// the MAC before it, so changing, removing, or inserting a line breaks
// the chain, and nobody without the key can build a new one.
type AuditEntry struct {
	Time    time.Time `json:"time"`
	Run     string    `json:"run"`
	Call    string    `json:"call"`
	Tool    string    `json:"tool"`
	Targets []string  `json:"targets,omitzero"`
	Hash    string    `json:"hash"`
	Outcome string    `json:"outcome"` // approved, denied, stale, ...
	By      string    `json:"by"`
	Reason  string    `json:"reason,omitzero"`
	MAC     string    `json:"mac"`
}

// end::audit[]

// tag::record[]

// An Audit appends entries to a file as JSON Lines, one writer at a
// time. Key signs the chain; keep it where the agent and its tools
// can't read it. A nil *Audit records nothing.
type Audit struct {
	W   io.Writer // syncs after each entry if it has a Sync method
	Key []byte
	Now func() time.Time // time.Now if nil

	mu   sync.Mutex
	prev string
}

// end::record[]

// tag::record-method[]

// Record appends one outcome.
func (a *Audit) Record(r agent.ApprovalRequest,
	d agent.ApprovalDecision, outcome string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e := AuditEntry{Time: a.now(), Run: r.Run, Call: r.Call.ID,
		Tool: r.Call.Name, Targets: r.Targets, Hash: r.Hash,
		Outcome: outcome, By: d.By, Reason: d.Reason}
	e.MAC = mac(a.Key, a.prev, e)
	line, err := json.Marshal(e, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if _, err := a.W.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if s, ok := a.W.(interface{ Sync() error }); ok {
		if err := s.Sync(); err != nil { // <1>
			return fmt.Errorf("audit: %w", err)
		}
	}
	a.prev = e.MAC
	return nil
}

// end::record-method[]

func (a *Audit) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// mac signs an entry, without its own MAC, together with the MAC of the
// entry before it.
func mac(key []byte, prev string, e AuditEntry) string {
	e.MAC = ""
	b, err := json.Marshal(e, jsontext.AllowInvalidUTF8(true), json.Deterministic(true))
	if err != nil {
		return "" // can't happen for this struct; an empty MAC won't verify
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(prev))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

// tag::open-audit[]

// OpenAudit opens an audit log to append to, picking up the chain where
// the last process left it, so a restart doesn't break it. It checks
// the whole chain first, and refuses to append to a broken one.
func OpenAudit(path string, key []byte) (*Audit, *os.File, error) {
	flags := os.O_RDWR | os.O_CREATE | os.O_APPEND
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open audit: %w", err)
	}
	last, _, err := verify(f, key)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return &Audit{W: f, Key: key, prev: last}, f, nil
}

// end::open-audit[]

// ErrAuditBroken means an audit log's chain doesn't hold: a line was
// changed, removed, or inserted, or signed with another key.
var ErrAuditBroken = errors.New("audit log chain broken")

// VerifyAudit checks every line's MAC against the line before it and
// returns the number of entries. A chain can't show its last lines were
// cut off; keep the latest MAC somewhere else to check that.
func VerifyAudit(r io.Reader, key []byte) (int, error) {
	_, n, err := verify(r, key)
	return n, err
}

func verify(r io.Reader, key []byte) (string, int, error) {
	return walk(r, key, func(AuditEntry) {})
}

// walk checks the chain as verify does, passing each entry to f once
// the entry's MAC is checked.
func walk(r io.Reader, key []byte, f func(AuditEntry)) (string, int,
	error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	prev, n := "", 0
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &e,
			jsontext.AllowInvalidUTF8(true)); err != nil {
			return "", n, fmt.Errorf("audit line %d: %w", n+1, err)
		}
		if !hmac.Equal([]byte(e.MAC), []byte(mac(key, prev, e))) {
			return "", n, fmt.Errorf("audit line %d: %w", n+1, ErrAuditBroken)
		}
		f(e)
		prev = e.MAC
		n++
	}
	if err := sc.Err(); err != nil {
		return "", n, fmt.Errorf("audit: %w", err)
	}
	return prev, n, nil
}

// tag::recall[]

// Recall rebuilds the gate's denials from an audit log, so a run
// restarted after a crash still refuses what a person refused before
// it (chapter 15). It reads the entries of run, or of every run if run
// is "", checking the chain as it goes, and returns how many denials
// it found. A broken chain is an error, and nothing is recalled.
func (g *Gate) Recall(r io.Reader, key []byte, run string) (int,
	error) {
	var denials []AuditEntry
	_, _, err := walk(r, key, func(e AuditEntry) {
		if e.Outcome == "denied" && (run == "" || e.Run == run) {
			denials = append(denials, e)
		}
	})
	if err != nil {
		return 0, err
	}
	for _, e := range denials {
		g.deny(agent.ApprovalRequest{Hash: e.Hash, Targets: e.Targets},
			e.Reason)
	}
	return len(denials), nil
}

// end::recall[]
