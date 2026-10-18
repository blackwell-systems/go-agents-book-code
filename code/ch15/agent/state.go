package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"time"
)

// tag::phase[]

// A Phase says what a saved run does next (chapter 15).
type Phase string

// The phases, in the order a turn goes through them.
const (
	PhaseModel  Phase = "model"  // waiting for the model's next reply
	PhaseTools  Phase = "tools"  // running the last reply's tool calls
	PhasePaused Phase = "paused" // waiting for a person's decision
	PhaseDone   Phase = "done"   // finished with an answer
)

// end::phase[]

// tag::state[]

// StateVersion is the version of State this code writes. A checkpoint
// store refuses a newer one, written by code that knows fields this
// code doesn't.
const StateVersion = 1

// State is what a run needs to continue after a restart: everything
// in its Result that a later turn depends on, and what the loop was
// doing when it was saved (chapter 15).
type State struct {
	Version int
	RunID   string
	Phase   Phase

	Messages []Message    // the conversation, without unsettled answers
	Usage    Usage        // saved, so a restart can't reset
	Turns    int          // the cost budget or the turn limit
	Context  ContextState // the context policy's summary and cuts
	Final    jsontext.Value
	Pending  []ToolCall // calls waiting for a person

	Attempts []Attempt // side-effecting calls started, not answered
	Resumes  int       // how many times the run was restarted
	Files    []File    // what the run wrote to its workspace
	Saved    time.Time // when this checkpoint was written
}

// end::state[]

// tag::attempt[]

// An Attempt is the marker saved just before a side-effecting call
// runs. A marker whose call has no answer means the call may have done
// its work or not: its outcome is unknown.
type Attempt struct {
	CallID  string
	Tool    string
	Key     string // the idempotency key the call was given
	Started time.Time
}

// end::attempt[]

// A File is one file the run changed in its workspace, with the SHA-256
// of what it wrote, so a resumed run can check that the workspace it
// finds is the one it left.
type File struct {
	Path string
	Hash string
}

// tag::checkpointer[]

// A Checkpointer saves a run's state. The loop calls Save after the
// model's reply is appended, before a side-effecting call runs, and
// after each tool result; Save must not return until the state would
// survive a crash.
type Checkpointer interface {
	Save(ctx context.Context, st State) error
}

// end::checkpointer[]

var (
	// ErrWorkspaceChanged means a file the run wrote is gone or no
	// longer holds what the run wrote.
	ErrWorkspaceChanged = errors.New("agent: workspace changed " +
		"since the checkpoint")

	// ErrShutdown is the cause set on a run's context when the
	// process is asked to stop. The checkpoint keeps the run's work.
	ErrShutdown = errors.New("agent: shutting down")
)

// NewState returns the state of a run that hasn't started.
func NewState(runID string, msgs []Message) State {
	return State{Version: StateVersion, RunID: runID, Phase: PhaseModel,
		Messages: slices.Clone(msgs)}
}

// Result returns what the saved run produced so far.
func (s *State) Result() Result {
	return Result{Messages: slices.Clone(s.Messages), Usage: s.Usage,
		Turns: s.Turns, Context: s.Context, Final: s.Final,
		Pending: slices.Clone(s.Pending)}
}

// Attempt returns the marker saved for a call, if there is one.
func (s *State) Attempt(callID string) (Attempt, bool) {
	i := slices.IndexFunc(s.Attempts, func(a Attempt) bool {
		return a.CallID == callID
	})
	if i < 0 {
		return Attempt{}, false
	}
	return s.Attempts[i], true
}

// Answer adds a tool message to the conversation and drops the call's
// marker: the call's outcome is known now.
func (s *State) Answer(m Message) {
	s.Messages = append(s.Messages, m)
	s.Attempts = slices.DeleteFunc(s.Attempts, func(a Attempt) bool {
		return a.CallID == m.ToolCallID
	})
}

// Unanswered returns the calls of the conversation's last reply that
// have no answer yet, in the model's order.
func (s *State) Unanswered() []ToolCall {
	calls, _ := unanswered(s.Messages)
	return calls
}

// tag::workspace[]

// Wrote records that the run wrote the file at path, relative to the
// workspace root, replacing any earlier record of the same file. Local
// file operations can't be cancelled, so it takes no context.
func (s *State) Wrote(root *os.Root, path string) error {
	h, err := fileHash(root, path)
	if err != nil {
		return fmt.Errorf("record %s: %w", path, err)
	}
	s.Files = slices.DeleteFunc(s.Files, func(f File) bool {
		return f.Path == path
	})
	s.Files = append(s.Files, File{Path: path, Hash: h})
	return nil
}

// end::workspace[]

// tag::check-workspace[]

// CheckWorkspace reports whether every file the run wrote still holds
// what it wrote. A resumed run that finds its workspace changed, or
// gone, can't trust what its conversation says about the code. It
// takes no context, as Wrote doesn't.
func (s *State) CheckWorkspace(root *os.Root) error {
	var errs []error
	for _, f := range s.Files {
		h, err := fileHash(root, f.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			errs = append(errs, fmt.Errorf("%w: %s is gone",
				ErrWorkspaceChanged, f.Path))
		case err != nil:
			errs = append(errs, fmt.Errorf("check %s: %w", f.Path, err))
		case h != f.Hash:
			errs = append(errs, fmt.Errorf("%w: %s was changed",
				ErrWorkspaceChanged, f.Path))
		}
	}
	return errors.Join(errs...)
}

// end::check-workspace[]

func fileHash(root *os.Root, path string) (string, error) {
	f, err := root.Open(path)
	if err != nil {
		return "", err //nolint:wrapcheck // callers wrap it
	}
	defer f.Close() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err //nolint:wrapcheck // callers wrap it
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
