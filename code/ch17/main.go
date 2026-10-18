// Command ch17 runs the crash-triage agent on a task from the crash
// catalog, or on every crash in it at once (-task batch), as in chapter
// 8, with tool calls run in parallel and a sub-agent, explain_package,
// for questions that would fill the context. With -groups the harness
// starts one agent per fingerprint group instead, several at once, each in
// its own copy of remind, and merges, files, and grades what they did.
// With OPENROUTER_API_KEY set, or OPENROUTER_API_KEY_FILE naming a
// file that holds the key, it calls the live API; without it, it
// replays a saved run, while the tools, including go test, still run
// for real.
package main

import (
	"cmp"
	"context"
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
	"github.com/blackwell-systems/go-agents-book/code/ch17/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch17/orsdk"
	"github.com/blackwell-systems/go-agents-book/code/ch17/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch17/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch17/trackermcp"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

//go:embed testdata/replies
var embedded embed.FS

// explainLine is the line the system prompt gains with explain_package.
const explainLine = "For a question about how a package works that " +
	"would take many file reads, such as how schedule computes the next " +
	"run, ask explain_package: its answer is short, and your context " +
	"stays small."

const (
	defaultModel = "anthropic/claude-haiku-5.5"
	instruction  = "Triage the crash reports: group them by cause, " +
		"then investigate, fix, and file each cause that isn't filed yet."
)

func main() {
	var c config
	flag.StringVar(&c.task, "task", "m1-done-after-delete",
		"the catalog task to run (see catalog/testdata/tasks), or batch "+
			"for every crash in the catalog at once")
	flag.StringVar(&c.context, "context", "default",
		"the context policy: none, cache, clear, trim, or compact (default)")
	flag.StringVar(&c.repo, "repo", ".",
		"the directory holding remind and catalog (the book's code directory)")
	flag.StringVar(&c.record, "record", "",
		"save each call to this directory, to replay later")
	flag.StringVar(&c.save, "save", "",
		"write the conversation to this file as JSON")
	flag.IntVar(&c.maxTurns, "max-turns", 40,
		"stop the run after this many turns")
	flag.Float64Var(&c.maxCost, "max-cost", 0.25,
		"stop the run once it has spent this many dollars; 0 for no limit")
	flag.BoolVar(&c.groups, "groups", false,
		"start one agent per fingerprint group, and merge what they did")
	flag.IntVar(&c.parallel, "parallel", 6,
		"with -groups, how many agents run at once")
	flag.BoolVar(&c.noSub, "no-subagent", false,
		"leave out explain_package, the sub-agent tool")
	flag.StringVar(&c.tracker, "tracker", "mcp",
		"how the agent reaches the tracker: mcp, through its MCP server, "+
			"or builtin, with chapter 7's tools")
	flag.StringVar(&c.provider, "provider", "openrouter",
		"the model API: openrouter (chapter 2's client), sdk "+
			"(OpenRouter's Go SDK), or anthropic (the Messages API)")
	flag.StringVar(&c.prices, "prices", "",
		"a JSON price table for -provider anthropic; the built-in one "+
			"if empty")
	flag.StringVar(&c.pins, "tracker-pins", "",
		"a file of the tracker tools' digests, from -print-digests; a tool "+
			"not as pinned stops the run")
	faults := flag.String("faults", "", "fail requests on purpose, one "+
		"fault per request: 429[:seconds], 503, drop, lost, reset:bytes, "+
		"truncate:bytes, ok (chapter 12)")
	flag.BoolVar(&c.trace, "trace", false, "print the run as a span tree")
	flag.BoolVar(&c.meter, "meter", false,
		"print the cost by agent and by call")
	flag.BoolVar(&c.log, "log", false, "write logs to standard error")
	flag.StringVar(&c.defenses, "defenses", "all", "chapter 14's "+
		"defenses: none, marked (the data marker alone), or all")
	flag.StringVar(&c.approve, "approve", "ask", "with -defenses all, who "+
		"decides calls that need a person: ask (this terminal), deny, "+
		"files (deny file changes, allow the rest), later (pause the "+
		"run), or off")
	flag.IntVar(&c.minRung, "sandbox", 3, "with -defenses all, the "+
		"lowest sandbox rung to accept: 3 (the operating system's "+
		"sandbox) or 2 (the hardened runner alone)")
	flag.StringVar(&c.audit, "audit", "", "with -defenses all, append "+
		"the approval audit log to this file, keyed with $AUDIT_KEY or "+
		"the file $AUDIT_KEY_FILE names")
	flag.StringVar(&c.state, "state", os.Getenv("TRIAGE_STATE"), "keep "+
		"the run's checkpoint, workspace, and tracker in this directory, "+
		"so a run that was stopped resumes when started again (chapter "+
		"15); $TRIAGE_STATE if not set")
	flag.StringVar(&c.runID, "run", "", "with -state, the run to "+
		"resume or start; empty resumes the task's newest unfinished "+
		"run, or starts a new one")
	flag.BoolVar(&c.keyless, "keyless", false, "with -state, send the "+
		"tracker no idempotency keys, as for a tracker without them: "+
		"unknown outcomes go to a person (uses the built-in tracker)")
	flag.IntVar(&c.maxResumes, "max-resumes", 10, "with -state, how "+
		"many times a run may be restarted")
	flag.DurationVar(&c.keep, "keep", 7*24*time.Hour, "with -state, "+
		"delete finished runs older than this at start")
	flag.StringVar(&c.dieAfter, "die-after", "", "with -state, for the "+
		"chapter's demonstration: kill this process with SIGKILL right "+
		"after the named built-in tool's first call returns, before its "+
		"answer is saved; not a tool served over MCP, such as the "+
		"tracker's with -tracker mcp (-keyless uses the built-in ones)")
	digests := flag.Bool("print-digests", false,
		"print the digests of the tracker server's tools, as JSON, and exit")
	serve := flag.String("serve-tracker", "",
		"serve the issues in this file over MCP on standard input and "+
			"output, as the program does for itself with -tracker mcp")
	var ec evalConfig
	flag.StringVar(&ec.set, "eval", "", "run an eval on a task set "+
		"instead of one task: tuning, confirm, floor, hard, injection, "+
		"or all (chapters 13 and 14)")
	versionList := flag.String("versions", "base",
		"with -eval, the agent versions to run: base, checked, "+
			"no-crash-site, marked, defended, final, or "+
			"final-no-history, comma-separated")
	flag.IntVar(&ec.runs, "runs", 2, "with -eval, scored runs per task")
	only := flag.String("only", "", "with -eval, just these tasks of "+
		"the set, comma-separated, as for a pilot")
	flag.IntVar(&ec.parallel, "eval-parallel", 4,
		"with -eval, runs at once")
	flag.Float64Var(&ec.budget, "budget", 4, "with -eval, stop "+
		"starting runs past this many dollars, counting what the "+
		"model's records in -out already spent")
	flag.StringVar(&ec.out, "out", "", "with -eval, append every run's "+
		"record to this JSON Lines file; its records for the same set "+
		"and versions count toward the quota, so a stopped eval resumes")
	flag.StringVar(&ec.replays, "replays", "", "with -eval, record calls "+
		"to this directory with a key, or replay them from it without one")
	flag.StringVar(&ec.pin, "pin", "", "with -eval, the one OpenRouter "+
		"provider to send requests to, such as claude-on-aws")
	flag.StringVar(&ec.reviewer, "reviewer", "deepseek/deepseek-v4.1-flash",
		"with -eval, the reviewer's model for the checked version")
	flag.StringVar(&ec.revPin, "reviewer-pin", "", "with -eval, the one "+
		"OpenRouter provider for the reviewer's requests")
	flag.StringVar(&c.issues, "issues", "", "with serve, the tracker's "+
		"file (default: issues.json in -state)")
	flag.StringVar(&c.git, "git", "", "with a file or serve, a git "+
		"repository to check out each crash's commit from (default: "+
		"the book's remind)")
	targetFile := flag.String("target", "", "with a file or serve, a "+
		"JSON file naming the service the reports come from: its name, "+
		"its module path, and its protected paths (default: remind)")
	flag.Float64Var(&c.daily, "daily", 0, "with serve, the most to "+
		"spend in a day, in dollars; 0 means no daily limit")
	flag.BoolVar(&c.diagnoseOnly, "diagnose-only", false, "with a file, "+
		"catalog, or serve: leave out code_patch, so runs write "+
		"reproducers and issues but no fixes")
	flag.BoolVar(&c.review, "review", false, "with a file, catalog, or "+
		"serve: a second model (-reviewer) reviews each group's "+
		"diagnosis, at most twice (chapter 13)")
	addr := flag.String("addr", "localhost:8417", "with serve, the "+
		"address to listen on")
	flag.Parse()
	getenv, err := takeSecrets() // chapter 14: before any child starts
	if err != nil {
		log.Fatal(err)
	}
	if *targetFile != "" {
		if flag.NArg() == 0 { // a catalog task is remind's
			log.Fatal("-target is for a file of reports or serve")
		}
		t, err := triage.LoadTarget(*targetFile)
		if err != nil {
			log.Fatal(err)
		}
		c.target = &t
	}
	ec.versions = strings.Split(*versionList, ",")
	c.reviewer = ec.reviewer
	if *only != "" {
		ec.only = strings.Split(*only, ",")
	}
	for _, v := range ec.versions {
		if _, ok := versions[v]; !ok {
			log.Fatalf("no version %q; try base, checked, no-crash-site, "+
				"marked, defended, final, or final-no-history", v)
		}
	}
	ctx, stop := withShutdown(context.Background()) // chapter 15
	defer stop()
	if c.faults, err = parseFaults(*faults); err != nil {
		log.Fatal(err)
	}
	if *digests {
		if err := printDigests(ctx, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *serve != "" {
		err := trackermcp.Serve(ctx, *serve, taskrun.Now, os.Stdin,
			os.Stdout, os.Stderr)
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if args := flag.Args(); len(args) > 0 { // chapter 17
		if err := command(ctx, getenv, c, *addr, args); err != nil {
			if _, ok := errors.AsType[*stoppedError](err); ok {
				os.Exit(1)
			}
			log.Fatal(err)
		}
		return
	}
	if ec.set != "" {
		c.tracker = "builtin"
		if err := runEval(ctx, getenv, os.Stdout, c, ec); err != nil {
			log.Fatal(err)
		}
		return
	}
	err = run(ctx, getenv, os.Stdout, c)
	if _, ok := errors.AsType[*stoppedError](err); ok {
		os.Exit(1) // run has printed why
	}
	if err != nil {
		log.Fatal(err)
	}
}

// config holds the program's settings, from its flags.
type config struct {
	held     bool          // grade protected fixes as needs_human (eval)
	task     string        // the catalog task, and the saved run's name
	context  string        // the context policy's name
	repo     string        // the directory holding remind and catalog
	record   string        // a directory to record calls to
	save     string        // a file to save the conversation to
	maxTurns int           // the run's turn limit
	maxCost  float64       // the run's cost budget, in dollars
	groups   bool          // one agent per fingerprint group (chapter 9)
	parallel int           // with groups, agents at once
	noSub    bool          // leave out explain_package
	tracker  string        // "mcp" or "builtin" (chapter 10)
	pins     string        // a file of tracker tool digests, if set
	provider string        // "openrouter", "sdk", or "anthropic" (chapter 11)
	prices   string        // a price table for -provider anthropic, if set
	faults   []agent.Fault // faults to inject, one per request (chapter 12)
	trace    bool          // print the run as a span tree
	meter    bool          // print the cost by agent and call
	log      bool          // write structured logs to standard error
	backoff  agent.Backoff // the retries' waits; tests make them zero
	defenses string        // none (or empty), marked, or all (chapter 14)
	approve  string        // who decides, with defenses all
	audit    string        // the audit log's file, if set
	minRung  int           // the lowest sandbox rung defenses accept

	// Chapter 15: a run kept in a state directory, resumed when the
	// program starts again.
	state      string // the state directory, or "" for none
	runID      string // the run's ID; the task's if empty
	keyless    bool   // no idempotency keys for the tracker
	maxResumes int    // how many restarts a run may have

	// Chapter 17: the command over a file of reports, and the service.
	issues       string         // the tracker's file for serve, if not in -state
	git          string         // a git repository to build workspaces from
	target       *triage.Target // the service triaged; nil for remind
	daily        float64        // serve's daily budget, in dollars
	diagnoseOnly bool           // no tool that changes code
	review       bool           // a second model reviews each answer
	reviewer     string         // its model
	keep         time.Duration  // how long finished runs are kept
	dieAfter     string         // a tool after whose first call to die
}

// tag::run[]

// run sets up the task, runs the agent on it, and prints what it did,
// its answer, the issues it filed, and the catalog's grade.
func run(ctx context.Context, getenv func(string) string, w io.Writer,
	c config) error {
	model, err := newClient(getenv, w, c)
	if err != nil {
		return err
	}
	if c.groups {
		if c.state != "" { // chapter 17 makes fan-out durable
			return errors.New("-state doesn't work with -groups yet")
		}
		return runGroups(ctx, getenv, w, model, c)
	}
	_, direct := providerKey(getenv, c.provider)
	p := &printer{w: w}
	wr := newWrapped(c, hostName(c, direct), modelName(getenv, c, direct),
		p, nil)
	sm := wr.Model(model) // chapter 12
	start := time.Now()
	if c.state != "" && c.keyless {
		c.tracker = "builtin" // the MCP tracker always honors keys
	}
	var saved *savedRun
	if c.state != "" { // chapter 15
		if saved, err = pickRun(&c, p); err != nil {
			return err
		}
		defer saved.cp.Close()
	}
	j, err := setUpRun(ctx, c, saved)
	if err != nil {
		return err
	}
	defer j.Close()
	j.Env.Keys = c.state != "" && !c.keyless // chapter 15
	if saved != nil {
		if err := j.Env.Keep(filepath.Join(workDir(c),
			"tools.json")); err != nil {
			return fmt.Errorf("run %s: %w", runID(c), err)
		}
	}
	policy, err := contextPolicy(cmp.Or(c.context, "default"),
		agent.Generator(sm)) // summaries get the same retries
	if err != nil {
		return err
	}
	ctx = replay.WithScript(ctx, scriptName(c))
	marker := safety.NewMarker() // one for this run of the program
	if saved != nil {            // the saved conversation's rule names its boundary
		if marker, err = keptMarker(workDir(c)); err != nil {
			return err
		}
	}
	lim := agent.Limits{MaxTurns: cmp.Or(c.maxTurns, 40), MaxCost: c.maxCost,
		Context: policy}
	msgs := []agent.Message{
		{Role: "system", Content: triage.Prompt}, // <1>
		{Role: "user", Content: instruction},
	}
	tools := triage.Tools(j.Env)
	builtin := make([]string, len(tools))
	for i, t := range tools {
		builtin[i] = t.Def.Name
	}
	if c.tracker != "builtin" { // the tracker through MCP (chapter 10)
		server, err := startTracker(ctx, j)
		if err != nil {
			return err
		}
		defer server.Close()
		pins, err := loadPins(c.pins)
		if err != nil {
			return err
		}
		if tools, err = trackerTools(ctx, server, j, tools, pins); err != nil {
			return err
		}
		msgs[0].Content = renameIssueTools(msgs[0].Content)
	}
	if c.dieAfter != "" { // chapter 15's demonstration
		if tools, err = withDieAfter(tools, builtin, c.dieAfter,
			c.state); err != nil {
			return err
		}
	}
	if !c.noSub { // a sub-agent with the same model and the run's pool
		pool := &agent.SpendPool{Limit: c.maxCost}
		lim.Spend = pool
		tools = append(tools, triage.ExplainPackage(sm, j.Env,
			agent.Limits{MaxTurns: 15, Spend: pool, Context: policy}))
		msgs[0].Content += "\n\n" + explainLine
	}
	d, err := newDefense(ctx, getenv, defenseConfig{c.defenses, c.approve,
		c.minRung}, j, &marker, w) // chapter 14
	if err != nil {
		return err
	}
	defer d.close()
	if err := d.openAudit(getenv, c.audit); err != nil {
		return err
	}
	msgs[1].Content += d.rule
	lim.Approver, wr.outer = d.approver, d.outer
	tools = wr.Tools(agent.WrapTools(tools, d.inner...))
	if d.box.Rung > 0 {
		p.printf("defenses on; tests run at %s\n", rungName(d.box.Rung))
	}
	ctx, span := agent.StartRun(ctx, wr.rec, "triage", wr.provider)
	events := agent.Events(ctx, sm, tools, msgs, lim)
	if saved != nil { // chapter 15
		if err := saved.restore(ctx, c, msgs); err != nil {
			return err
		}
		if err := d.recall(getenv, c.audit, saved); err != nil {
			return err
		}
		if saved.resumed {
			p.printf("resuming run %s: %s, %d turns, $%.4f so far, "+
				"restart %d\n", saved.st.RunID, saved.st.Phase,
				saved.st.Turns, saved.st.Usage.Cost, saved.st.Resumes)
		}
		ctx = agent.WithName(ctx, saved.st.RunID) // for the audit log
		tools = agent.WrapTools(tools, agent.Grace(grace))
		lim.Checkpoint = saved.cp
		events = agent.ResumeEvents(ctx, sm, tools, saved.st, lim)
	}
	p.printf("task: %s, %d crash reports, %d issues already filed\n",
		j.Task.ID, len(j.Env.Reports), len(j.Task.Known))
	if turn := firstTurn(saved); turn > 0 {
		p.printf("\nturn %d\n", turn)
	}
	res, runErr := watch(events, p)
	span.Finish(runErr)
	if c.save != "" {
		if err := saveConversation(c.save, res.Messages); err != nil {
			return err
		}
	}
	p.printf("\nstopped: %s\n", whyStopped(runErr))
	if runErr != nil {
		p.printf("error: %v\n", runErr)
	}
	printPending(p, res.Pending) // chapter 14
	if err := d.review(p, j); err != nil {
		return err
	}
	u := res.Usage
	p.printf("%d tokens in (%d from the cache), %d out, $%.4f\n",
		u.PromptTokens, u.CachedTokens, u.CompletionTokens, u.Cost)
	if lim.Spend != nil {
		p.printf("with the sub-agent's calls: $%.4f\n", lim.Spend.Spent())
	}
	if getenv("OPENROUTER_API_KEY") != "" { // a replay's time means nothing
		p.printf("took %s\n", time.Since(start).Round(time.Second))
	}
	if res.Context.Summary != "" {
		p.printf("compacted: a summary covers the first %d messages\n",
			res.Context.Covered)
	}
	if res.Final != nil { // <2>
		if err := report(ctx, p, j, res.Final); err != nil {
			return err
		}
	}
	wr.report(c, w, p) // the trace and the cost, if asked for
	if p.err != nil {
		return p.err
	}
	if runErr != nil {
		return &stoppedError{runErr}
	}
	return nil
}

// end::run[]

// modelName is the model a run calls, for traces and logs.
func modelName(getenv func(string) string, c config, direct bool) string {
	if c.provider == "anthropic" && direct {
		return cmp.Or(getenv("ANTHROPIC_MODEL"), "claude-haiku-5-5")
	}
	return cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel)
}

// setUpRun sets up the run's task: in a temporary directory, or, with
// -state, in the run's own directory, found again after a restart. A
// run with a checkpoint whose directory isn't whole can't go on: its
// conversation describes a workspace and a tracker that aren't there.
func setUpRun(ctx context.Context, c config,
	saved *savedRun) (*taskrun.Job, error) {
	if c.state == "" {
		return setUp(ctx, c.repo, c.task)
	}
	if _, err := saved.cp.Load(); err == nil && !ready(workDir(c)) {
		return nil, fmt.Errorf("%w: run %s's workspace is missing",
			agent.ErrWorkspaceChanged, runID(c))
	}
	if n, err := prune(c); err != nil {
		return nil, err
	} else if n > 0 {
		log.Printf("pruned %d finished runs", n)
	}
	tasks, err := taskrun.Load(c.repo)
	if err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	task, err := taskrun.Find(tasks, c.task)
	if err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	j, err := taskrun.SetUpIn(ctx, c.repo, task, workDir(c))
	if err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	return j, nil
}

// setUp loads a task, or builds the batch of every catalog crash, and
// sets it up.
func setUp(ctx context.Context, repo, id string) (*taskrun.Job, error) {
	tasks, err := taskrun.Load(repo)
	if err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	task, err := taskrun.Find(tasks, id)
	if id == "batch" {
		task, err = catalog.Batch(tasks)
	}
	if err != nil {
		return nil, fmt.Errorf("set up: %w", err)
	}
	j, err := taskrun.SetUp(ctx, repo, task)
	if err != nil {
		return nil, fmt.Errorf("set up %s: %w", id, err)
	}
	return j, nil
}

// A stoppedError is a run that ended early, which run has already
// reported; main only sets the exit status.
type stoppedError struct{ err error }

func (e *stoppedError) Error() string { return e.err.Error() }
func (e *stoppedError) Unwrap() error { return e.err }

// tag::watch[]

// watch runs the agent and prints what happens as it happens: the
// model's text as it is written, each tool call, and each result.
func watch(events iter.Seq[agent.Event], p *printer) (agent.Result,
	error) {
	text := &textWriter{p: p}
	next := 0 // a turn whose heading waits for its first event
	for ev := range events {
		if next > 0 && (ev.Kind == agent.TextEvent ||
			ev.Kind == agent.CallEvent) {
			p.printf("\nturn %d\n", next)
			next = 0
		}
		switch ev.Kind {
		case agent.TextEvent:
			text.write(ev.Text) // <1>
		case agent.CallEvent:
			text.end()
			printCall(p, ev.Call)
		case agent.ToolResultEvent:
			printResult(p, ev.Tool)
		case agent.ResetEvent: // chapter 12
			text.end()
			p.printf("  (the reply broke off; the call starts again)\n")
		case agent.TurnEvent:
			next = ev.Turn + 1 // ev.Turn turns have ended
		case agent.StoppedEvent:
			if text.end() && ev.Err != nil { // <2>
				p.printf("  (cut off: not in the conversation)\n")
			}
			if ev.Err != nil {
				return ev.Result, fmt.Errorf("run agent: %w", ev.Err)
			}
			return ev.Result, nil
		}
	}
	return agent.Result{}, errors.New("run agent: no stop event")
}

// end::watch[]

// A model is what the program needs from a client: a whole reply, for
// compaction summaries, and a reply in pieces, for the loop.
type model interface {
	agent.Model
	agent.StreamModel
}

// tag::new-client[]

// newClient returns the client -provider names, calling the live API
// when that provider has a key and replaying the saved runs when not.
func newClient(getenv func(string) string, w io.Writer,
	c config) (model, error) {
	key, direct := providerKey(getenv, c.provider)
	httpClient, err := transport(key, w, c.record)
	if err != nil {
		return nil, err
	}
	httpClient = withFaults(httpClient, c.faults)
	name := cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel)
	base := getenv("OPENROUTER_BASE_URL")
	switch c.provider {
	case "", "openrouter":
		return &openrouter.Client{APIKey: key, Model: name,
			BaseURL: base, HTTP: httpClient}, nil
	case "sdk":
		m := orsdk.New(key, name, base, httpClient)
		return struct { // the SDK adapter has no Stream of its own
			agent.Model
			agent.StreamModel
		}{m, agent.Streamed(m)}, nil
	case "anthropic":
		return anthropicClient(getenv, key, name, direct, httpClient,
			c.prices)
	}
	return nil, fmt.Errorf("no provider %q; try openrouter, sdk, or "+
		"anthropic", c.provider)
}

// end::new-client[]

// providerKey returns the key for a provider: Anthropic's own, when
// the provider is anthropic and ANTHROPIC_API_KEY is set (direct), and
// otherwise OpenRouter's.
func providerKey(getenv func(string) string, provider string) (string,
	bool) {
	if k := getenv("ANTHROPIC_API_KEY"); provider == "anthropic" && k != "" {
		return k, true
	}
	return getenv("OPENROUTER_API_KEY"), false
}

// tag::anthropic-client[]

// anthropicClient returns a Messages API client: Anthropic's own, with
// its model names, when direct, and otherwise OpenRouter's, which
// serves the same API with the OpenRouter key and OpenRouter's names.
func anthropicClient(getenv func(string) string, key, name string,
	direct bool, httpClient *http.Client,
	priceFile string) (*anthropic.Client, error) {
	prices, err := anthropic.DefaultPrices()
	if priceFile != "" {
		prices, err = anthropic.LoadPrices(priceFile)
	}
	if err != nil {
		return nil, fmt.Errorf("prices: %w", err)
	}
	c := &anthropic.Client{APIKey: key, Model: name, Prices: prices,
		BaseURL: openrouter.DefaultBaseURL, HTTP: httpClient}
	if direct {
		c.BaseURL = anthropic.DefaultBaseURL
		c.Model = cmp.Or(getenv("ANTHROPIC_MODEL"), "claude-haiku-5-5")
	}
	if _, ok := prices[c.Model]; !ok { // a cost of 0 disables -max-cost
		return nil, fmt.Errorf("no price for %s; add it with -prices",
			c.Model)
	}
	return c, nil
}

// end::anthropic-client[]

// transport returns the HTTP client a model client uses: one that
// replays the saved calls without a key, one that records them when
// asked, and otherwise the network.
func transport(key string, w io.Writer, record string) (*http.Client,
	error) {
	switch {
	case key == "":
		files, err := fs.Sub(embedded, "testdata/replies")
		if err != nil {
			return nil, fmt.Errorf("saved replies: %w", err)
		}
		if _, err := fmt.Fprintln(w, "No OPENROUTER_API_KEY: "+
			"replaying a saved run. The tools run for real."); err != nil {
			return nil, fmt.Errorf("write output: %w", err)
		}
		return &http.Client{Transport: &replay.Transport{Files: files}}, nil
	case record != "":
		return &http.Client{Transport: &replay.Recorder{Dir: record}}, nil
	}
	return &http.Client{}, nil
}

// scriptName names a run's saved calls: the task, and the provider
// when it isn't chapter 2's client, since each API's calls differ.
func scriptName(c config) string {
	if c.provider == "" || c.provider == "openrouter" {
		return c.task
	}
	return c.task + "-" + c.provider
}

// retryable is an API error that says whether a retry might help, as
// both the OpenRouter client's and the Anthropic client's do.
type retryable interface {
	error
	Retryable() bool
}

// tag::save[]

// saveConversation writes msgs to a file as indented JSON, to read or
// load later. It works after a failed run too: the messages up to the
// failure are often what you need to see.
func saveConversation(file string, msgs []agent.Message) error {
	b, err := json.Marshal(savable(msgs), jsontext.Multiline(true),
		json.OmitZeroStructFields(true))
	if err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	return nil
}

// end::save[]

// savable returns msgs with any tool-call arguments that aren't valid
// JSON turned into a JSON string, so the file is valid JSON: a model can
// write broken arguments, and the conversation keeps them as sent.
func savable(msgs []agent.Message) []agent.Message {
	out := slices.Clone(msgs)
	for i, m := range out {
		copied := false
		for j, c := range m.ToolCalls {
			if len(c.Arguments) == 0 || c.Arguments.IsValid() {
				continue
			}
			if !copied { // never change the caller's messages
				out[i].ToolCalls, copied = slices.Clone(m.ToolCalls), true
			}
			// Invalid UTF-8 comes back replaced, which is fine here.
			q, _ := jsontext.AppendQuote(nil, c.Arguments)
			out[i].ToolCalls[j].Arguments = q
		}
	}
	return out
}

// tag::why-stopped[]

// whyStopped says in a few words why a run ended, from its error.
func whyStopped(err error) string {
	switch {
	case err == nil:
		return "the model answered"
	case errors.Is(err, agent.ErrMaxTurns):
		return "the turn limit"
	case errors.Is(err, agent.ErrBudgetExceeded):
		return "the cost budget"
	case errors.Is(err, openrouter.ErrStreamIdle), // before Canceled
		errors.Is(err, anthropic.ErrStreamIdle):
		return "the stream went quiet, worth retrying"
	case errors.Is(err, agent.ErrShutdown): // before Canceled
		return "asked to stop; start the same command again to resume"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, durable.ErrMaxResumes): // before ErrBudget
		return "restarted too many times; give -run a new ID for a " +
			"new run"
	case errors.Is(err, agent.ErrOutcomeUnknown):
		return "a call's outcome is unknown; resume to settle it"
	case errors.Is(err, openrouter.ErrCallTimeout),
		errors.Is(err, anthropic.ErrCallTimeout):
		return "the model call timed out, worth retrying"
	case errors.Is(err, context.DeadlineExceeded):
		return "a deadline passed"
	case errors.Is(err, agent.ErrNoFinalAnswer):
		return "the model answered without submitting a diagnosis"
	case errors.Is(err, agent.ErrRefused):
		return "the model refused" // chapter 11
	case errors.Is(err, agent.ErrBadReply):
		return "a reply the loop can't use"
	case errors.Is(err, agent.ErrToolFatal):
		return "a tool that can't run" // chapter 9
	}
	if _, ok := errors.AsType[*agent.ApprovalPending](err); ok {
		return "a call waits for a person's decision" // chapter 14
	}
	if e, ok := errors.AsType[retryable](err); ok { // either API's error
		if e.Retryable() {
			return "an API error, worth retrying"
		}
		return "an API error"
	}
	return "an error"
}

// end::why-stopped[]
