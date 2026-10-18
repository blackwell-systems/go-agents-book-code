// Command ch11 runs the crash-triage agent on a task from the crash
// catalog, or on every crash in it at once (-task batch), as in chapter
// 8, with tool calls run in parallel and a sub-agent, explain_package,
// for questions that would fill the context. With -groups the harness
// starts one agent per fingerprint group instead, several at once, each in
// its own copy of remind, and merges, files, and grades what they did.
// With OPENROUTER_API_KEY set it calls the live API; without it, it
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
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch11/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch11/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch11/orsdk"
	"github.com/blackwell-systems/go-agents-book/code/ch11/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch11/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch11/trackermcp"
	"github.com/blackwell-systems/go-agents-book/code/ch11/triage"
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
	digests := flag.Bool("print-digests", false,
		"print the digests of the tracker server's tools, as JSON, and exit")
	serve := flag.String("serve-tracker", "",
		"serve the issues in this file over MCP on standard input and "+
			"output, as the program does for itself with -tracker mcp")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
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
	err := run(ctx, os.Getenv, os.Stdout, c)
	if _, ok := errors.AsType[*stoppedError](err); ok {
		os.Exit(1) // run has printed why
	}
	if err != nil {
		log.Fatal(err)
	}
}

// config holds the program's settings, from its flags.
type config struct {
	task     string  // the catalog task, and the saved run's name
	context  string  // the context policy's name
	repo     string  // the directory holding remind and catalog
	record   string  // a directory to record calls to
	save     string  // a file to save the conversation to
	maxTurns int     // the run's turn limit
	maxCost  float64 // the run's cost budget, in dollars
	groups   bool    // one agent per fingerprint group (chapter 9)
	parallel int     // with groups, agents at once
	noSub    bool    // leave out explain_package
	tracker  string  // "mcp" or "builtin" (chapter 10)
	pins     string  // a file of tracker tool digests, if set
	provider string  // "openrouter", "sdk", or "anthropic" (chapter 11)
	prices   string  // a price table for -provider anthropic, if set
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
		return runGroups(ctx, getenv, w, model, c)
	}
	start := time.Now()
	j, err := setUp(ctx, c.repo, c.task)
	if err != nil {
		return err
	}
	defer j.Close()
	policy, err := contextPolicy(cmp.Or(c.context, "default"), model)
	if err != nil {
		return err
	}
	ctx = replay.WithScript(ctx, scriptName(c))
	lim := agent.Limits{MaxTurns: cmp.Or(c.maxTurns, 40), MaxCost: c.maxCost,
		Context: policy}
	msgs := []agent.Message{
		{Role: "system", Content: triage.Prompt}, // <1>
		{Role: "user", Content: instruction},
	}
	tools := triage.Tools(j.Env)
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
	if !c.noSub { // a sub-agent with the same model and the run's pool
		pool := &agent.SpendPool{Limit: c.maxCost}
		lim.Spend = pool
		tools = append(tools, triage.ExplainPackage(model, j.Env,
			agent.Limits{MaxTurns: 15, Spend: pool, Context: policy}))
		msgs[0].Content += "\n\n" + explainLine
	}
	p := &printer{w: w}
	p.printf("task: %s, %d crash reports, %d issues already filed\n\nturn 1\n",
		j.Task.ID, len(j.Env.Reports), len(j.Task.Known))
	res, runErr := watch(ctx, model, tools, msgs, lim, p)
	if c.save != "" {
		if err := saveConversation(c.save, res.Messages); err != nil {
			return err
		}
	}
	p.printf("\nstopped: %s\n", whyStopped(runErr))
	if runErr != nil {
		p.printf("error: %v\n", runErr)
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
	if p.err != nil {
		return p.err
	}
	if runErr != nil {
		return &stoppedError{runErr}
	}
	return nil
}

// end::run[]

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
func watch(ctx context.Context, m agent.StreamModel, tools []agent.Tool,
	msgs []agent.Message, lim agent.Limits, p *printer) (
	agent.Result, error) {
	text := &textWriter{p: p}
	next := 0 // a turn whose heading waits for its first event
	for ev := range agent.Events(ctx, m, tools, msgs, lim) {
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
	name := cmp.Or(getenv("OPENROUTER_MODEL"), defaultModel)
	base := getenv("OPENROUTER_BASE_URL")
	switch c.provider {
	case "", "openrouter":
		return &openrouter.Client{APIKey: key, Model: name,
			BaseURL: base, HTTP: httpClient}, nil
	case "sdk":
		m := orsdk.New(key, name, base, httpClient)
		return struct { // <1>
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
		return &http.Client{Transport: &replay.Recorder{Dir: record,
			Next: resend{http.DefaultTransport}}}, nil
	}
	return &http.Client{Transport: resend{http.DefaultTransport}}, nil
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

// resend sends a request a second time when the connection failed
// before any reply came back. A batch run makes a hundred calls or
// more, and in this chapter's runs about one call in a hundred lost
// its connection, which ended the run. Chapter 12 builds retries
// properly: which failures to retry, how long to wait, and when a
// repeated call could do something twice.
type resend struct{ next http.RoundTripper }

// RoundTrip passes the transport's errors on as they are, as a
// RoundTripper should, so the client wraps them in its *url.Error.
//
//nolint:wrapcheck // see above
func (t resend) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil || req.Context().Err() != nil || req.GetBody == nil {
		return resp, err
	}
	body, bodyErr := req.GetBody()
	if bodyErr != nil {
		return nil, err // the first failure is the one to report
	}
	// The pool's other connections may have failed the same way, so
	// the second try opens a new one.
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
	again := req.Clone(req.Context())
	again.Body = body
	return t.next.RoundTrip(again)
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
	case errors.Is(err, context.Canceled):
		return "cancelled"
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
	if e, ok := errors.AsType[retryable](err); ok { // either API's error
		if e.Retryable() {
			return "an API error, worth retrying"
		}
		return "an API error"
	}
	return "an error"
}

// end::why-stopped[]
