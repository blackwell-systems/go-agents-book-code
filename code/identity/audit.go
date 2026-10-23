package main

import (
	"sync"
	"time"
)

// tag::record[]

// Record is one entry in the audit log: for whom, which agent, which
// run, which credential, who approved it, what was tried, and whether
// it was allowed. It names the credential by ID, never by its token.
type Record struct {
	Time       time.Time `json:"time"`
	User       string    `json:"user"`
	Agent      string    `json:"agent"`
	Run        string    `json:"run"`
	Credential string    `json:"credential"`
	Approver   string    `json:"approver,omitzero"`
	Action     string    `json:"action"`
	Allowed    bool      `json:"allowed"`
	Reason     string    `json:"reason,omitzero"` // why it was refused
}

// end::record[]

// auditLog keeps records in memory, safe for concurrent handlers. A
// real one appends to a file and chains each entry to the last, as
// the site's approval guide shows, so edits and deletions show up.
type auditLog struct {
	mu      sync.Mutex
	records []Record
}

func (l *auditLog) add(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
}

func (l *auditLog) all() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Record(nil), l.records...)
}
