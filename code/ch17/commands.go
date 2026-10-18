package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/causes"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
	"github.com/blackwell-systems/go-agents-book/code/ch17/groups"
	"github.com/blackwell-systems/go-agents-book/code/ch17/review"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// models are a command's model clients: the agent's, and with -review,
// the reviewer's (nil without).
type models struct {
	main, review model
}

// commandModels builds the clients from the settings: the agent's
// from OPENROUTER_MODEL, the reviewer's from -reviewer, each recorded
// to -record or replayed as newClient does.
func commandModels(getenv func(string) string, w io.Writer,
	c config) (models, error) {
	var ms models
	var err error
	if ms.main, err = newClient(getenv, w, c); err != nil || !c.review {
		return ms, err
	}
	revenv := func(k string) string {
		if k == "OPENROUTER_MODEL" {
			return c.reviewer
		}
		return getenv(k)
	}
	ms.review, err = newClient(revenv, w, c)
	return ms, err
}

// A stack is what the command and the service share: the state
// directory's tracker, cause record, and Triager.
type stack struct {
	tr     *triager
	issues *tracker.Store
	late   *lateIssues
}

// newStack sets up the shared parts in state, filing into issues.
func newStack(getenv func(string) string, w io.Writer, c config,
	ms models, state string, issues *tracker.Store,
	build func(context.Context, service.Run, string) error) (*stack,
	error) {
	rec, err := causes.Open(filepath.Join(state, "causes"))
	if err != nil {
		return nil, err //nolint:wrapcheck // it says "causes:"
	}
	rec.ByKey = func(key string) (int, bool) {
		is, err := issues.ByKey(key)
		return is.ID, err == nil
	}
	rec.Open = func(_ context.Context, issue int) (bool, error) {
		is, err := issues.Get(issue)
		return err == nil && is.Status == "open", nil
	}
	late, err := newLateIssues(getenv, state, issues)
	if err != nil {
		return nil, err
	}
	tr := &triager{c: c, getenv: getenv, out: w, model: ms.main,
		review: ms.review,
		pace:   &agent.Limiter{Every: 200 * time.Millisecond},
		slots:  agent.NewSlots(2),
		dir:    filepath.Join(state, "checkpoints"),
		issues: issues, causes: rec, build: build}
	if err := os.MkdirAll(tr.dir, 0o700); err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	return &stack{tr: tr, issues: issues, late: late}, nil
}

// tag::run-file[]

// runFile is the command: it triages every report in file as the
// service would, in the state directory, with no HTTP in front: the
// same queue, one run per fingerprint group, each with its own workspace,
// defenses, and checkpoint, so starting it again resumes every group
// that didn't finish. At a terminal, approvals are asked there first
// (chapter 14); what nobody answers waits in the review queue.
func runFile(ctx context.Context, getenv func(string) string,
	w io.Writer, c config, ms models, file string) error {
	reports, err := triage.LoadReports(file)
	if err != nil {
		return err //nolint:wrapcheck // LoadReports names the file
	}
	state, done, err := stateDir(c)
	if err != nil {
		return err
	}
	defer done()
	issues, err := tracker.Open(filepath.Join(state, "issues.json"),
		taskrun.Now)
	if err != nil {
		return fmt.Errorf("tracker: %w", err)
	}
	s, err := newStack(getenv, w, c, ms, state, issues, treeFor(c))
	if err != nil {
		return err
	}
	s.tr.ask = commandApprover(c.approve, w)
	_, err = s.runAll(ctx, w, c, state, reports, nil)
	return err
}

// end::run-file[]

// runCatalog is runFile on a catalog task's reports, with its filed
// issues in the tracker and its grade at the end: every group's
// changes merged into one tree, as chapter 9 merged them.
func runCatalog(ctx context.Context, getenv func(string) string,
	w io.Writer, c config, ms models) error {
	_, err := catalogRun(ctx, getenv, w, c, ms,
		commandApprover(c.approve, w), "group-")
	return err
}

// A batchResult is what a command run produced: every group's run,
// what they cost, and with a catalog task, the merged tree's grade.
type batchResult struct {
	Runs   []service.Run
	Cost   float64
	Turns  int
	Report groups.Report

	PromptHash   string   // the runs' prompt hashes, for the eval
	Review       []string // every review's verdict, for the eval
	ReviewErrors int      // reviews that failed
}

// catalogRun is runCatalog with its approver and its runs' script
// prefix given, for the eval's batch (chapter 17).
func catalogRun(ctx context.Context, getenv func(string) string,
	w io.Writer, c config, ms models, ask agent.Approver,
	script string) (batchResult, error) {
	j, err := setUp(ctx, c.repo, c.task)
	if err != nil {
		return batchResult{}, err
	}
	defer j.Close()
	state, done, err := stateDir(c)
	if err != nil {
		return batchResult{}, err
	}
	defer done()
	if c.held {
		j.Held = taskrun.Held(j.Task, triage.Protected.Covers)
	}
	task := j.Task
	build := func(ctx context.Context, _ service.Run, ws string) error {
		return catalog.BuildRepo(ctx, task, filepath.Join(c.repo,
			"remind"), ws)
	}
	issues, err := catalogTracker(state, task)
	if err != nil {
		return batchResult{}, err
	}
	j.Env.Issues = issues // the grade counts what the groups filed
	s, err := newStack(getenv, w, c, ms, state, issues, build)
	if err != nil {
		return batchResult{}, err
	}
	s.tr.ask, s.tr.script, s.tr.notes = ask, script, &batchNotes{}
	res, err := s.runAll(ctx, w, c, state, j.Env.Reports, j)
	res.PromptHash, res.Review, res.ReviewErrors = s.tr.notes.results()
	return res, err
}

// catalogTracker opens the state directory's tracker, or makes it
// with the task's filed issues, so a command started again finds what
// its groups filed.
func catalogTracker(state string, task catalog.Task) (*tracker.Store,
	error) {
	file := filepath.Join(state, "issues.json")
	if _, err := os.Stat(file); err == nil {
		s, err := tracker.Open(file, taskrun.Now)
		if err != nil {
			return nil, fmt.Errorf("tracker: %w", err)
		}
		return s, nil
	}
	var filed []triage.Filed
	for _, k := range task.Known {
		filed = append(filed, triage.Filed{ID: k.ID, Title: k.Title,
			Body: k.Body})
	}
	s, err := triage.OpenTracker(state, filed, taskrun.Now)
	if err != nil {
		return nil, err //nolint:wrapcheck // it says "open tracker"
	}
	return s, nil
}

// runAll files the reports, runs every group, and prints the report;
// with a job, it merges the groups' changes and grades them.
func (s *stack) runAll(ctx context.Context, w io.Writer, c config,
	state string, reports []triage.Report, j *taskrun.Job) (batchResult,
	error) {
	// -max-cost is the whole batch's: one pool over every group's run,
	// and the ledger's total, which never starts over, so a restart,
	// even after midnight, can't spend it again.
	slots := cmp.Or(c.parallel, 2)
	s.tr.batch = &agent.SpendPool{Limit: c.maxCost}
	svc, err := service.New(context.WithoutCancel(ctx), service.Config{
		Dir: filepath.Join(state, "service"), Secret: randomSecret(),
		Triager: s.tr, Issues: s.late, Busy: s.tr.causes.Busy,
		Prune: s.tr.prune, Slots: slots,
		MaxQueued: len(reports) + 1, TotalCost: c.maxCost, SpentSoFar: s.tr.spentSoFar,
		RunCost:     c.maxCost / float64(slots),
		Fingerprint: fingerprinter(c), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		return batchResult{}, fmt.Errorf("start: %w", err)
	}
	s.tr.causes.Active = svc.Active
	if _, err := svc.Add(ctx, reports...); err != nil {
		return batchResult{}, errors.Join(err,
			svc.Shutdown(context.Background()))
	}
	svc.Start()
	waitErr := svc.Wait(ctx)
	stop, cancel := context.WithTimeout(context.Background(),
		grace+time.Second)
	defer cancel()
	if err := svc.Shutdown(stop); err != nil {
		return spentBy(svc), errors.Join(waitErr, err)
	}
	if waitErr != nil {
		fmt.Fprintf(w, "\nstopped: %v; start the command again with "+
			"-state %s to resume\n", context.Cause(ctx), state)
		return spentBy(svc), &stoppedError{waitErr}
	}
	return s.report(ctx, w, svc, j)
}

// spentBy is what a stopped batch's runs cost and how many turns they
// took, so a record of it says what was spent.
func spentBy(svc *service.Service) batchResult {
	var res batchResult
	for _, r := range svc.Runs() {
		res.Runs, res.Cost = append(res.Runs, r), res.Cost+r.Cost
		res.Turns += r.Turns
	}
	return res
}

// tag::report[]

// report prints one line per group in the report's own words (fixed,
// already fixed, duplicate, needs a person; unresolved; waiting for
// review; failed, with the error), then the diagnoses joined by root
// cause, as chapter 9's finish step joined them, and with a job, the
// grade of the merged tree.
func (s *stack) report(ctx context.Context, w io.Writer,
	svc *service.Service, j *taskrun.Job) (batchResult, error) {
	p := &printer{w: w}
	var res batchResult
	p.printf("\n%-10s %-24s %3s %6s %8s\n", "group", "status",
		"rep", "issue", "cost")
	var parts []groups.Part
	for _, r := range svc.Runs() {
		status := cmp.Or(r.Outcome, string(r.State))
		if r.State == service.Paused {
			status = "waiting for review"
		}
		p.printf("%-10s %-24s %3d %6s %8.4f\n", r.Fingerprint, status,
			len(r.Reports)+len(r.Late), issueRef(r.Issue), r.Cost)
		if r.Err != "" {
			p.printf("  %s\n", short(r.Err, 200))
		}
		res.Runs, res.Cost = append(res.Runs, r), res.Cost+r.Cost
		res.Turns += r.Turns
		part, err := s.part(r)
		if err != nil {
			return res, err
		}
		parts = append(parts, part)
	}
	p.printf("spent $%.4f in all\n", res.Cost)
	if j != nil {
		var err error
		if res.Report, err = groups.Merge(ctx, j, parts); err != nil {
			return res, err //nolint:wrapcheck // Merge says what failed
		}
		printGroups(p, res.Report, nil)
	}
	return res, p.err
}

// end::report[]

// part reads a run's answer from its checkpoint, for the join and the
// merge.
func (s *stack) part(r service.Run) (groups.Part, error) {
	work := filepath.Join(s.tr.dir, r.ID+".work")
	part := groups.Part{Fingerprint: r.Fingerprint,
		Dir:      filepath.Join(work, "remind"),
		Original: filepath.Join(work, "original")}
	cp, err := durable.Open(s.tr.dir, r.ID)
	if err != nil {
		return part, fmt.Errorf("read run %s: %w", r.ID, err)
	}
	defer cp.Close()
	st, err := cp.Load()
	switch {
	case errors.Is(err, durable.ErrNoCheckpoint):
		part.Err = errors.New("never started")
		return part, nil
	case err != nil:
		return part, fmt.Errorf("read run %s: %w", r.ID, err)
	}
	if st.Phase != agent.PhaseDone {
		part.Err = fmt.Errorf("stopped in phase %s", st.Phase)
		return part, nil
	}
	var answer struct {
		Groups []triage.Diagnosis `json:"groups"`
	}
	if err := json.Unmarshal(st.Final, &answer); err != nil {
		part.Err = fmt.Errorf("read the answer: %w", err)
		return part, nil
	}
	part.Diagnoses = answer.Groups
	return part, nil
}

func issueRef(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("#%d", n)
}

// stateDir returns -state, or a new temporary directory removed when
// done is called.
func stateDir(c config) (string, func(), error) {
	if c.state != "" {
		if err := os.MkdirAll(c.state, 0o700); err != nil {
			return "", nil, fmt.Errorf("state: %w", err)
		}
		return c.state, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "triage-state-")
	if err != nil {
		return "", nil, fmt.Errorf("state: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil // nothing to keep
}

// randomSecret is an ingest secret nobody needs: the command files its
// reports directly, not through POST /crashes.
func randomSecret() string { return rand.Text() + rand.Text() }

// commandApprover is who decides first in the command: chapter 14's
// scripted approvers for deny and files, as evals use them; with ask,
// chapter 14's terminal approver when standard input is a terminal;
// and otherwise nobody, so every decision waits in the review queue.
func commandApprover(approve string, w io.Writer) agent.Approver {
	switch approve {
	case "deny":
		return agent.ApproverFunc(denyAll)
	case "files":
		return agent.ApproverFunc(denyFiles)
	}
	fi, err := os.Stdin.Stat()
	if approve != "ask" || err != nil ||
		fi.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	a := &safety.LineApprover{In: os.Stdin, Out: w}
	var mu sync.Mutex // one question at a time, from every group
	return agent.ApproverFunc(func(ctx context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		mu.Lock()
		defer mu.Unlock()
		return a.Decide(ctx, r) //nolint:wrapcheck // as the person said
	})
}

// tag::serve[]

// serve runs the triage service until a signal: POST /crashes from
// remind's shipper, the review routes for triage review, and one run
// per fingerprint group in the state directory. It stops in chapter 15's
// order: the HTTP server first, so no report is half saved, then the
// runs, cancelled with ErrShutdown and kept by their checkpoints.
func serve(ctx context.Context, getenv func(string) string,
	w io.Writer, c config, ms models, addr string) error {
	if c.state == "" {
		return errors.New("serve needs -state, the directory it keeps " +
			"everything in")
	}
	if c.defenses != "all" { // strangers' reports reach every run
		return fmt.Errorf("serve runs only with -defenses all, not %q",
			c.defenses)
	}
	if c.daily <= 0 { // every report can start a run
		return errors.New("serve needs -daily, the most to spend in " +
			"a day, in dollars")
	}
	reviewers, err := parseReviewers(getenv("TRIAGE_REVIEWERS"))
	if err != nil {
		return err
	}
	issues, err := tracker.Open(cmp.Or(c.issues,
		filepath.Join(c.state, "issues.json")), time.Now)
	if err != nil {
		return fmt.Errorf("tracker: %w", err)
	}
	s, err := newStack(getenv, w, c, ms, c.state, issues, treeFor(c))
	if err != nil {
		return err
	}
	root := context.WithoutCancel(ctx) // <1>
	svc, err := service.New(root, service.Config{
		Dir:    filepath.Join(c.state, "service"),
		Secret: getenv("TRIAGE_INGEST_SECRET"), Reviewers: reviewers,
		Triager: s.tr, Issues: s.late, Busy: s.tr.causes.Busy,
		Prune: s.tr.prune, Slots: cmp.Or(c.parallel, 2),
		RunCost: c.maxCost, DailyCost: c.daily,
		SpentSoFar: s.tr.spentSoFar, Fingerprint: fingerprinter(c)})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// end::serve[]

	// tag::serve-run[]

	s.tr.causes.Active = svc.Active
	svc.Start()
	hs := &http.Server{Addr: addr, Handler: svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, WriteTimeout: time.Minute,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	fmt.Fprintf(w, "serving on %s, state in %s\n", addr, c.state)
	select {
	case err = <-errc:
	case <-ctx.Done(): // a signal: ErrShutdown is the cause
	}

	// end::serve-run[]

	// tag::serve-stop[]

	// Inside a platform's usual 10 s grace: requests get 2 s, then the
	// runs are cancelled and save (chapter 15's Grace is 5 s).
	httpStop, cancelHTTP := context.WithTimeout(context.Background(),
		2*time.Second)
	defer cancelHTTP()
	stop, cancel := context.WithTimeout(context.Background(),
		9*time.Second)
	defer cancel()
	serr := hs.Shutdown(httpStop)
	if errors.Is(serr, context.DeadlineExceeded) {
		// Shutdown waits 5 s for a connection that sent nothing yet;
		// past 2 s, it and any slow request are cut off, and the
		// client sends again.
		fmt.Fprintln(w, "closing connections still open after 2 s")
		serr = hs.Close()
	}
	if serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, serr)
	}
	if serr := svc.Shutdown(stop); serr != nil {
		err = errors.Join(err, serr)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// end::serve-stop[]

// parseReviewers reads the reviewers' tokens: one "name token" per
// line, as TRIAGE_REVIEWERS_FILE holds them.
func parseReviewers(text string) (map[string]string, error) {
	out := map[string]string{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, tok, ok := strings.Cut(line, " ")
		tok = strings.TrimSpace(tok)
		if !ok || tok == "" {
			return nil, errors.New("TRIAGE_REVIEWERS: each line is a " +
				"name and a token")
		}
		if _, dup := out[tok]; dup {
			return nil, fmt.Errorf("TRIAGE_REVIEWERS: %s's token is "+
				"also another reviewer's", name)
		}
		out[tok] = name
	}
	return out, nil
}

// tag::command[]

// command runs chapter 17's commands: serve, the service; review, its
// review queue; catalog, a catalog task's reports as one command run,
// graded; and a file of reports, triaged as one command run.
func command(ctx context.Context, getenv func(string) string, c config,
	addr string, args []string) error {
	w := os.Stdout
	if args[0] == "review" {
		//nolint:wrapcheck // its errors say what failed
		return review.Main(ctx, args[1:], getenv, w)
	}
	if len(args) != 1 {
		return errors.New("usage: triage [flags] FILE, catalog, " +
			"serve, or review")
	}
	ms, err := commandModels(getenv, w, c)
	if err != nil {
		return err
	}
	switch args[0] {
	case "serve":
		return serve(ctx, getenv, w, c, ms, addr)
	case "catalog":
		if c.git != "" || c.target != nil { // its tasks are remind's
			return errors.New("catalog runs remind's crashes; " +
				"-git and -target are for a file or serve")
		}
		return runCatalog(ctx, getenv, w, c, ms)
	}
	return runFile(ctx, getenv, w, c, ms, args[0])
}

// end::command[]
