// Package service is the triage service (chapter 17): it takes crash
// reports from remind over HTTP, groups them by fingerprint, runs one
// agent per group with a bounded number at once, and keeps what a
// person has to decide in a review queue. Everything it knows is in a
// state directory, so a restart picks up where it stopped.
package service

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// tag::triager[]

// A Triager runs the agent on one fingerprint group, or resumes it
// from its checkpoint if an earlier process started it. reports are
// the run's, as filed; decisions come from ap; maxCost is the run's
// share of the day's budget. A run waiting for a person returns
// *agent.ApprovalPending, and one whose workspace changed since its
// checkpoint returns agent.ErrWorkspaceChanged.
type Triager interface {
	Triage(ctx context.Context, run Run, reports []triage.Report,
		ap agent.Approver, maxCost float64) (Outcome, error)
}

// An Outcome is what a run found for its group: the diagnosis status
// (fixed, already_fixed, duplicate, not_a_bug, needs_human), the issue
// it filed or found, and what it cost.
type Outcome struct {
	Status string
	Issue  int
	Cost   float64
	Turns  int
}

// end::triager[]

// Issues is the part of the tracker the service uses itself, outside
// any run: the open issue for a fingerprint (chapter 15's lookup), and
// a comment for reports that arrive after a run. Comment goes through
// the same egress check and approval gate as the agent's
// issue_comment, asking ap; it returns *agent.ApprovalPending while a
// person hasn't decided.
type Issues interface {
	Open(ctx context.Context, fingerprint string) (id int, ok bool,
		err error)
	IsOpen(ctx context.Context, id int) (bool, error)
	Comment(ctx context.Context, ap agent.Approver, id int, key,
		body string) error
}

// Config holds the service's settings.
type Config struct {
	Dir       string            // the state directory
	Secret    string            // what remind sends to post reports
	Reviewers map[string]string // review tokens, each to a name
	Triager   Triager
	Issues    Issues

	// Busy reports whether what a waiting run waits for, such as
	// another group's filing of the same cause, hasn't settled yet.
	Busy func(target string) bool

	Slots     int     // runs at once; default 2
	MaxQueued int     // runs waiting for a slot; default 20
	RunCost   float64 // each run's budget, in dollars
	DailyCost float64 // the day's budget; 0 means none
	TotalCost float64 // a budget that never starts over; 0: none

	// SpentSoFar, if set, returns what a run has spent by its own
	// record, with an allowance for a call in flight, so a hold a
	// killed process left is counted as that, at most the hold, not as
	// the whole hold, and the run can resume.
	SpentSoFar func(run string) (float64, bool)
	ReviewWait time.Duration // how long an approval waits; default 24h
	StaleAfter time.Duration // when an unknown outcome is flagged; 4h
	PerMinute  int           // reports a source may post; default 60
	Sweep      time.Duration // how often deadlines are checked
	Keep       time.Duration // finished runs keep their files; 7 days

	// Prune deletes a finished run's files, such as its workspace and
	// checkpoint, once Keep has passed; the run's record stays, and so
	// does its lock file, since a run ID is never used twice.
	Prune func(r Run) error

	Now func() time.Time
	Log *slog.Logger
}

// tag::run[]

// A RunState says where a group's run is.
type RunState string

// The states a run moves through.
const (
	Queued  RunState = "queued"  // waiting for a slot
	Running RunState = "running" // the agent is working
	Paused  RunState = "paused"  // waiting for a person
	Done    RunState = "done"    // finished with an answer
	Failed  RunState = "failed"  // stopped without one
)

// A Run is one fingerprint group's investigation, saved as
// runs/<id>.json.
type Run struct {
	ID          string   `json:"id"`
	Fingerprint string   `json:"fingerprint"`
	Commit      string   `json:"commit"`  // the build that crashed
	Reports     []string `json:"reports"` // the agent sees these
	Late        []string `json:"late,omitzero"`
	Posted      int      `json:"posted,omitzero"` // Late on the issue
	State       RunState `json:"state"`
	Started     bool     `json:"started,omitzero"` // the agent began
	Batch       int      `json:"batch,omitzero"`   // being posted: Late[Posted:Batch]
	RestartOf   string   `json:"restart_of,omitzero"`
	FollowUpOf  string   `json:"follow_up_of,omitzero"`
	Note        string   `json:"note,omitzero"`      // for a restarted run
	Pruned      bool     `json:"pruned,omitzero"`    // its files are gone
	Capped      bool     `json:"capped,omitzero"`    // stopped by money
	HandedOn    string   `json:"handed_on,omitzero"` // the run Late went to
	Declined    []int    `json:"declined,omitzero"`  // batches declined, by start
	Outcome     string   `json:"outcome,omitzero"`
	Issue       int      `json:"issue,omitzero"`
	Err         string   `json:"error,omitzero"`
	Cost        float64  `json:"cost"`
	Turns       int      `json:"turns,omitzero"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// end::run[]

func (r *Run) active() bool {
	return r.State == Queued || r.State == Running || r.State == Paused
}

// ErrDeclined is what Issues.Comment returns for a comment it won't
// post as written: a person denied it, its deadline passed, or the
// egress check refused its text. The batch is done with; the next
// late reports get a new comment and a new question.
var ErrDeclined = errors.New("service: comment declined")

// A Restart is a Triager's ErrWorkspaceChanged with what the restarted
// run should be told, such as what a person said of the calls the old
// run had in flight.
type Restart struct {
	Note string
	Err  error
}

func (r *Restart) Error() string { return r.Err.Error() }
func (r *Restart) Unwrap() error { return r.Err }

// ErrFull means the queue holds as many runs as it may; the client
// should post again later.
var ErrFull = errors.New("service: queue full")

// A Service is the triage service. New loads it from its state
// directory; Start sets it working; Shutdown stops it.
type Service struct {
	cfg    Config
	root   context.Context // every run's context derives from it
	cancel context.CancelCauseFunc
	review *reviewQueue
	limit  *limiter
	kick   chan struct{} // wakes the poster
	wg     sync.WaitGroup

	mu        sync.Mutex
	runs      map[string]*Run
	groups    map[string]string // fingerprint to its newest run
	queue     []string
	running   map[string]bool
	ledger    *ledger
	isStarted bool
}

// tag::new[]

// New opens the service's state directory and recovers its queue: a
// run that was going when the last process stopped is queued again,
// to resume from its checkpoint. ctx is the root of every run's
// context; it should outlive every request, so pass one that only
// ends with the process.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if err := cfg.checkSecrets(); err != nil {
		return nil, err
	}
	cfg.Slots = cmp.Or(cfg.Slots, 2)
	cfg.MaxQueued = cmp.Or(cfg.MaxQueued, 20)
	cfg.ReviewWait = cmp.Or(cfg.ReviewWait, 24*time.Hour)
	cfg.StaleAfter = cmp.Or(cfg.StaleAfter, 4*time.Hour)
	cfg.PerMinute = cmp.Or(cfg.PerMinute, 60)
	cfg.Sweep = cmp.Or(cfg.Sweep, time.Minute)
	cfg.Keep = cmp.Or(cfg.Keep, 7*24*time.Hour)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Log = cmp.Or(cfg.Log, slog.Default())
	for _, d := range []string{reportsDir, runsDir, reviewDir} {
		err := os.MkdirAll(filepath.Join(cfg.Dir, d), 0o700)
		if err != nil {
			return nil, fmt.Errorf("service: %w", err)
		}
	}

	// end::new[]

	// tag::new-open[]

	s := &Service{cfg: cfg, kick: make(chan struct{}, 1),
		runs: map[string]*Run{}, groups: map[string]string{},
		running: map[string]bool{},
		limit: &limiter{per: cfg.PerMinute, window: time.Minute,
			now: cfg.Now}}
	s.root, s.cancel = context.WithCancelCause(ctx) // <1>
	var err error
	if s.review, err = openReview(filepath.Join(cfg.Dir, reviewDir),
		cfg.ReviewWait, cfg.StaleAfter, cfg.Now); err != nil {
		return nil, err
	}
	s.review.busy = cfg.Busy
	if s.ledger, err = openLedger(filepath.Join(cfg.Dir, spendFile),
		cfg.DailyCost, cfg.TotalCost, cfg.Now,
		cfg.SpentSoFar); err != nil {
		return nil, err
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

// end::new-open[]

// minSecret is the shortest secret or token the service accepts: 128
// bits as hex, too many to guess one request at a time.
const minSecret = 32

// checkSecrets refuses to start with a secret or token short enough to
// guess, or a token without a reviewer's name for the audit.
func (c *Config) checkSecrets() error {
	if len(c.Secret) < minSecret {
		return fmt.Errorf("service: the ingest secret needs at least "+
			"%d characters", minSecret)
	}
	for tok, who := range c.Reviewers {
		if len(tok) < minSecret || strings.TrimSpace(who) == "" {
			return fmt.Errorf("service: each reviewer token needs at "+
				"least %d characters and a name", minSecret)
		}
		if tok == c.Secret {
			return errors.New("service: a reviewer token is the " +
				"ingest secret")
		}
	}
	return nil
}

// tag::recover[]

// recover reads the runs, queues every run that was queued or going,
// oldest first, and routes any report that reached the disk without
// reaching a run, as after a crash between the two writes.
func (s *Service) recover() error {
	runs, err := readAll[Run](filepath.Join(s.cfg.Dir, runsDir))
	if err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	slices.SortFunc(runs, func(a, b Run) int {
		return a.Created.Compare(b.Created)
	})
	seen := map[string]bool{}
	for _, r := range runs {
		if r.State == Running { // the last process stopped mid-run
			r.State = Queued
		}
		if r.State == Queued {
			s.queue = append(s.queue, r.ID)
		}
		s.runs[r.ID], s.groups[r.Fingerprint] = &r, r.ID
		for _, id := range slices.Concat(r.Reports, r.Late) {
			seen[id] = true
		}
	}

	// end::recover[]

	// tag::recover-reports[]

	reps, err := readAll[storedReport](filepath.Join(s.cfg.Dir,
		reportsDir))
	if err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	for _, rep := range reps {
		if !seen[rep.Report.ID] {
			// No queue limit: these were accepted already.
			if _, err := s.attach(rep, 0); err != nil {
				return fmt.Errorf("recover: %w", err)
			}
		}
	}
	return nil
}

// end::recover-reports[]

// tag::start[]

// Start starts the queued runs, the sweeper, which resumes runs whose
// review deadlines have passed, and the poster, which adds late
// reports to issues. Shutdown's cancellation stops them all.
func (s *Service) Start() {
	s.wg.Go(s.sweeper)
	s.wg.Go(s.poster)
	s.mu.Lock()
	s.isStarted = true
	s.mu.Unlock()
	s.sweep() // decisions made just before the last process stopped
}

// Shutdown cancels every run with agent.ErrShutdown as the cause and
// waits for them to save and return, or for ctx to end. Stop the HTTP
// server first, so no new work arrives: the checkpoints, not the
// running calls, keep the work, so Shutdown doesn't wait for a run to
// finish (chapter 15).
func (s *Service) Shutdown(ctx context.Context) error {
	s.cancel(agent.ErrShutdown)
	done := make(chan struct{})
	// If ctx ends first, this goroutine outlives Shutdown, but only
	// until the runs return, which the cancellation makes them do.
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("shutdown: %w", context.Cause(ctx))
	}
}

// end::start[]

// tag::add[]

// Add files reports from a trusted source, such as the command's
// crash file, keeping their IDs: it routes them as ingest does, with
// no secret, rate limit, or queue limit, and returns the runs they
// went to. Called before Start, it puts every report of a group in its
// run's first message.
func (s *Service) Add(ctx context.Context,
	reports ...triage.Report) ([]string, error) {
	var runs []string
	for _, rep := range reports {
		stored := storedReport{Report: rep,
			Fingerprint: triage.Fingerprint(rep), Received: s.cfg.Now()}
		issue, _, err := s.followup(ctx, stored.Fingerprint)
		if err != nil {
			return runs, err
		}
		s.mu.Lock()
		file := filepath.Join(s.cfg.Dir, reportsDir, rep.ID+".json")
		var got routed
		if !exists(file) {
			if err = writeJSON(file, stored); err == nil {
				got, err = s.attach(stored, issue)
			}
		}
		s.mu.Unlock()
		if err != nil {
			return runs, fmt.Errorf("add %s: %w", rep.ID, err)
		}
		if got.Run != "" && !slices.Contains(runs, got.Run) {
			runs = append(runs, got.Run)
		}
	}
	return runs, nil
}

// end::add[]

// Wait returns when no run is queued or running, or when ctx ends: a
// paused run waits for a person, not for the service, so it doesn't
// count, and neither does a queued run the day's budget can't start.
func (s *Service) Wait(ctx context.Context) error {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		if s.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait: %w", context.Cause(ctx))
		case <-t.C:
		}
	}
}

func (s *Service) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) == 0 &&
		(len(s.queue) == 0 || !s.ledger.canStart())
}

// Active reports whether a run is queued, running, or paused, for the
// cause record (package causes).
func (s *Service) Active(run string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[run]
	return ok && r.active()
}

// Runs returns every run, oldest first, as the report prints them.
func (s *Service) Runs() []Run { return s.snapshot() }

// newRunID returns an ID for a new run of a group: the fingerprint,
// the time, and a random part, since keys are made from run IDs and
// must never repeat (chapter 15).
func newRunID(fp string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%s", fp,
		now.UTC().Format("20060102T150405"), rand.Text()[:8])
}

func (s *Service) saveRun(r *Run) error {
	r.Updated = s.cfg.Now()
	return writeJSON(filepath.Join(s.cfg.Dir, runsDir, r.ID+".json"), r)
}
