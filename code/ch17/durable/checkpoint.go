package durable

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// A Checkpoint is one run's state on disk: <dir>/<run>.json, with
// <dir>/<run>.lock held while a process has the run open. Saves are
// serialized. Hold the Checkpoint, and Close it, for the whole run: a
// dropped one releases the lock when it is garbage collected. It is an
// agent.Checkpointer.
type Checkpoint struct {
	dir  string
	run  string
	path string
	lock *os.File
	now  func() time.Time
	mu   sync.Mutex // one save at a time

	// For tests: noSync skips the syncs, which cost tens of
	// milliseconds each on some machines, and crash, if set, is called
	// at each step of a save, so a test can stop the save there.
	noSync bool
	crash  func(point string)
}

// tag::open[]

// Open opens the run's checkpoint in dir, creating dir if needed, and
// locks it, so no other process can open the same run until Close.
// It returns ErrLocked if another process has it open. Local file
// operations can't be cancelled, so it takes no context.
func Open(dir, run string) (*Checkpoint, error) {
	if !validRun(run) {
		return nil, fmt.Errorf("open checkpoint: bad run ID %q", run)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // <1>
		return nil, fmt.Errorf("open checkpoint: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, run+".lock"),
		os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint: %w", err)
	}
	if err := lock(f); err != nil { // <2>
		f.Close()
		return nil, fmt.Errorf("open checkpoint %s: %w", run, err)
	}
	c := &Checkpoint{dir: dir, run: run, lock: f, now: time.Now,
		path: filepath.Join(dir, run+".json")}
	c.removeTemps() // <3>
	return c, nil
}

// end::open[]

// Close releases the run's lock.
func (c *Checkpoint) Close() error {
	// Unlock before closing: a program started at this moment holds the
	// file until it execs, and the next Open in this process would find
	// the run locked. The lock file stays, since removing it would race
	// with a process about to lock it.
	err := unlock(c.lock)
	if cerr := c.lock.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("close checkpoint: %w", err)
	}
	return nil
}

// Load returns the saved state, or ErrNoCheckpoint if the run was
// never saved. Local file operations can't be cancelled, so it takes
// no context. A checkpoint that holds another run, such as a copied
// file, is an error: keys come from the run ID, and the other run's
// keys would be sent again.
func (c *Checkpoint) Load() (agent.State, error) {
	b, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return agent.State{}, ErrNoCheckpoint
	}
	if err != nil {
		return agent.State{}, fmt.Errorf("load checkpoint: %w", err)
	}
	st, err := decode(b)
	if err == nil && st.RunID != c.run {
		return agent.State{}, fmt.Errorf("load checkpoint %s: it holds "+
			"run %q", c.run, st.RunID)
	}
	return st, err
}

// tag::decode[]

// decode reads a checkpoint, refusing any version but this code's: a
// newer one has fields this code doesn't know. An older version would
// be migrated here; there is only one so far.
func decode(b []byte) (agent.State, error) {
	var v struct{ Version int }
	if err := json.Unmarshal(b, &v); err != nil {
		return agent.State{}, fmt.Errorf("load checkpoint: %w", err)
	}
	if v.Version != agent.StateVersion {
		return agent.State{}, fmt.Errorf("%w: version %d, this code "+
			"reads %d", ErrVersion, v.Version, agent.StateVersion)
	}
	var st agent.State
	if err := json.Unmarshal(b, &st, format,
		json.RejectUnknownMembers(true)); err != nil {
		return agent.State{}, fmt.Errorf("load checkpoint: %w", err)
	}
	return st, nil
}

// end::decode[]

// tag::restore[]

// Restore loads the state of a run being restarted and counts the
// restart, saving the count before anything else happens, so a run
// that crashes during every restart still reaches the limit. More than
// limit restarts is ErrMaxResumes.
func (c *Checkpoint) Restore(ctx context.Context,
	limit int) (agent.State, error) {
	st, err := c.Load()
	if err != nil {
		return agent.State{}, err
	}
	if st.Phase == agent.PhaseDone { // finishing needs no restart
		return st, nil
	}
	if st.Resumes >= limit {
		return st, fmt.Errorf("%w: run %s restarted %d times",
			ErrMaxResumes, c.run, st.Resumes)
	}
	st.Resumes++
	if err := c.Save(ctx, st); err != nil {
		return agent.State{}, err
	}
	return st, nil
}

// end::restore[]

// tag::save[]

// Save writes st so that a crash at any moment leaves either the old
// checkpoint or the new one, whole. It ignores ctx's cancellation: the
// save that keeps a stopping run's work must finish. An error means the
// new state may or may not be on disk, and the old one is undamaged:
// stop the run, and resume from whatever Load returns.
func (c *Checkpoint) Save(_ context.Context, st agent.State) error {
	if st.RunID != c.run { // it couldn't be loaded again
		return fmt.Errorf("save checkpoint %s: the state is run %q",
			c.run, st.RunID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st.Version, st.Saved = agent.StateVersion, c.now().UTC()
	b, err := json.Marshal(st, format) // <1>
	if err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	if err := c.write(b); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return nil
}

// end::save[]

// tag::format[]

// format is how a checkpoint is written. Zero fields are left out, so
// a nil field loads as nil, not as an empty value. Every jsontext.Value
// is written as a string: a model's tool-call arguments can be invalid
// JSON, and a run that holds such a call must still be saved.
var format = json.JoinOptions(
	json.OmitZeroStructFields(true),
	json.WithMarshalers(json.MarshalFunc(
		func(v jsontext.Value) ([]byte, error) {
			//nolint:wrapcheck // json.Marshal wraps it
			return jsontext.AppendQuote(nil, v)
		})),
	json.WithUnmarshalers(json.UnmarshalFunc(
		func(b []byte, v *jsontext.Value) error {
			s, err := jsontext.AppendUnquote(nil, b)
			*v = s
			return err //nolint:wrapcheck // json wraps it
		})),
)

// end::format[]

// tag::write[]

// write replaces the checkpoint file with b: write a temporary file,
// sync it, rename it over the old one, then sync the directory.
func (c *Checkpoint) write(b []byte) error {
	tmp, err := os.CreateTemp(c.dir, "."+c.run+"-*.tmp") // <1>
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	_, err = tmp.Write(b)
	if err == nil {
		err = c.sync(tmp) // <2>
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	c.point("mid-save")                                   // <3>
	if err := os.Rename(tmp.Name(), c.path); err != nil { // <4>
		return fmt.Errorf("rename: %w", err)
	}
	c.point("after rename")
	if err := c.syncDir(); err != nil { // <5>
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

// end::write[]

// tag::sync-dir[]

// syncDir makes the rename durable: the new name is an entry in the
// directory, and until the directory is synced, a power loss can bring
// back the old entry, or none.
func (c *Checkpoint) syncDir() error {
	d, err := os.Open(c.dir)
	if err != nil {
		return err //nolint:wrapcheck // write wraps it
	}
	defer d.Close() // read-only: Sync reported what matters
	return c.sync(d)
}

// end::sync-dir[]

func (c *Checkpoint) sync(f *os.File) error {
	if c.noSync {
		return nil
	}
	return f.Sync() //nolint:wrapcheck // write wraps it
}

func (c *Checkpoint) point(name string) {
	if c.crash != nil {
		c.crash(name)
	}
}

// removeTemps deletes temporary files a crashed save left behind. Only
// the process holding the lock calls it, so no save is in progress.
func (c *Checkpoint) removeTemps() {
	names, _ := filepath.Glob(filepath.Join(c.dir, "."+c.run+"-*.tmp"))
	for _, n := range names {
		os.Remove(n) // what's left over is harmless, only untidy
	}
}

// validRun reports whether a run ID is safe as a file name: letters,
// digits, '-', '_', and '.', not starting with '.'.
func validRun(run string) bool {
	if run == "" || len(run) > 128 || strings.HasPrefix(run, ".") {
		return false
	}
	for _, r := range run {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z',
			'0' <= r && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// Prune deletes the checkpoints of runs in dir that finished more than
// keep ago, and returns how many it deleted. A run another process has
// open is skipped, and lock files are never deleted. A file that isn't
// a checkpoint, or one that can't be read, such as one written by newer
// code, is skipped too, and reported in the error, which is only a
// report: every other run was pruned. Local file operations can't be
// cancelled, so it takes no context. Checkpoints hold tool output,
// which can include
// what the agent read, so they shouldn't be kept longer than needed.
func Prune(dir string, keep time.Duration, now time.Time) (int, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	n := 0
	var errs []error
	for _, name := range names {
		run := strings.TrimSuffix(filepath.Base(name), ".json")
		if !validRun(run) {
			continue // not a checkpoint this code wrote
		}
		done, err := pruneOne(dir, run, now.Add(-keep))
		if err != nil && !errors.Is(err, ErrLocked) {
			errs = append(errs, err)
		}
		if done {
			n++
		}
	}
	return n, errors.Join(errs...)
}

func pruneOne(dir, run string, before time.Time) (bool, error) {
	c, err := Open(dir, run)
	if err != nil {
		return false, err
	}
	defer c.Close() // the lock only: nothing was written
	st, err := c.Load()
	if err != nil || st.Phase != agent.PhaseDone ||
		!st.Saved.Before(before) {
		return false, err
	}
	// A tombstone first: the run ID was used, and a service keeps keys
	// made from it after the run is gone, so it is never used again.
	if err := os.WriteFile(Tombstone(dir, run), nil, 0o600); err != nil {
		return false, fmt.Errorf("prune %s: %w", run, err)
	}
	if err := os.Remove(c.path); err != nil {
		return false, fmt.Errorf("prune %s: %w", run, err)
	}
	// The lock file stays: a process may have it open, about to lock
	// it, and a new file of the same name would give a second process
	// a lock of its own.
	return true, nil
}

// Tombstone is the file Prune leaves for a run it deleted, so that its
// ID isn't used again.
func Tombstone(dir, run string) string {
	return filepath.Join(dir, run+".pruned")
}
