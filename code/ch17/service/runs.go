package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// A storedReport is a report as reports/<id>.json keeps it: redacted,
// with its fingerprint and when it arrived.
type storedReport struct {
	Report      triage.Report `json:"report"`
	Fingerprint string        `json:"fingerprint"`
	Received    time.Time     `json:"received"`
}

// A routed says where a report went.
type routed struct {
	Run       string `json:"run,omitzero"`
	New       bool   `json:"new,omitzero"`  // it started a run
	Late      bool   `json:"late,omitzero"` // a comment, after the run
	Duplicate bool   `json:"duplicate,omitzero"`
}

// tag::ingest[]

// ingest saves a report and routes it by fingerprint: into a run that
// hasn't started, as a late report for a run that has, or into a new
// run. A report ID seen before is ignored, so a client's retry is
// harmless. The report is on disk before ingest returns.
func (s *Service) ingest(ctx context.Context,
	rep storedReport) (routed, error) {
	for {
		issue, seen, err := s.followup(ctx, rep.Fingerprint) // <1>
		if err != nil {
			return routed{}, err
		}
		s.mu.Lock()
		if s.groupState(rep.Fingerprint) != seen { // it moved: again
			s.mu.Unlock()
			continue
		}
		got, err := s.file(rep, issue)
		s.mu.Unlock()
		return got, err
	}
}

// end::ingest[]

// tag::ingest-file[]

// file saves and routes a report; the caller holds s.mu.
func (s *Service) file(rep storedReport, issue int) (routed, error) {
	file := filepath.Join(s.cfg.Dir, reportsDir, rep.Report.ID+".json")
	if exists(file) { // <1>
		return routed{Duplicate: true}, nil
	}
	if s.wouldStart(rep.Fingerprint, issue) &&
		len(s.queue) >= s.cfg.MaxQueued {
		return routed{}, ErrFull // <2>
	}
	if err := writeJSON(file, rep); err != nil {
		return routed{}, fmt.Errorf("ingest: %w", err)
	}
	return s.attach(rep, issue)
}

// end::ingest-file[]

// groupState names where a group's newest run is, so ingest can tell
// whether it changed while the tracker was asked. The caller holds
// s.mu.
func (s *Service) groupState(fp string) string {
	id, ok := s.groups[fp]
	if !ok {
		return ""
	}
	return id + " " + string(s.runs[id].State)
}

// tag::followup[]

// followup returns the open issue a report for a finished group should
// go to: the group's own run's issue if it is still open, which for a
// group answered as a duplicate of another group's issue is that
// issue; then the tracker's fingerprint lookup (chapter 15). It returns
// 0 if the group is new, still running, or its issue was closed, and
// the group's state as it saw it.
func (s *Service) followup(ctx context.Context,
	fp string) (int, string, error) {
	s.mu.Lock()
	seen := s.groupState(fp)
	id, ok := s.groups[fp]
	var own int
	finished := ok && !s.runs[id].active()
	if finished {
		own = s.runs[id].Issue
	}
	s.mu.Unlock()
	if !finished || s.cfg.Issues == nil {
		return 0, seen, nil
	}
	if own > 0 {
		open, err := s.cfg.Issues.IsOpen(ctx, own)
		if err != nil {
			return 0, seen, fmt.Errorf("look up #%d: %w", own, err)
		}
		if open {
			return own, seen, nil
		}
	}
	issue, open, err := s.cfg.Issues.Open(ctx, fp)
	if err != nil {
		return 0, seen, fmt.Errorf("look up %s: %w", fp, err)
	}
	if !open {
		return 0, seen, nil
	}
	return issue, seen, nil
}

// end::followup[]

// wouldStart reports whether a report for fp would start a new run.
func (s *Service) wouldStart(fp string, issue int) bool {
	id, ok := s.groups[fp]
	return !ok || !s.runs[id].active() && issue == 0
}

// tag::attach[]

// attach adds a saved report to its group's run, or starts one. The
// caller holds s.mu.
func (s *Service) attach(rep storedReport, issue int) (routed, error) {
	fp, id := rep.Fingerprint, rep.Report.ID
	if run, ok := s.groups[fp]; ok {
		r := s.runs[run]
		next := *r
		switch {
		case !r.Started: // the agent will see it in its first message
			next.Reports = append(slices.Clone(r.Reports), id)
		case r.active(), issue > 0: // too late for the agent
			next.Late = append(slices.Clone(r.Late), id)
			next.Issue = max(next.Issue, issue)
		default: // finished, and no open issue: a new run
			return s.newRun(rep)
		}
		if err := s.saveRun(&next); err != nil {
			return routed{}, fmt.Errorf("attach %s: %w", id, err)
		}
		late := r.Started
		*r = next
		s.poke()
		return routed{Run: run, Late: late}, nil
	}
	return s.newRun(rep)
}

// end::attach[]

func (s *Service) newRun(rep storedReport) (routed, error) {
	now := s.cfg.Now()
	r := &Run{ID: newRunID(rep.Fingerprint, now),
		Fingerprint: rep.Fingerprint, Commit: rep.Report.Commit,
		Reports: []string{rep.Report.ID}, State: Queued, Created: now}
	if err := s.saveRun(r); err != nil {
		return routed{}, fmt.Errorf("new run: %w", err)
	}
	s.runs[r.ID], s.groups[r.Fingerprint] = r, r.ID
	s.queue = append(s.queue, r.ID)
	s.dispatch()
	return routed{Run: r.ID, New: true}, nil
}

// tag::dispatch[]

// dispatch starts queued runs while there are free slots and money
// left today. The caller holds s.mu.
func (s *Service) dispatch() {
	for s.started() && len(s.running) < s.cfg.Slots &&
		len(s.queue) > 0 {
		id := s.queue[0]
		held, ok, err := s.ledger.reserve(id, s.cfg.RunCost)
		if err != nil { // unsaved, so not started; the sweeper retries
			s.cfg.Log.Error("save spending", "err", err)
			return
		}
		if !ok {
			return // the day's budget is spent; the sweeper retries
		}
		s.queue = s.queue[1:]
		s.start(id, held)
	}
}

// end::dispatch[]

// tag::start[]

// start runs one group's agent in a goroutine the service owns, with
// a context derived from the root, never from the request that
// brought the report: a client that hangs up doesn't stop the
// investigation.
func (s *Service) start(id string, held float64) {
	r := s.runs[id]
	r.State, r.Started = Running, true
	if err := s.saveRun(r); err != nil { // on disk it stays queued
		s.cfg.Log.Error("save run", "run", id, "err", err)
	}
	run := *r
	run.Reports, run.Late = slices.Clone(r.Reports), nil
	s.running[id] = true
	s.wg.Go(func() {
		ctx, cancel := context.WithCancelCause(s.root) // <1>
		defer cancel(nil)
		reports, err := s.reportsOf(run.Reports)
		var out Outcome
		if err == nil {
			out, err = s.cfg.Triager.Triage(ctx, run, reports,
				s.review.approver(id), held)
		}
		s.finish(ctx, id, out, err)
	})
}

// end::start[]

// started reports whether Start has run and Shutdown hasn't.
func (s *Service) started() bool {
	return s.isStarted && s.root.Err() == nil
}

// tag::finish[]

// finish records how a run stopped. A pause puts its requests in the
// review queue; a shutdown queues it again, for the next process to
// resume from its checkpoint; a run out of turns or money is
// unresolved, which is the group's state, not the service's budget.
func (s *Service) finish(ctx context.Context, id string,
	out Outcome, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
	if lerr := s.ledger.settle(id, out.Cost); lerr != nil {
		s.cfg.Log.Error("save spending", "err", lerr)
	}
	r := s.runs[id]
	next := *r
	next.Cost += out.Cost
	next.Turns += out.Turns
	next.Err = ""

	// end::finish[]

	// tag::finish-state[]

	pending, paused := errors.AsType[*agent.ApprovalPending](err)
	switch {
	case paused:
		next.State = Paused
		if aerr := s.review.add(id, pending); aerr != nil {
			s.cfg.Log.Error("add review items", "run", id, "err", aerr)
		}
	case err == nil:
		next.State, next.Outcome = Done, out.Status
		next.Issue = max(next.Issue, out.Issue)
	case errors.Is(context.Cause(ctx), agent.ErrShutdown):
		next.State = Queued
	case errors.Is(err, agent.ErrBudget):
		next.State, next.Outcome, next.Err = Failed, "unresolved",
			err.Error()
		next.Capped = errors.Is(err, agent.ErrBudgetExceeded)
	case errors.Is(err, agent.ErrWorkspaceChanged):
		s.workspaceChanged(&next, err)
	default:
		next.State, next.Err = Failed, err.Error()
	}

	// end::finish-state[]

	s.handOn(&next)
	if !next.active() { // what it waited for no longer matters
		s.review.close(id, "the run ended")
	}
	if serr := s.saveRun(&next); serr != nil {
		s.cfg.Log.Error("save run", "run", id, "err", serr)
	}
	*r = next
	s.wakeSettled() // a group waiting on this one's filing goes on now
	s.poke()
	s.dispatch()
}

// workspaceChanged restarts a run whose workspace changed since its
// checkpoint, once, under a new ID, with what a person said of its calls
// in flight; a restarted run that fails the same way is for a person.
// The caller holds s.mu and saves next.
func (s *Service) workspaceChanged(next *Run, err error) {
	next.State, next.Err = Failed, err.Error()
	if next.RestartOf != "" {
		next.Err = "needs a person: changed again after a restart: " +
			err.Error()
		return
	}
	note := ""
	if r, ok := errors.AsType[*Restart](err); ok {
		note = r.Note
	}
	if rerr := s.restart(next, note); rerr != nil {
		next.Err += "; restart: " + rerr.Error()
	}
}

// wakeSettled queues every paused run whose items are all settled,
// oldest first, as the sweeper does, so a run that waited on another
// group's filing resumes the moment that filing settles, even when it
// was the last run going. The caller holds s.mu.
func (s *Service) wakeSettled() {
	var paused []*Run
	for _, r := range s.runs {
		if r.State == Paused {
			paused = append(paused, r)
		}
	}
	slices.SortFunc(paused, func(a, b *Run) int {
		return a.Created.Compare(b.Created)
	})
	for _, r := range paused {
		s.requeue(r.ID)
	}
}

// tag::wake[]

// wake queues a paused run again once every item it waits on is
// decided or past its deadline.
func (s *Service) wake(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requeue(id)
	s.dispatch()
}

func (s *Service) requeue(id string) {
	r, ok := s.runs[id]
	if !ok || r.State != Paused || !s.review.settled(id) {
		return
	}
	next := *r
	next.State = Queued
	if err := s.saveRun(&next); err != nil {
		s.cfg.Log.Error("save run", "run", id, "err", err)
		return
	}
	*r = next
	s.queue = append(s.queue, id)
}

// end::wake[]

// sweep queues the paused runs whose deadlines have passed, oldest
// first, and starts runs a spent budget held back yesterday.
func (s *Service) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wakeSettled()
	s.prune()
	for _, r := range s.runs { // late reports a failed save left behind
		if next := *r; !r.active() && r.HandedOn == "" {
			s.handOn(&next)
			if next.HandedOn != "" && s.saveRun(&next) == nil {
				*r = next
			}
		}
	}
	s.dispatch()
	s.poke()
}

func (s *Service) sweeper() {
	t := time.NewTicker(s.cfg.Sweep)
	defer t.Stop()
	for {
		select {
		case <-s.root.Done():
			return
		case <-t.C:
			s.sweep()
		}
	}
}

// poke wakes the poster without waiting.
func (s *Service) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) poster() {
	for {
		select {
		case <-s.root.Done():
			return
		case <-s.kick:
			s.postLate()
		}
	}
}

// tag::late[]

// postLate adds each finished run's late reports to its issue: all the
// reports since the last comment in one comment, through Issues.Comment,
// which checks the text and asks a person as the agent's issue_comment
// does. The batch is fixed before it is proposed, so the person decides
// on the text that will be posted, and its key is made from the run and
// the batch's start, so a comment posted just before a crash isn't
// posted twice.
func (s *Service) postLate() {
	if s.cfg.Issues == nil {
		return
	}
	for _, j := range s.lateBatches() {
		ctx, cancel := context.WithTimeout(s.root, 30*time.Second)
		pseudo := "late-" + j.run // the review queue's name for it
		err := s.cfg.Issues.Comment(ctx, s.review.approver(pseudo),
			j.issue, j.run+"/late/"+strconv.Itoa(j.from), j.body)
		cancel()
		pending, paused := errors.AsType[*agent.ApprovalPending](err)
		switch {
		case paused:
			if aerr := s.review.add(pseudo, pending); aerr != nil {
				s.cfg.Log.Error("add review items", "run", pseudo,
					"err", aerr)
			}
		case errors.Is(err, ErrDeclined): // that batch's answer: move on
			s.declined(j.run, j.from, j.to)
		case err != nil:
			s.cfg.Log.Warn("post late reports", "run", j.run, "err", err)
		default:
			s.posted(j.run, j.to)
		}
	}
}

// end::late[]

// A lateBatch is one comment's worth of late reports for a run's issue.
type lateBatch struct {
	run      string
	issue    int
	from, to int // Late[from:to]
	body     string
}

// lateBatches fixes, for each finished run with late reports not yet
// posted, the batch its next comment covers, and returns them.
func (s *Service) lateBatches() []lateBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []lateBatch
	for _, id := range slices.Sorted(maps.Keys(s.runs)) {
		r := s.runs[id]
		if r.active() || r.Issue == 0 || r.Posted >= len(r.Late) {
			continue
		}
		if r.Batch <= r.Posted { // start a batch with what is there now
			next := *r
			next.Batch = len(r.Late)
			if err := s.saveRun(&next); err != nil {
				s.cfg.Log.Error("save run", "run", id, "err", err)
				continue
			}
			*r = next
		}
		out = append(out, lateBatch{run: id, issue: r.Issue,
			from: r.Posted, to: r.Batch,
			body: s.lateText(r.Late[r.Posted:r.Batch])})
	}
	return out
}

// declined records that the batch Late[from:to] won't be posted, and
// moves past it, so the next late reports get their own comment.
func (s *Service) declined(run string, from, to int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	next := *r
	next.Declined = append(slices.Clone(r.Declined), from)
	next.Posted = max(next.Posted, to)
	if err := s.saveRun(&next); err != nil {
		s.cfg.Log.Error("save run", "run", run, "err", err)
		return
	}
	*r = next
}

func (s *Service) posted(run string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	next := *r
	next.Posted = max(next.Posted, n)
	if err := s.saveRun(&next); err != nil {
		s.cfg.Log.Error("save run", "run", run, "err", err)
		return
	}
	*r = next
}

// lateText is the comment for reports that arrived after their
// group's run: how many, and for each when, which build, and the
// panic's first line, all redacted at ingest. Caller holds s.mu or
// not; it only reads report files.
func (s *Service) lateText(ids []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Seen %d more times:\n", len(ids))
	for _, id := range ids {
		var rep storedReport
		if err := readJSON(filepath.Join(s.cfg.Dir, reportsDir,
			id+".json"), &rep); err != nil {
			fmt.Fprintf(&b, "- report %s\n", id)
			continue
		}
		first, _, _ := strings.Cut(rep.Report.Panic, "\n")
		fmt.Fprintf(&b, "- report %s at %s, build %s (%s): %s\n", id,
			rep.Report.FirstSeen.UTC().Format(time.RFC3339),
			rep.Report.Version, rep.Report.Commit, first)
	}
	return b.String()
}

// reportsOf reads the filed reports with these IDs, in order.
func (s *Service) reportsOf(ids []string) ([]triage.Report, error) {
	out := make([]triage.Report, 0, len(ids))
	for _, id := range ids {
		var rep storedReport
		if err := readJSON(filepath.Join(s.cfg.Dir, reportsDir,
			id+".json"), &rep); err != nil {
			return nil, fmt.Errorf("reports of run: %w", err)
		}
		out = append(out, rep.Report)
	}
	return out, nil
}

// tag::restart[]

// restart starts a group again under a new run ID, from a fresh
// workspace, after its run found its workspace changed. The caller
// holds s.mu and saves old. The new run gets every report the old one
// had, late ones included, and is told it is a restart, so its
// Triager looks the group's issue up by fingerprint and comments on it
// instead of filing again. A restarted run that fails the same way is
// failed, for a person.
func (s *Service) restart(old *Run, note string) error {
	if id, ok := s.runOf(func(f *Run) bool { return f.RestartOf == old.ID }); ok {
		old.HandedOn = id // made before a crash kept the old run's save
		return nil
	}
	now := s.cfg.Now()
	r := &Run{ID: newRunID(old.Fingerprint, now),
		Fingerprint: old.Fingerprint, Commit: old.Commit,
		Reports: slices.Concat(old.Reports, old.Late), State: Queued,
		RestartOf: old.ID, Issue: old.Issue, Note: note, Created: now}
	if err := s.saveRun(r); err != nil {
		return err
	}
	s.runs[r.ID], s.groups[r.Fingerprint] = r, r.ID
	s.queue = append(s.queue, r.ID)
	old.Err += "; restarted as " + r.ID
	old.HandedOn = r.ID // its late reports went with it
	return nil
}

// runOf finds a run that matches, such as the follow-up a crash
// between two saves left behind. The caller holds s.mu.
func (s *Service) runOf(match func(*Run) bool) (string, bool) {
	for id, r := range s.runs {
		if match(r) {
			return id, true
		}
	}
	return "", false
}

// tag::prune[]

// prune deletes the files of runs that finished longer ago than Keep,
// the one retention rule: paused runs are kept until decided, however
// long that takes. The caller holds s.mu.
func (s *Service) prune() {
	if s.cfg.Prune == nil {
		return
	}
	for _, r := range s.runs {
		old := s.cfg.Now().Sub(r.Updated) >= s.cfg.Keep
		if r.active() || r.Pruned || !old {
			continue
		}
		next := *r
		next.Pruned = true
		if err := s.cfg.Prune(next); err != nil {
			s.cfg.Log.Warn("prune", "run", r.ID, "err", err)
			continue
		}
		if err := s.saveRun(&next); err != nil {
			s.cfg.Log.Error("save run", "run", r.ID, "err", err)
			continue
		}
		*r = next
	}
}

// end::prune[]

// tag::hand-on[]

// handOn gives a finished run's late reports a run of their own when
// the run left no issue to comment on: not_a_bug, an unresolved run, a
// failure. A verdict on the first reports may not hold for the next,
// so they are looked at, not dropped. The caller holds s.mu and saves
// r.
func (s *Service) handOn(r *Run) {
	if r.active() || r.Issue > 0 || len(r.Late) == 0 ||
		r.HandedOn != "" {
		return
	}
	if id, ok := s.runOf(func(f *Run) bool { return f.FollowUpOf == r.ID }); ok {
		r.HandedOn = id // made before a crash kept this run's save
		return
	}
	now := s.cfg.Now()
	f := &Run{ID: newRunID(r.Fingerprint, now),
		Fingerprint: r.Fingerprint, Commit: r.Commit, Reports: slices.Clone(r.Late), State: Queued,
		FollowUpOf: r.ID, Created: now}
	if err := s.saveRun(f); err != nil {
		s.cfg.Log.Error("save run", "run", f.ID, "err", err)
		return // the sweeper tries again
	}
	s.runs[f.ID], s.groups[f.Fingerprint] = f, f.ID
	s.queue = append(s.queue, f.ID)
	r.HandedOn = f.ID
}

// end::hand-on[]

// end::restart[]
