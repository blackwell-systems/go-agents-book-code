package triage

import (
	"fmt"
	"os"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::env[]

// An Env is everything the triage tools work on: the crash reports, the
// agent's working copy of remind, an untouched copy to check
// reproducers against, and the issue tracker.
// Its bookkeeping is safe to use from several goroutines, but the files
// are not locked: two code_patch calls on one file at once could lose
// an edit, which chapter 9's parallel tool calls must not allow.
type Env struct {
	Reports  []Report
	Issues   *tracker.Store
	Dir      string // the workspace: the agent's copy of remind
	Original string // remind as it was before the agent changed it

	root *os.Root // Dir, opened so no path can leave it

	mu      sync.Mutex
	read    map[string]bool // files code_read has shown the model
	created map[string]bool // files the agent created
}

// end::env[]

// NewEnv opens the workspace for the tools.
func NewEnv(dir, original string, reports []Report,
	issues *tracker.Store) (*Env, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	return &Env{Reports: reports, Issues: issues, Dir: dir,
		Original: original, root: root, read: map[string]bool{},
		created: map[string]bool{}}, nil
}

// Close releases the workspace.
func (e *Env) Close() error {
	if err := e.root.Close(); err != nil {
		return fmt.Errorf("close workspace: %w", err)
	}
	return nil
}

// markRead records that the model has seen a file.
func (e *Env) markRead(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.read[path] = true
}

// hasRead reports whether the model has seen a file, or made it.
func (e *Env) hasRead(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.read[path] || e.created[path]
}

// markCreated records a file the agent made, such as a reproducer test.
func (e *Env) markCreated(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.created[path] = true
}

// Created returns the files the agent made.
func (e *Env) Created() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for p := range e.created {
		out = append(out, p)
	}
	return out
}
