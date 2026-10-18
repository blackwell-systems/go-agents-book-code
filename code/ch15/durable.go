package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/durable"
	"github.com/blackwell-systems/go-agents-book/code/ch15/safety"
)

// grace is how long a side-effecting call may run on after a shutdown
// signal, so a call already talking to the tracker can hear its answer.
const grace = 5 * time.Second

// tag::shutdown[]

// withShutdown returns a context that the first SIGINT or SIGTERM
// cancels with agent.ErrShutdown as its cause: the run stops, saves
// where it was, and the program exits. A second signal exits at once.
// stop releases the signals and ends the goroutine that waits for them.
func withShutdown(parent context.Context) (ctx context.Context,
	stop func()) {
	ctx, cancel := context.WithCancelCause(parent)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel(agent.ErrShutdown) // <1>
		case <-done:
			return
		}
		select {
		case <-sigs:
			os.Exit(130) // <2>
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(sigs)
		close(done)
		cancel(nil)
	}
}

// end::shutdown[]

// tag::open-run[]

// A savedRun is what -state adds to a run: its checkpoint, the state to
// start from, and whether that state is an earlier process's.
type savedRun struct {
	cp      *durable.Checkpoint
	st      agent.State
	resumed bool
}

// lockRun opens the run's checkpoint in the state directory, locked
// for this process, before anything else touches the run's files.
func lockRun(c config) (*savedRun, error) {
	cp, err := durable.Open(c.state, runID(c))
	if err != nil {
		return nil, fmt.Errorf("open run: %w", err)
	}
	return &savedRun{cp: cp}, nil
}

// end::open-run[]

// tag::restore-run[]

// restore loads the run, counting the restart; a run never saved
// starts from msgs, and is saved before it does anything: work is
// accepted only once it is on disk.
func (r *savedRun) restore(ctx context.Context, c config,
	msgs []agent.Message) error {
	st, err := r.cp.Restore(ctx, cmp.Or(c.maxResumes, 10))
	r.st, r.resumed = st, err == nil
	if errors.Is(err, durable.ErrNoCheckpoint) {
		r.st = agent.NewState(runID(c), msgs)
		err = r.cp.Save(ctx, r.st)
	}
	if err != nil {
		return fmt.Errorf("open run %s: %w", runID(c), err)
	}
	return nil
}

// end::restore-run[]

// tag::pick-run[]

// pickRun decides which run this start is, and takes its lock: the run
// -run names; or, with no -run, the newest unfinished run of the task
// in the state directory, so that starting the same command again, as
// a platform restarting a container does, resumes it; or a new run.
func pickRun(c *config, p *printer) (*savedRun, error) {
	if c.runID == "" {
		if id, ok := latestRun(c.state, c.task); ok {
			c.runID = id
		} else {
			c.runID = newRunID(c.task)
		}
	}
	if pruned(*c) {
		return nil, fmt.Errorf("run %s was pruned; an ID isn't "+
			"used twice", c.runID)
	}
	saved, err := lockRun(*c)
	if err != nil {
		return nil, err
	}
	p.printf("run %s, in %s\n", c.runID, c.state)
	return saved, nil
}

// end::pick-run[]

// latestRun returns the newest unfinished run of a task in the state
// directory. A run another process has open, or one that can't be read,
// is passed over.
func latestRun(state, task string) (string, bool) {
	names, _ := filepath.Glob(filepath.Join(state, task+"-*.json"))
	ours := regexp.MustCompile("^" + regexp.QuoteMeta(task) +
		`-\d{8}T\d{6}-[A-Z2-7]{8}$`) // newRunID's, for this task only
	var best string
	var newest time.Time
	for _, n := range names {
		id := strings.TrimSuffix(filepath.Base(n), ".json")
		if !ours.MatchString(id) {
			continue
		}
		cp, err := durable.Open(state, id)
		if err != nil {
			continue
		}
		st, err := cp.Load()
		cp.Close()
		if err == nil && st.Phase != agent.PhaseDone &&
			st.Saved.After(newest) {
			best, newest = id, st.Saved
		}
	}
	return best, best != ""
}

// pruned reports whether the run's ID was used before and pruned: Prune
// leaves a tombstone. Keys are made from the run ID, and a service keeps
// keys after the run is gone, so an ID is never used twice. A run whose
// first start failed before it was saved never ran, and its ID can be
// used.
func pruned(c config) bool {
	_, err := os.Stat(durable.Tombstone(c.state, c.runID))
	return err == nil
}

// ready reports whether a run's directory was set up in full.
func ready(work string) bool {
	_, err := os.Stat(filepath.Join(work, "ready"))
	return err == nil
}

// keptMarker returns the data marker saved in a run's directory, in
// the file boundary, or makes one and saves it: a resumed run keeps its
// boundary, which the saved conversation's rule names (chapter 14). A
// run saved before the file had that name kept it in marker, which is
// read too.
func keptMarker(work string) (safety.Marker, error) {
	for _, name := range []string{"boundary", "marker"} {
		b, err := os.ReadFile(filepath.Join(work, name))
		if err == nil && len(b) > 0 {
			return safety.MarkerWith(string(b)), nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return safety.Marker{}, fmt.Errorf("data marker: %w", err)
		}
	}
	m := safety.NewMarker()
	file := filepath.Join(work, "boundary")
	if err := os.WriteFile(file, []byte(m.Boundary()), 0o600); err != nil {
		return safety.Marker{}, fmt.Errorf("data marker: %w", err)
	}
	return m, nil
}

// runID is the run's ID, from -run.
func runID(c config) string { return c.runID }

// newRunID returns an ID for a new run: the task, the time, and a
// random part. Keys are made from the run ID and the call ID, and call
// IDs repeat across runs, so a run ID must never be reused: not the
// task's name alone, and not an ID that Prune deleted.
func newRunID(task string) string {
	return fmt.Sprintf("%s-%s-%s", task,
		time.Now().UTC().Format("20060102T150405"), rand.Text()[:8])
}

// workDir is where a saved run keeps its workspace and tracker.
func workDir(c config) string {
	return filepath.Join(c.state, runID(c)+".work")
}

// prune deletes finished runs older than -keep, with their workspaces,
// and reports how many it deleted. Zero keeps every run.
func prune(c config) (int, error) {
	if err := os.MkdirAll(c.state, 0o700); err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	if c.keep <= 0 {
		return 0, nil
	}
	n, err := durable.Prune(c.state, c.keep, time.Now())
	if err != nil { // a file it couldn't read: say so and go on
		log.Printf("prune: skipped: %v", err)
	}
	works, err := filepath.Glob(filepath.Join(c.state, "*.work"))
	if err != nil {
		return n, fmt.Errorf("prune: %w", err)
	}
	for _, w := range works {
		run := strings.TrimSuffix(filepath.Base(w), ".work")
		if err := pruneWork(c.state, run, w); err != nil {
			return n, err
		}
	}
	return n, nil
}

// pruneWork deletes a run's workspace if the run has no checkpoint and
// no process has it open: a run being set up holds its lock first.
func pruneWork(state, run, work string) error {
	cp, err := durable.Open(state, run)
	if errors.Is(err, durable.ErrLocked) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	defer cp.Close()
	if _, err := cp.Load(); !errors.Is(err, durable.ErrNoCheckpoint) {
		return nil // a run to resume, or one to look at by hand
	}
	if err := os.RemoveAll(work); err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	return nil
}

// withDieAfter applies -die-after to tools, the run's final set. Only
// a built-in tool can be named, one of builtin: the tools served over
// MCP, such as the tracker's with -tracker mcp, are not wrapped. A name
// no such tool has is an error that lists the ones it can be, and the
// flag needs -state, since a killed run without one can't be resumed.
func withDieAfter(tools []agent.Tool, builtin []string, name,
	state string) ([]agent.Tool, error) {
	var can []string
	for _, t := range tools {
		if slices.Contains(builtin, t.Def.Name) {
			can = append(can, t.Def.Name)
		}
	}
	switch {
	case state == "":
		return nil, errors.New("-die-after needs -state")
	case !slices.Contains(can, name):
		return nil, fmt.Errorf("-die-after: no built-in tool %q in this "+
			"run (tools served over MCP can't be named); try one of %s",
			name, strings.Join(can, ", "))
	}
	return agent.WrapTools(tools, dieAfter(name)), nil
}

// dieAfter returns a ToolWare that kills the process with SIGKILL as
// soon as the named tool's first call returns: the call took effect,
// and its answer isn't saved, which is the window the chapter's crash
// sweep covers. It is for demonstrations; nothing else uses it.
func dieAfter(name string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		if t.Def.Name != name {
			return t
		}
		run := t.Run
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			out, err := run(ctx, args)
			log.Printf("-die-after %s: the call returned (%q, %v); "+
				"killing the process", name, out, err)
			if err := killSelf(); err != nil {
				log.Fatalf("-die-after %s: %v", name, err)
			}
			select {} // the signal ends the process
		}
		return t
	}
}

// firstTurn is the number of the turn a run starts or resumes in: 1 for
// a new run; for a resumed one, the saved turn whose calls are still to
// run or waiting for a person, or else the turn after the saved ones,
// as after a crash during a model call; and 0 for a finished run,
// which has no turn left (chapter 15).
func firstTurn(saved *savedRun) int {
	switch {
	case saved == nil || !saved.resumed:
		return 1
	case saved.st.Phase == agent.PhaseDone:
		return 0
	case len(saved.st.Unanswered()) > 0:
		return max(saved.st.Turns, 1)
	}
	return saved.st.Turns + 1
}
