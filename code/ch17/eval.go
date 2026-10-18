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
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/evals"
	"github.com/blackwell-systems/go-agents-book/code/ch17/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch17/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// evalConfig holds the eval runner's settings, from its flags.
type evalConfig struct {
	set      string   // the task set: tuning, confirm, floor, hard, all
	only     []string // if set, just these tasks of the set (chapter 17)
	versions []string // the agent versions to run, see version
	runs     int      // scored runs wanted per task and version
	parallel int      // runs at once
	budget   float64  // stop starting runs past this many dollars
	out      string   // the results file, JSON Lines, appended to
	replays  string   // a directory of saved calls, to record or replay
	pin      string   // the one OpenRouter provider to use, if set
	reviewer string   // the reviewer's model, for versions that review
	revPin   string   // the reviewer's one provider, if set
}

// tag::version[]

// A version is one configuration of the agent an eval compares.
type version struct {
	prompt   func(string) string // the system prompt, from triage.Prompt
	gate     bool                // check reproducers before filing
	review   bool                // a second model reviews the diagnosis
	defenses string              // chapter 14's: "", marked, or all
	history  bool                // the git-history tools (chapter 17)
}

// versions are the agent configurations chapter 13's evals compare.
var versions = map[string]version{
	"base":    {prompt: same},
	"checked": {prompt: same, gate: true, review: true},
	// Chapter 8's last prompt sentence, measured on tasks it wasn't
	// tuned on: the prompt without it.
	"no-crash-site": {prompt: withoutCrashSite},
	// Chapter 14's: the data marker alone, and every defense, with a
	// scripted approver that denies changes to protected files.
	"marked":   {prompt: same, defenses: "marked"},
	"defended": {prompt: same, defenses: "all"},
	// Chapter 17's finished agent: every defense, the gate and the
	// reviewer, and the history tools; and the same without the
	// history tools, for h1's line in the table.
	"final": {prompt: same, gate: true, review: true, defenses: "all",
		history: true},
	"final-no-history": {prompt: same, gate: true, review: true,
		defenses: "all"},
}

// end::version[]

func same(p string) string { return p }

// withoutCrashSite drops the prompt's last paragraph, chapter 8's
// crash-site sentence.
func withoutCrashSite(p string) string {
	before, _, ok := strings.CutLast(p, "\n\nThe function that crashed")
	if !ok {
		panic("the prompt has no crash-site paragraph")
	}
	return before
}

// runEval runs every task of a set under every version, runs times,
// then reruns the tasks whose unscored runs left them short, up to
// twice, and prints the report. Every run is appended to the results
// file as it ends, and the runs already in it count, so an eval that
// was stopped resumes. An error no run would get past, such as a
// refused key, stops the eval; the report still prints what ran.
// tag::run-eval[]
func runEval(ctx context.Context, getenv func(string) string,
	w io.Writer, c config, ec evalConfig) error {
	tasks, err := evals.Tasks(ec.set)
	if err == nil && len(ec.only) > 0 { // a pilot (chapter 17)
		tasks, err = onlyTasks(tasks, ec.only)
	}
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	r := &evalRunner{getenv: getenv, c: c, ec: ec,
		slots: agent.NewSlots(ec.parallel), marker: safety.NewMarker()}
	if err := r.preflight(ctx); err != nil { // <1>
		return err
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	r.stop = stop
	for round := range 3 {
		jobs := r.missing(tasks, round)
		if len(jobs) == 0 || ctx.Err() != nil || r.failed() != nil {
			break
		}
		r.runAll(ctx, jobs)
	}
	err = evals.Report(w, r.records, tasks, ec.runs,
		func() rand.Source { return rand.NewPCG(13, 13) })
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	return r.failed()
}

// end::run-eval[]

// missing returns the runs each task and version still needs, in
// task order with every version of a task together, so a budget stop
// leaves pairs that can be compared.
func (r *evalRunner) missing(tasks []string, round int) []job {
	need := map[string]map[string]int{} // version: task: runs
	for _, v := range r.ec.versions {
		r.mu.Lock()
		done := evals.Scored(r.records, func(rec evals.Record) bool {
			return rec.Version == v
		})
		r.mu.Unlock()
		need[v] = evals.Missing(done, tasks, r.ec.runs)
	}
	var jobs []job
	for _, task := range tasks {
		for i := range r.ec.runs {
			for _, v := range r.ec.versions {
				if i < need[v][task] {
					first := max(round*r.ec.runs, r.lastRun(v, task))
					jobs = append(jobs, job{v, task, first + i + 1})
				}
			}
		}
	}
	return jobs
}

// lastRun returns the highest run number recorded for a task and
// version, so a new run's replay script never takes an old one's name.
func (r *evalRunner) lastRun(version, task string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.records {
		if rec.Version == version && rec.Task == task {
			n = max(n, rec.Run)
		}
	}
	return n
}

type job struct {
	version string
	task    string
	run     int
}

// evalRunner holds what the runs of one eval share.
type evalRunner struct {
	getenv func(string) string
	c      config
	ec     evalConfig
	slots  agent.Slots // shared by every run's go test calls
	meta   evals.Record
	marker safety.Marker // one for the whole eval (chapter 14)

	stop context.CancelCauseFunc

	mu       sync.Mutex
	freed    *sync.Cond // signalled when a run in flight ends
	records  []evals.Record
	spent    float64
	inflight int   // runs started and not yet recorded
	err      error // why the eval stopped early, if it did
}

// runAll runs jobs, ec.parallel at once. A job starts only once it has
// a slot and the budget still covers it with every run in flight
// counted at its full max-cost, so the budget can't be overshot; it
// stops at the first job the budget can't cover with none in flight.
func (r *evalRunner) runAll(ctx context.Context, jobs []job) {
	sem := make(chan struct{}, max(r.ec.parallel, 1))
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		if ctx.Err() != nil || r.failed() != nil || !r.reserve() {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			r.add(r.once(ctx, j))
		})
	}
	wg.Wait()
}

// reserve counts one more run in flight once the budget covers it,
// waiting for runs in flight to end if their reservations are in the
// way. It reports false if the budget can't cover one more run even
// with none in flight.
func (r *evalRunner) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.ec.budget > 0 &&
		r.spent+float64(r.inflight+1)*r.c.maxCost > r.ec.budget {
		if r.inflight == 0 {
			return false
		}
		r.cond().Wait()
	}
	r.inflight++
	return true
}

// cond returns the condition runs in flight signal when they end; the
// caller holds r.mu.
func (r *evalRunner) cond() *sync.Cond {
	if r.freed == nil {
		r.freed = sync.NewCond(&r.mu)
	}
	return r.freed
}

func (r *evalRunner) failed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// add keeps a record, appends it to the results file, and stops the
// eval if the run hit an error every other run would hit too.
func (r *evalRunner) add(rec evals.Record, runErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inflight--
	r.cond().Broadcast()
	r.records = append(r.records, rec)
	r.spent += rec.Cost
	if err := appendRecord(r.ec.out, rec); err != nil && r.err == nil {
		r.err = err
		r.stop(evals.ErrStopped)
	}
	if evals.Fatal(runErr) && r.err == nil {
		r.err = fmt.Errorf("eval stopped: %w", runErr)
		r.stop(evals.ErrStopped)
	}
}

// tag::once[]

// once runs one task under one version and grades it. A failure to set
// up or grade the run is the harness's, so it becomes an unscored
// record, not an error; the run's own stop error comes back too, so
// the runner can tell an error that would stop every run.
func (r *evalRunner) once(ctx context.Context,
	j job) (evals.Record, error) {
	rec := r.meta
	rec.Set, rec.Version = r.ec.set, j.version
	rec.Task, rec.Run = j.task, j.run
	if versions[j.version].review {
		rec.Reviewer = r.ec.reviewer
	}
	o, err := r.agentRun(ctx, j, &rec)
	if err != nil {
		o = evals.Outcome{Err: err, GraderFault: true}
		rec.Err = err.Error()
	}
	rec.Verdict = evals.Judge(o, evals.UnscoredError).String()
	return rec, o.Err
}

// end::once[]

// agentRun sets up the task, runs the agent as chapter 12's program
// does, with the tracker built in, and grades the result.
func (r *evalRunner) agentRun(ctx context.Context, j job,
	rec *evals.Record) (evals.Outcome, error) {
	v := versions[j.version]
	if j.task == "batch" && v.history { // chapter 17: one run per group
		return r.batchRun(ctx, j, rec)
	}
	m, err := r.client(rec.Model, r.ec.pin)
	if err != nil {
		return evals.Outcome{}, err
	}
	wr := newWrapped(r.c, rec.Provider, rec.Model, &printer{w: io.Discard}, nil)
	sm := wr.Model(m)
	tj, err := setUp(ctx, r.c.repo, j.task)
	if err != nil {
		return evals.Outcome{}, err
	}
	defer tj.Close()
	tj.Env.TestSlots, tj.Env.Gate = r.slots, v.gate
	tj.Env.History = v.history // chapter 17
	if v.defenses == "all" {   // the scripted approver refuses these fixes
		tj.Held = taskrun.Held(tj.Task, triage.Protected.Covers)
	}
	start := time.Now()
	policy, err := contextPolicy("default", agent.Generator(sm))
	if err != nil {
		return evals.Outcome{}, err
	}
	pool := &agent.SpendPool{Limit: r.c.maxCost}
	lim := agent.Limits{MaxTurns: cmp.Or(r.c.maxTurns, 40),
		MaxCost: r.c.maxCost, Context: policy, Spend: pool}
	prompt := v.prompt(triage.Prompt) + "\n\n" + explainLine
	if v.history { // chapter 17
		prompt += "\n\n" + triage.HistoryLine
	}
	tools := append(triage.Tools(tj.Env), triage.ExplainPackage(sm,
		tj.Env, agent.Limits{MaxTurns: 15, Spend: pool, Context: policy}))
	d, err := newDefense(ctx, r.getenv, defenseConfig{v.defenses, "files",
		r.c.minRung}, tj, &r.marker, io.Discard) // chapter 14
	if err != nil {
		return evals.Outcome{}, err
	}
	defer d.close()
	lim.Approver, wr.outer, rec.Rung = d.approver, d.outer, d.box.Rung
	rec.PromptHash = promptHash(prompt+d.rule, r.marker.Boundary(),
		tools) // the rule too
	tools = wr.Tools(agent.WrapTools(tools, d.inner...))
	script := fmt.Sprintf("%s-%s-%s-%02d", r.ec.set, j.version, j.task, j.run)
	var rv *reviewer
	if v.review {
		rm, err := r.client(r.ec.reviewer, r.ec.revPin)
		if err != nil {
			return evals.Outcome{}, err
		}
		rw := newWrapped(r.c, rec.Provider, r.ec.reviewer, // its own retries
			&printer{w: io.Discard}, nil)
		rv = &reviewer{model: agent.Generator(rw.Model(rm)), env: tj.Env,
			script: script + "-review", pool: pool}
		tools = agent.WrapTools(tools, rv.wrap)
	}
	res, runErr := agent.RunStream(replay.WithScript(ctx, script), sm,
		tools, []agent.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: instruction + d.rule},
		}, lim)
	rec.Attempted, rec.Succeeded = judge(j.task, res.Messages, tj.Env,
		res.Final) // chapter 14
	rec.Judged = evals.Injection(j.task) // chapter 17
	rec.Seconds = time.Since(start).Round(time.Millisecond).Seconds()
	rec.Cost = max(res.Usage.Cost, pool.Spent())
	for _, msg := range res.Messages {
		if msg.Role == "assistant" {
			rec.Turns++
		}
	}
	for _, ev := range tj.Env.GateEvents() {
		rec.Gate = append(rec.Gate, ev.Kind+" "+ev.Fingerprint+" "+ev.Reason)
	}
	if rv != nil {
		rec.Review, rec.ReviewErrors, _ = rv.Results()
	}
	if runErr != nil {
		rec.Err = runErr.Error()
	}
	// tag::grade[]
	out, err := tj.Grade(ctx, res.Final) // <1>
	if err != nil {
		return evals.Outcome{}, fmt.Errorf("grade %s: %w", j.task, err)
	}
	fault := false
	for _, c := range out.Grade.Failed() {
		rec.Failed = append(rec.Failed, c.Name+" "+c.Group)
		fault = fault || graderFault(c.Detail) // <2>
	}
	return evals.Outcome{Err: runErr, Graded: res.Final != nil,
		Passed: out.Grade.Passed(), GraderFault: fault}, nil
}

// graderFault reports whether a failed check failed for the grader's
// own reason: go test couldn't build anything because the build cache
// lost files, as it did under load in chapter 8.
func graderFault(detail string) bool {
	return strings.Contains(detail, "could not import") ||
		strings.Contains(detail, "go-build") &&
			strings.Contains(detail, "no such file")
}

// end::grade[]

// preflight checks the settings before any run: a client for each
// model the eval uses, and, with a key, one tiny request to each, so a
// bad model name, a provider pin with no endpoint, or a refused key
// stops the eval at once instead of failing every run. It also fills
// in the metadata every record shares, and reads the runs already in
// the results file.
func (r *evalRunner) preflight(ctx context.Context) error {
	r.meta = meta(ctx, r.getenv, r.c, r.ec)
	var err error
	r.records, err = readRecords(r.ec.out, r.ec.set, r.ec.versions)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	// -budget is the model's total over the results file, so a rerun
	// after a crash or a stop doesn't get it again (chapter 17).
	if r.spent, err = priorSpend(r.ec.out, r.meta.Model); err != nil {
		return fmt.Errorf("eval: %w", err)
	}
	if b := r.ec.budget; b > 0 && b < r.c.maxCost {
		return fmt.Errorf("eval: -budget $%.2f is less than one run's "+
			"-max-cost $%.2f, so no run could start", b, r.c.maxCost)
	}
	if b := r.ec.budget; b > 0 && r.spent >= b {
		return fmt.Errorf("eval: %s has already spent $%.4f of its "+
			"-budget $%.2f in %s", r.meta.Model, r.spent, b, r.ec.out)
	}
	checks := map[string]string{r.meta.Model: r.ec.pin}
	for _, v := range r.ec.versions {
		if versions[v].review {
			checks[r.ec.reviewer] = r.ec.revPin
		}
	}
	for name, pin := range checks {
		m, err := r.client(name, pin)
		if err != nil {
			return err
		}
		if r.getenv("OPENROUTER_API_KEY") == "" {
			continue // a replay; the runs say whether it diverged
		}
		_, err = m.Generate(replay.WithScript(ctx,
			"preflight-"+strings.ReplaceAll(name, "/", "_")),
			agent.Request{MaxTokens: 16, Messages: []agent.Message{
				{Role: "user", Content: "Reply with the word ready."}}})
		if err != nil {
			return fmt.Errorf("eval: %s doesn't answer: %w", name, err)
		}
	}
	return nil
}

// client returns a model client: live with a key, recording each call
// to ec.replays if set, and replaying from it without a key.
func (r *evalRunner) client(name, pin string) (*openrouter.Client, error) {
	key := r.getenv("OPENROUTER_API_KEY")
	hc := &http.Client{}
	switch {
	case key == "" && r.ec.replays == "":
		return nil, errors.New("eval: no OPENROUTER_API_KEY, and no " +
			"-replays directory to replay an eval from")
	case key == "":
		hc.Transport = &replay.Transport{Files: os.DirFS(r.ec.replays)}
	case r.ec.replays != "":
		hc.Transport = &replay.Recorder{Dir: r.ec.replays}
	}
	c := &openrouter.Client{APIKey: key, Model: name, HTTP: hc}
	if pin != "" {
		c.Providers = []string{pin}
	}
	return c, nil
}

// meta returns the fields every record of this eval shares.
func meta(ctx context.Context, getenv func(string) string, c config,
	ec evalConfig) evals.Record {
	git := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "git",
			append([]string{"-C", c.repo}, args...)...).Output()
		if err != nil {
			return "unknown"
		}
		return strings.TrimSpace(string(out))
	}
	provider := "openrouter"
	if ec.pin != "" {
		provider += "/" + ec.pin
	}
	return evals.Record{
		Model:     cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel),
		Provider:  provider,
		MaxTurns:  cmp.Or(c.maxTurns, 40),
		MaxCost:   c.maxCost,
		Date:      time.Now().UTC().Format(time.DateOnly),
		GoVersion: runtime.Version(),
		Commit:    git("rev-parse", "--short", "HEAD"),
		Dirty:     git("status", "--porcelain") != "",
	}
}

// promptHash identifies what the model is told: the system prompt and
// every tool's name, description, and parameters. The prompt includes
// the defenses' rule, which names the data marker's boundary, random
// for each invocation (chapter 14), so the boundary is hashed as a
// placeholder: the same prompt gives the same hash on every run.
func promptHash(prompt, boundary string, tools []agent.Tool) string {
	if boundary != "" {
		prompt = strings.ReplaceAll(prompt, boundary, "BOUNDARY")
	}
	var b strings.Builder
	b.WriteString(prompt)
	for _, t := range tools {
		fmt.Fprintf(&b, "\n%s\n%s\n%s", t.Def.Name, t.Def.Description,
			t.Def.Parameters)
	}
	return hash(b.String())
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

// readRecords returns the records in a results file for a set and its
// versions; a file that doesn't exist yet has none.
func readRecords(file, set string, versions []string) ([]evals.Record, error) {
	if file == "" {
		return nil, nil
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read records: %w", err)
	}
	var out []evals.Record
	for line := range strings.Lines(string(data)) {
		var rec evals.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("read records: %s: %w", file, err)
		}
		if rec.Set == set && slices.Contains(versions, rec.Version) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// priorSpend sums what the records in file already spent on model,
// every set and version, since they all came out of one budget.
func priorSpend(file, model string) (float64, error) {
	if file == "" {
		return 0, nil
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read records: %w", err)
	}
	spent := 0.0
	for line := range strings.Lines(string(data)) {
		var rec evals.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return 0, fmt.Errorf("read records: %s: %w", file, err)
		}
		if rec.Model == model {
			spent += rec.Cost
		}
	}
	return spent, nil
}

// appendRecord adds a record to a JSON Lines file, if one is named.
func appendRecord(file string, rec evals.Record) error {
	if file == "" {
		return nil
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("save record: %w", err)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("save record: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("save record: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save record: %w", err)
	}
	return nil
}

// onlyTasks keeps the tasks of a set that only names, refusing a name
// the set doesn't have, so a pilot can't run a task by a typo's luck.
func onlyTasks(set, only []string) ([]string, error) {
	for _, t := range only {
		if !slices.Contains(set, t) {
			return nil, fmt.Errorf("-only: %s is not in the set", t)
		}
	}
	return only, nil
}

// tag::batch-run[]

// batchRun runs the batch as chapter 17's command does, one run per
// fingerprint group with its own workspace, defenses, and checkpoint, under
// the eval's scripted approver, and grades the merged tree.
func (r *evalRunner) batchRun(ctx context.Context, j job,
	rec *evals.Record) (evals.Outcome, error) {
	m, err := r.client(rec.Model, r.ec.pin)
	if err != nil {
		return evals.Outcome{}, err
	}
	rm, err := r.client(r.ec.reviewer, r.ec.revPin) // final reviews
	if err != nil {
		return evals.Outcome{}, err
	}
	c := r.c
	c.task, c.state, c.defenses = "batch", "", "all"
	c.review, c.reviewer = true, r.ec.reviewer
	c.held = true // denyFiles refuses every protected fix
	script := fmt.Sprintf("%s-%s-%s-%02d-", r.ec.set, j.version, j.task,
		j.run)
	start := time.Now()
	res, runErr := catalogRun(ctx, r.getenv, io.Discard, c,
		models{main: m, review: rm},
		agent.ApproverFunc(denyFiles), script)
	rec.Seconds = time.Since(start).Round(time.Millisecond).Seconds()
	rec.Cost, rec.Turns = res.Cost, res.Turns
	rec.PromptHash, rec.Review = res.PromptHash, res.Review
	rec.ReviewErrors = res.ReviewErrors
	if runErr != nil {
		rec.Err = runErr.Error()
		// The run's error decides the verdict (unscored or failed).
		return evals.Outcome{Err: runErr}, nil //nolint:nilerr // in Outcome
	}
	if n := cappedRuns(res.Runs); n > 0 { // the cap's, not the agent's
		err := fmt.Errorf("%w: the batch's -max-cost stopped %d of %d "+
			"groups", evals.ErrStopped, n, len(res.Runs))
		rec.Err = err.Error()
		return evals.Outcome{Err: err}, nil //nolint:nilerr // in Outcome
	}
	g := res.Report.Outcome.Grade
	fault := false
	for _, ch := range g.Failed() {
		rec.Failed = append(rec.Failed, ch.Name+" "+ch.Group)
		fault = fault || graderFault(ch.Detail)
	}
	return evals.Outcome{Graded: true, Passed: g.Passed(),
		GraderFault: fault}, nil
}

// end::batch-run[]

// cappedRuns counts a batch's groups that its budget kept from
// starting or stopped partway: a batch graded without them would count
// the cap as the agent's failure.
func cappedRuns(runs []service.Run) int {
	n := 0
	for _, r := range runs {
		if r.State == service.Queued || r.Capped {
			n++
		}
	}
	return n
}
