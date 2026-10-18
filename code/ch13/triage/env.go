package triage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::env[]

// An Env is everything the triage tools work on: the crash reports, the
// agent's working copy of remind, an untouched copy to check
// reproducers against, and the issue tracker.
// Its bookkeeping is safe to use from several goroutines, but the files
// are not locked: two code_patch calls on one file at once could lose
// an edit. Chapter 9's parallel tool calls never do that, since
// code_patch isn't read-only and so runs alone.
type Env struct {
	Reports  []Report
	Issues   *tracker.Store
	Dir      string // the workspace: the agent's copy of remind
	Original string // remind as it was before the agent changed it

	// tag::env-shared[]
	// TestSlots, if set, limits how many run_tests calls run at once,
	// across every Env that shares it: six agents each running the
	// whole suite with -race would starve each other (chapter 9).
	TestSlots agent.Slots

	// FileLater means the harness files issues after the run, so a
	// group can be "fixed" without an issue number: the per-group
	// agents of chapter 9 report and don't file.
	FileLater bool
	// end::env-shared[]

	// Gate, if set, checks the reproducer named with each issue before
	// filing it (chapter 13).
	Gate bool

	root *os.Root // Dir, opened so no path can leave it

	mu            sync.Mutex
	refusedGroups map[string]bool // the gate's last verdict was a refusal
	gateEvents    []GateEvent
	read          map[string]bool     // files code_read has shown the model
	created       map[string]bool     // files the agent created
	notes         map[string][]string // notes by fingerprint group (chapter 8)
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
		created: map[string]bool{}, notes: map[string][]string{}}, nil
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

// tag::workspace-gone[]

// present returns an error wrapping agent.ErrToolFatal when the
// workspace is gone, such as when its directory was deleted while the
// agent ran: nothing the model tries can bring it back.
func (e *Env) present() error {
	_, err := os.Stat(e.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: the workspace %s is gone", agent.ErrToolFatal,
			e.Dir)
	}
	return nil
}

// end::workspace-gone[]
