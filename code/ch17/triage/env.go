package triage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
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

	// DiagnoseOnly says the run has no tool that changes remind's code
	// (DiagnoseTools): the gate checks only that a reproducer fails as
	// it should on the original code, and no group can be fixed
	// (chapter 17).
	DiagnoseOnly bool

	// Target is the service the run triages: its name, its module
	// path, and its protected paths; NewEnv sets Remind (chapter 17).
	Target Target

	// History adds the git-history tools, code_history and
	// code_blame, when the workspace has its own history (chapter 17).
	History bool

	// Keys says the run gives each call an idempotency key and the
	// tracker honors it, so issue_file and issue_comment are Keyed: the
	// same call is safe to repeat (chapter 15). Without it they are
	// neither read-only, idempotent, nor keyed, and an unknown outcome
	// goes to a person.
	Keys bool

	// Tests, if set, runs every go test the tools start, in dir, and
	// returns go test -json's output, such as a sandbox's Test method
	// (chapter 14). Nil runs agent.GoTest, chapter 4's runner.
	Tests func(ctx context.Context, dir string, timeout time.Duration,
		args ...string) (string, error)

	root *os.Root // Dir, opened so no path can leave it

	mu            sync.Mutex
	refusedGroups map[string]bool // the gate's last verdict was a refusal
	gateEvents    []GateEvent
	read          map[string]bool     // files code_read has shown the model
	created       map[string]bool     // files the agent created
	notes         map[string][]string // notes by fingerprint group (chapter 8)
	noteKeys      map[string]bool     // calls whose notes are in notes
	filed         map[string][]filing // issues this run filed, by group
	keep          string              // the tool state's file (chapter 15)
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
		Original: original, Target: Remind, root: root, read: map[string]bool{},
		created: map[string]bool{}, notes: map[string][]string{},
		noteKeys: map[string]bool{}}, nil
}

// Close releases the workspace.
func (e *Env) Close() error {
	if err := e.root.Close(); err != nil {
		return fmt.Errorf("close workspace: %w", err)
	}
	return nil
}

// markRead records that the model has seen a file.
func (e *Env) markRead(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.read[path] = true
	return e.persist()
}

// hasRead reports whether the model has seen a file, or made it.
func (e *Env) hasRead(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.read[path] || e.created[path]
}

// markCreated records a file the agent made, such as a reproducer test.
func (e *Env) markCreated(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.created[path] = true
	return e.persist()
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
