package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/causes"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
	"github.com/blackwell-systems/go-agents-book/code/ch17/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
	"github.com/blackwell-systems/go-agents-book/code/ch17/workspace"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// tag::triager[]

// A triager is the service's Triager: it runs one fingerprint group's
// agent as a run of its own, with its own workspace at the crash's
// commit, its own defenses (chapter 14), and its own checkpoint
// (chapter 15), resumed if an earlier process started it. Every group files into
// one shared tracker, through the cause record.
type triager struct {
	c      config
	getenv func(string) string
	out    io.Writer // progress, one line per call
	model  model
	pace   *agent.Limiter // one rate for every run's model calls
	slots  agent.Slots    // run_tests at once, across every run

	dir    string         // checkpoints, workspaces, audit logs
	issues *tracker.Store // the tracker every group files into
	causes *causes.Record
	build  func(ctx context.Context, run service.Run, ws string) error
	ask    agent.Approver   // asked before the review queue; may be nil
	script string           // the replay script's prefix, before a fingerprint
	review model            // the reviewer's model, if one reviews
	batch  *agent.SpendPool // a whole batch's budget, over every run
	notes  *batchNotes      // the eval's record of a batch; may be nil

	mu sync.Mutex // one run's progress lines at a time
}

// end::triager[]

// Triage runs or resumes run's agent until it answers, pauses, or
// stops.
func (t *triager) Triage(ctx context.Context, run service.Run,
	reports []triage.Report, ap agent.Approver,
	maxCost float64) (service.Outcome, error) {
	cp, err := durable.Open(t.dir, run.ID) // chapter 15's lock
	if err != nil {
		return service.Outcome{}, fmt.Errorf("open run %s: %w", run.ID,
			err)
	}
	defer cp.Close()
	work := filepath.Join(t.dir, run.ID+".work")
	env, err := t.setUp(ctx, run, reports, work)
	if err != nil {
		return service.Outcome{}, err
	}
	defer env.Close()
	r, err := t.prepare(ctx, run, env, work, ap, maxCost)
	if err != nil {
		return service.Outcome{}, err
	}
	defer r.close()
	st, err := t.restore(ctx, cp, run, env, r)
	if err != nil {
		return service.Outcome{}, err
	}
	r.lim.Checkpoint = cp
	before := st.Turns                // counted by an earlier process
	ctx = agent.WithName(ctx, run.ID) // for the audit log
	ctx = replay.WithScript(ctx, cmp.Or(t.script, "group-")+
		run.Fingerprint)
	events := agent.ResumeEvents(ctx, r.sm, r.tools, st, r.lim)
	events = savingSpend(events, filepath.Join(work, "spent"), r.pool, r.p)
	res, runErr := watch(events, r.p)
	if errors.Is(runErr, agent.ErrWorkspaceChanged) &&
		len(st.Attempts) > 0 {
		runErr = askAttempts(ctx, run.ID, st, r.lim.Approver, runErr)
	}
	out := service.Outcome{Cost: r.pool.Spent(),
		Turns: res.Turns - before}
	out.Status, out.Issue = statusOf(res.Final)
	return out, runErr
}

// A groupRun is what one run of a group is set up with.
type groupRun struct {
	p     *printer
	sm    agent.StreamModel
	tools []agent.Tool
	msgs  []agent.Message
	lim   agent.Limits
	pool  *agent.SpendPool
	d     *defense
	audit string
	rv    *reviewer // nil without a reviewer
}

func (r *groupRun) close() {
	if err := r.d.close(); err != nil {
		r.p.printf("close defenses: %v\n", err)
	}
}

// tag::prepare[]

// prepare builds the run's model stack, tools, defenses, and limits in
// chapter 12's and chapter 14's order: egress outermost, then the
// cause record, then the approval gate, then chapter 12's wrappers.
func (t *triager) prepare(ctx context.Context, run service.Run,
	env *triage.Env, work string, ap agent.Approver,
	maxCost float64) (*groupRun, error) {
	r := &groupRun{p: &printer{w: &lineWriter{w: t.out, mu: &t.mu,
		prefix: run.Fingerprint + " "}}}
	_, direct := providerKey(t.getenv, t.c.provider)
	wr := newWrapped(t.c, hostName(t.c, direct),
		modelName(t.getenv, t.c, direct), r.p, t.pace)
	r.sm = wr.Model(t.model)
	policy, err := contextPolicy(cmp.Or(t.c.context, "default"),
		agent.Generator(r.sm))
	if err != nil {
		return nil, err
	}
	marker, err := keptMarker(work) // the saved rule names it
	if err != nil {
		return nil, err
	}
	r.d, err = newDefense(ctx, t.getenv, defenseConfig{t.c.defenses,
		"later", t.c.minRung}, &taskrun.Job{Env: env}, &marker, r.p.w)
	if err != nil {
		return nil, err
	}

	// end::prepare[]

	if r.d.box.Rung > 0 {
		r.p.printf("defenses on; tests run at %s\n",
			rungName(r.d.box.Rung))
	} else {
		r.p.printf("defenses: %s\n", cmp.Or(t.c.defenses, "none"))
	}
	if t.getenv("AUDIT_KEY") != "" { // one log per run: one chain each
		r.audit = filepath.Join(t.dir, run.ID+".audit.jsonl")
		if err := r.d.openAudit(t.getenv, r.audit); err != nil {
			return nil, errors.Join(err, r.d.close())
		}
	}

	// tag::prepare-tools[]

	r.pool = &agent.SpendPool{Limit: maxCost, Parent: t.batch}
	r.lim = agent.Limits{MaxTurns: cmp.Or(t.c.maxTurns, 40),
		MaxCost: maxCost, Context: policy, Spend: r.pool,
		Approver: firstOf(t.ask, ap)}
	tools := triage.Tools(env)
	rule := r.d.rule
	if env.DiagnoseOnly { // chapter 17: the edit tool is absent
		tools = triage.DiagnoseTools(env)
		rule += triage.DiagnoseRule
	}
	tools = append(tools, triage.ExplainPackage(r.sm, env,
		agent.Limits{MaxTurns: 15, Spend: r.pool, Context: policy}))
	guard := t.causes.Guard(run.ID, env.Dir)
	wr.outer = guarded(r.d.outer, guard) // <1>
	r.tools = agent.WrapTools(wr.Tools(agent.WrapTools(tools,
		r.d.inner...)), agent.Grace(grace))
	if t.review != nil { // chapter 13's second opinion, at most twice
		rw := newWrapped(t.c, hostName(t.c, direct), t.c.reviewer, r.p,
			t.pace)
		r.rv = &reviewer{model: agent.Generator(rw.Model(t.review)),
			env: env, script: cmp.Or(t.script, "group-") +
				run.Fingerprint + "-review", pool: r.pool}
		r.tools = agent.WrapTools(r.tools, r.rv.wrap)
	}

	// end::prepare-tools[]

	r.msgs = []agent.Message{
		{Role: "system", Content: triage.Prompt + "\n\n" + explainLine +
			"\n\n" + triage.HistoryLine},
		{Role: "user",
			Content: instruction + rule + t.restartNote(run)},
	}
	if t.notes != nil {
		t.notes.add(promptHash(r.msgs[0].Content+rule, marker.Boundary(),
			tools), r.rv)
	}
	return r, nil
}

// batchNotes keeps what a batch's runs leave for the eval's record:
// the prompt hash of each run and each run's reviewer, since a batch
// record has the same fields as a single task's.
type batchNotes struct {
	mu        sync.Mutex
	hashes    map[string]bool
	reviewers []*reviewer
}

func (n *batchNotes) add(hash string, rv *reviewer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.hashes == nil {
		n.hashes = map[string]bool{}
	}
	n.hashes[hash] = true
	if rv != nil {
		n.reviewers = append(n.reviewers, rv)
	}
}

// results returns the runs' prompt hashes, sorted and joined (one,
// unless the runs' prompts differed), every review's verdict, and how
// many reviews failed.
func (n *batchNotes) results() (hash string, review []string,
	errs int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, rv := range n.reviewers {
		vs, e, _ := rv.Results()
		review, errs = append(review, vs...), errs+e
	}
	return strings.Join(slices.Sorted(maps.Keys(n.hashes)), ","),
		review, errs
}

// guarded puts the cause record's guard just inside the egress check,
// which is outer's first ware when defenses are on, so a cause another
// group filed is refused before anyone is asked to approve the call.
func guarded(outer []agent.ToolWare,
	guard agent.ToolWare) []agent.ToolWare {
	if len(outer) == 0 {
		return []agent.ToolWare{guard}
	}
	return slices.Concat(outer[:1], []agent.ToolWare{guard}, outer[1:])
}

// restore loads the run's checkpoint, counting the restart, or saves
// a new one with the workspace it checks on resume.
func (t *triager) restore(ctx context.Context, cp *durable.Checkpoint,
	run service.Run, env *triage.Env, r *groupRun) (agent.State, error) {
	st, err := cp.Restore(ctx, cmp.Or(t.c.maxResumes, 10))
	if errors.Is(err, durable.ErrNoCheckpoint) {
		st = agent.NewState(run.ID, r.msgs)
		ws, err := filepath.Abs(env.Dir) // a restart may run elsewhere
		if err != nil {
			return st, fmt.Errorf("open run %s: %w", run.ID, err)
		}
		st.Workspace = ws // chapter 17: checked on resume
		//nolint:wrapcheck // its errors say "save run"
		return st, cp.Save(ctx, st)
	}
	if err != nil {
		return st, fmt.Errorf("open run %s: %w", run.ID, err)
	}
	r.p.printf("resuming: %s, %d turns, $%.4f so far, restart %d\n",
		st.Phase, st.Turns, st.Usage.Cost, st.Resumes)
	saved := &savedRun{cp: cp, st: st, resumed: true}
	if err := r.d.recall(t.getenv, r.audit, saved); err != nil {
		return st, err
	}
	return st, nil
}

// tag::setup[]

// setUp makes the run's workspace, or finds the one an earlier process
// made: remind/ at the crash's commit and original/ beside it, which
// the gate runs reproducers against. A workspace that wasn't finished
// is made again.
func (t *triager) setUp(ctx context.Context, run service.Run,
	reports []triage.Report, work string) (*triage.Env, error) {
	ws := filepath.Join(work, "remind")
	orig := filepath.Join(work, "original")
	if !ready(work) {
		if err := os.RemoveAll(work); err != nil {
			return nil, fmt.Errorf("set up %s: %w", run.ID, err)
		}
		if err := os.MkdirAll(work, 0o700); err != nil {
			return nil, fmt.Errorf("set up %s: %w", run.ID, err)
		}
		if err := t.build(ctx, run, ws); err != nil {
			return nil, fmt.Errorf("set up %s: %w", run.ID, err)
		}
		if err := os.CopyFS(orig, os.DirFS(ws)); err != nil {
			return nil, fmt.Errorf("set up %s: %w", run.ID, err)
		}
		err := os.WriteFile(filepath.Join(work, "ready"), nil, 0o600)
		if err != nil {
			return nil, fmt.Errorf("set up %s: %w", run.ID, err)
		}
	}

	// end::setup[]

	// tag::setup-env[]

	env, err := triage.NewEnv(ws, orig, reports, t.issues)
	if err != nil {
		return nil, fmt.Errorf("set up %s: %w", run.ID, err)
	}
	env.Keys, env.Gate, env.History = true, true, true
	env.DiagnoseOnly = t.c.diagnoseOnly
	env.TestSlots = t.slots
	if err := env.Keep(filepath.Join(work, "tools.json")); err != nil {
		return nil, errors.Join(fmt.Errorf("set up %s: %w", run.ID, err),
			env.Close())
	}
	return env, nil
}

// end::setup-env[]

// practiceTree builds a workspace from the book's copy of remind, for
// runs on the catalog's crashes, whose commit names a build of it.
func practiceTree(repo string) func(context.Context, service.Run,
	string) error {
	return func(ctx context.Context, _ service.Run, ws string) error {
		return catalog.BuildRepo(ctx, catalog.Task{},
			filepath.Join(repo, "remind"), ws)
	}
}

// gitTree builds a workspace from a git repository at the crash's
// commit, for a service of your own.
func gitTree(repo string) func(context.Context, service.Run,
	string) error {
	return func(ctx context.Context, run service.Run, ws string) error {
		return workspace.At(ctx, repo, run.Commit, ws)
	}
}

// tag::restart-note[]

// restartNote tells a restarted run about the issue its group already
// has, which the new run's own records don't hold: the harness looks
// it up by fingerprint itself, so a second issue isn't left to the
// model's choice to search first.
func (t *triager) restartNote(run service.Run) string {
	if run.RestartOf == "" {
		return ""
	}
	note := "\n\nThis group was started before, and its workspace " +
		"was lost."
	if open := t.issues.Search(tracker.Query{
		Fingerprint: run.Fingerprint, Status: "open"}); len(open) > 0 {
		note += fmt.Sprintf(" It already has issue #%d: add what you "+
			"find to it with issue_comment, and don't file another.",
			open[0].ID)
	}
	if run.Note != "" {
		note += " " + run.Note
	}
	return note
}

// end::restart-note[]

// statusOf reads the group's status and issue from the run's answer:
// the diagnoses' statuses, joined if the group held more than one
// cause, and the first issue named.
func statusOf(final []byte) (string, int) {
	if final == nil {
		return "", 0
	}
	var answer struct {
		Groups []triage.Diagnosis `json:"groups"`
	}
	if err := json.Unmarshal(final, &answer); err != nil {
		return "", 0
	}
	var statuses []string
	issue := 0
	for _, d := range answer.Groups {
		if !slices.Contains(statuses, d.Status) {
			statuses = append(statuses, d.Status)
		}
		issue = cmp.Or(issue, d.Issue, d.Duplicate)
	}
	return strings.Join(statuses, "+"), issue
}

// firstOf asks a, then b when a has no decision: the terminal first in
// the command, then the review queue. Either may be nil.
func firstOf(a, b agent.Approver) agent.Approver {
	if a == nil {
		return b
	}
	return agent.ApproverFunc(func(ctx context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		d, err := a.Decide(ctx, r)
		if errors.Is(err, agent.ErrNoDecision) && b != nil {
			return b.Decide(ctx, r) //nolint:wrapcheck // as b said it
		}
		return d, err //nolint:wrapcheck // as a said it
	})
}

// A lineWriter writes whole lines, each with a prefix, under a lock
// shared by every run, so runs at once don't interleave mid-line.
type lineWriter struct {
	w      io.Writer
	mu     *sync.Mutex
	prefix string
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := slices.Index(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		l.mu.Lock()
		_, err := fmt.Fprintf(l.w, "%s%s\n", l.prefix, l.buf[:i])
		l.mu.Unlock()
		l.buf = l.buf[i+1:]
		if err != nil {
			return len(p), fmt.Errorf("print: %w", err)
		}
	}
}

// tag::ask-attempts[]

// askAttempts settles, with a person, the calls a run had in flight
// when its workspace changed, before the group restarts under a new ID
// whose checkpoint won't hold them: each one's outcome is unknown, and
// the new run must not repeat what took effect. Until every one is
// answered the run waits (*agent.ApprovalPending); then the answers
// go to the restarted run in a service.Restart.
func askAttempts(ctx context.Context, runID string, st agent.State,
	ap agent.Approver, cause error) error {
	var pending []agent.ApprovalRequest
	var notes []string
	for _, a := range st.Attempts {
		r := attemptRequest(runID, st, a)
		dec, err := agent.ApprovalDecision{}, agent.ErrNoDecision
		if ap != nil {
			dec, err = ap.Decide(ctx, r)
		}
		switch {
		case errors.Is(err, agent.ErrNoDecision),
			err == nil && dec.Hash != r.Hash:
			pending = append(pending, r)
		case err != nil:
			return fmt.Errorf("ask about %s: %w", a.Tool, err)
		case dec.Approved:
			notes = append(notes, fmt.Sprintf("%s (%s) did not take "+
				"effect, %s said.", a.Tool, a.CallID, dec.By))
		default:
			notes = append(notes, fmt.Sprintf("%s (%s) took effect, %s "+
				"said: %s.", a.Tool, a.CallID, dec.By, dec.Reason))
		}
	}
	if len(pending) > 0 {
		return &agent.ApprovalPending{Requests: pending}
	}
	return &service.Restart{Err: cause, Note: "Of the calls it had in " +
		"flight: " + strings.Join(notes, " ")}
}

// end::ask-attempts[]

// attemptRequest is the question about one call in flight.
func attemptRequest(runID string, st agent.State,
	a agent.Attempt) agent.ApprovalRequest {
	call := agent.ToolCall{ID: a.CallID, Name: a.Tool}
	for _, m := range st.Messages {
		for _, c := range m.ToolCalls {
			if c.ID == a.CallID {
				call = c
			}
		}
	}
	sum := sha256.Sum256([]byte(runID + "\x00" + a.CallID + "\x00" +
		a.Started.Format(time.RFC3339Nano)))
	return agent.ApprovalRequest{Kind: agent.KindUnknownOutcome,
		Run: runID, Call: call, Hash: hex.EncodeToString(sum[:]),
		Why: "outcome unknown: " + a.Tool + " started at " +
			a.Started.Format(time.RFC3339) + " and the workspace " +
			"changed before its result was saved; the group " +
			"restarts from a fresh workspace. Say done if it took " +
			"effect (what you found), rerun if it didn't",
		Evidence: "idempotency key " + cmp.Or(a.Key, "(none sent)")}
}

// prune deletes a finished run's workspace, checkpoint, and audit log,
// the service's retention rule; the lock file stays, since a run ID is
// never used twice (chapter 15).
func (t *triager) prune(r service.Run) error {
	var errs []error
	for _, p := range []string{r.ID + ".work", r.ID + ".json",
		r.ID + ".audit.jsonl"} {
		if err := os.RemoveAll(filepath.Join(t.dir, p)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// spentSoFar is what a run has spent, for the ledger to count when a
// killed process left the run's hold behind: the larger of what its
// checkpoint says and what its spend record says, which also counts
// reviews and sub-agent calls, plus its most expensive turn, for a call
// in flight at the kill. False means there is no record of either.
func (t *triager) spentSoFar(run string) (float64, bool) {
	saved, err := readSpend(filepath.Join(t.dir, run+".work", "spent"))
	ok := err == nil
	if cp, err := durable.Open(t.dir, run); err == nil {
		defer cp.Close()
		if st, err := cp.Load(); err == nil {
			saved.Spent, ok = max(saved.Spent, st.Usage.Cost), true
		}
	}
	return saved.Spent + saved.Turn, ok
}
