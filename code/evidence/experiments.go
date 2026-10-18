package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch06/openrouter"
)

// usage is agent.Usage with JSON names.
type usage struct {
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Cached     int     `json:"cached,omitzero"`
	Reasoning  int     `json:"reasoning,omitzero"`
	Cost       float64 `json:"cost"`
}

func fromUsage(u agent.Usage) usage {
	return usage{u.PromptTokens, u.CompletionTokens, u.CachedTokens,
		u.ReasoningTokens, u.Cost}
}

// A call is one model call's measurements.
type call struct {
	usage
	Seconds float64 `json:"seconds"`
	Finish  string  `json:"finish,omitzero"`
	Calls   int     `json:"calls,omitzero"` // tool calls in the reply
	Err     string  `json:"err,omitzero"`
}

// meter wraps a model and measures each call.
type meter struct {
	m     agent.Model
	mu    sync.Mutex
	calls []call
}

func (m *meter) Generate(ctx context.Context, req agent.Request) (
	agent.Reply, error) {
	start := time.Now()
	r, err := m.m.Generate(ctx, req)
	c := call{usage: fromUsage(r.Usage), Seconds: since(start),
		Finish: r.FinishReason, Calls: len(r.Message.ToolCalls)}
	if err != nil {
		c.Err = err.Error()
	}
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	return r, err //nolint:wrapcheck // passed through unchanged
}

func since(t time.Time) float64 {
	return time.Since(t).Round(time.Millisecond).Seconds()
}

// client returns a client for model whose calls are recorded under
// dir as script.
func (e *env) client(model, dir, script string) (*openrouter.Client, *tee) {
	t := &tee{dir: dir, script: script}
	return &openrouter.Client{APIKey: e.key, Model: model,
		HTTP: &http.Client{Transport: t}}, t
}

// perModel runs f for each model at once and joins their errors.
func perModel(ms []string, f func(model string) error) error {
	errs := make([]error, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Go(func() {
			if err := f(m); err != nil {
				errs[i] = fmt.Errorf("%s: %w", m, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// next returns the number of the first new run for model, after the
// runs already saved, so runs can be added later.
func next[T interface{ model() string }](file, model string) (int, error) {
	saved, err := load[T](file)
	if err != nil {
		return 0, err
	}
	n := 1
	for _, r := range saved {
		if r.model() == model {
			n++
		}
	}
	return n, nil
}

// The fix task (chapters 1, 4, 5).

const fixTask = "The tests in this project fail. Find out why and fix it."

type fixRun struct {
	Model      string         `json:"model"`
	Run        int            `json:"run"`
	Stopped    string         `json:"stopped"` // "answer", or the error
	Passed     bool           `json:"passed"`  // the tests pass afterwards
	TestEdited bool           `json:"test_edited"`
	ToolCalls  map[string]int `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	Calls      []call         `json:"calls"`
	Total      usage          `json:"total"`
	Seconds    float64        `json:"seconds"`
	Path       []string       `json:"path,omitzero"`   // each reply's tool calls
	Answer     string         `json:"answer,omitzero"` // the last reply's text
}

func (r fixRun) model() string { return r.Model }

// A variant changes the fix task's tools, to see how models respond.
type variant struct {
	name   string // "" for the plain task
	models []string
	n      int
	modify func(tools []agent.Tool)
}

var variants = map[string]variant{
	// Chapter 4's Try it: does a description steer the model?
	"prefer-test": {"prefer-test", models[:2], 10, func(tools []agent.Tool) {
		tools[1].Def.Description = "Read a file. Prefer reading the test file first."
	}},
	// The same, with an error that says the failure is permanent.
	"locked-clear": {"locked-clear", models, 3, func(tools []agent.Tool) {
		tools[2].Run = func(context.Context, jsontext.Value) (string, error) {
			return "", errors.New("the project is read-only: edits are " +
				"not possible in this session, so don't retry; report the " +
				"fix instead")
		}
	}},
	// Chapter 4's Try it: a tool that always fails.
	"locked": {"locked", models, 3, func(tools []agent.Tool) {
		tools[2].Run = func(context.Context, jsontext.Value) (string, error) {
			return "", errors.New("the file is locked")
		}
	}},
}

func runFix(ctx context.Context, e *env) error {
	return runVariant(ctx, e, variant{models: models, n: 10})
}

func runVariant(ctx context.Context, e *env, v variant) error {
	project, err := readProject("ch06/testdata/duration")
	if err != nil {
		return err
	}
	name := "fix"
	if v.name != "" {
		name = "fix-" + v.name
	}
	out := &results{file: filepath.Join(e.dir, name+".jsonl")}
	return perModel(v.models, func(model string) error {
		first, err := next[fixRun](out.file, model)
		if err != nil {
			return err
		}
		for i := range cmp.Or(e.n, v.n) {
			if err := e.spend.check(); err != nil {
				return err
			}
			r, err := fixOnce(ctx, e, name, model, first+i, project, v.modify)
			if err != nil {
				return err
			}
			e.spend.add(r.Total.Cost)
			fmt.Printf("fix %s %d: %s, passed %v, %d turns, $%.4f\n",
				model, r.Run, r.Stopped, r.Passed, len(r.Calls), r.Total.Cost)
			if err := out.add(r); err != nil {
				return err
			}
		}
		return nil
	})
}

func fixOnce(ctx context.Context, e *env, name, model string, n int,
	project agent.FS, modify func([]agent.Tool)) (fixRun, error) {
	c, t := e.client(model, filepath.Join(e.dir, name, slug(model)),
		fmt.Sprintf("run-%02d", n))
	m := &meter{m: c}
	files := agent.FS{}
	maps.Copy(files, project)
	tools := agent.Tools(files)
	if modify != nil {
		modify(tools)
	}
	start := time.Now()
	res, runErr := agent.Run(ctx, m, tools,
		[]agent.Message{{Role: "user", Content: fixTask}},
		agent.Limits{MaxTurns: 20, MaxCost: 0.10})
	r := fixRun{Model: model, Run: n, Stopped: "answer",
		ToolCalls: map[string]int{}, Calls: m.calls,
		Total: fromUsage(res.Usage), Seconds: since(start)}
	if runErr != nil {
		r.Stopped = runErr.Error()
	}
	if ctx.Err() != nil {
		return r, fmt.Errorf("fix run: %w", ctx.Err())
	}
	for _, msg := range res.Messages {
		var names []string
		for _, tc := range msg.ToolCalls {
			r.ToolCalls[tc.Name]++
			names = append(names, tc.Name)
		}
		if msg.Role == "assistant" {
			r.Path = append(r.Path, cmp.Or(strings.Join(names, "+"), "answer"))
			r.Answer = msg.Content
		}
		if msg.Role == "tool" && msg.IsError {
			r.ToolErrors++
		}
	}
	r.TestEdited = files["duration_test.go"] != project["duration_test.go"]
	report, err := agent.Tools(files)[3].Run(ctx, nil) // run_tests
	r.Passed = err == nil && !strings.Contains(report, "FAIL") &&
		strings.Contains(report, "ok")
	return r, t.err()
}

func readProject(dir string) (agent.FS, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	files := agent.FS{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read project: %w", err)
		}
		files[e.Name()] = string(b)
	}
	return files, nil
}

// One question to each model (chapter 2).

const question = "In Go, what is the difference between a nil slice " +
	"and an empty slice? Answer in under 100 words."

type askRun struct {
	Model string `json:"model"`
	Run   int    `json:"run"`
	call
	Text string `json:"text"`
}

func (r askRun) model() string { return r.Model }

func runAsk(ctx context.Context, e *env) error {
	out := &results{file: filepath.Join(e.dir, "ask.jsonl")}
	return perModel(models, func(model string) error {
		first, err := next[askRun](out.file, model)
		if err != nil {
			return err
		}
		for i := range cmp.Or(e.n, 3) {
			if err := e.spend.check(); err != nil {
				return err
			}
			n := first + i
			c, t := e.client(model, filepath.Join(e.dir, "ask", slug(model)),
				fmt.Sprintf("run-%02d", n))
			m := &meter{m: c}
			reply, err := m.Generate(ctx, agent.Request{Messages: []agent.Message{
				{Role: "user", Content: question}}})
			if ctx.Err() != nil {
				return fmt.Errorf("ask: %w", ctx.Err())
			}
			r := askRun{Model: model, Run: n, call: m.calls[0],
				Text: reply.Message.Content}
			if err := cmp.Or(err, t.err()); err != nil && r.Err == "" {
				return fmt.Errorf("ask: %w", err)
			}
			e.spend.add(r.Cost)
			fmt.Printf("ask %s %d: %d+%d tokens, %.2fs\n", model, n,
				r.Prompt, r.Completion, r.Seconds)
			if err := out.add(r); err != nil {
				return err
			}
		}
		return nil
	})
}

// A long chat, to see the cost of resending history (chapter 2).

type chatRun struct {
	Model string `json:"model"`
	Run   int    `json:"run"`
	Turns []call `json:"turns"`
}

func (r chatRun) model() string { return r.Model }

func runChat(ctx context.Context, e *env) error {
	questions, err := lines(filepath.Join(e.dir, "chat-questions.txt"))
	if err != nil {
		return err
	}
	out := &results{file: filepath.Join(e.dir, "chat.jsonl")}
	return perModel(models, func(model string) error {
		first, err := next[chatRun](out.file, model)
		if err != nil {
			return err
		}
		for i := range cmp.Or(e.n, 1) {
			if err := e.spend.check(); err != nil {
				return err
			}
			n := first + i
			c, t := e.client(model, filepath.Join(e.dir, "chat", slug(model)),
				fmt.Sprintf("run-%02d", n))
			m := &meter{m: c}
			var msgs []agent.Message
			for _, q := range questions {
				msgs = append(msgs, agent.Message{Role: "user", Content: q})
				reply, err := m.Generate(ctx, agent.Request{Messages: msgs})
				if err != nil {
					return err
				}
				msgs = append(msgs, agent.Message{Role: "assistant",
					Content: reply.Message.Content})
			}
			if err := t.err(); err != nil {
				return err
			}
			r := chatRun{Model: model, Run: n, Turns: m.calls}
			var cost float64
			for _, c := range r.Turns {
				cost += c.Cost
			}
			e.spend.add(cost)
			fmt.Printf("chat %s %d: %d turns, $%.4f\n", model, n,
				len(r.Turns), cost)
			if err := out.add(r); err != nil {
				return err
			}
		}
		return nil
	})
}

func lines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("read lines: %w", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			out = append(out, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read lines: %w", err)
	}
	return out, nil
}

// Token counts for the same text on each model's tokenizer (chapter 3).

type tokenRun struct {
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Text     string  `json:"text"` // which text
	Bytes    int     `json:"bytes"`
	Words    int     `json:"words"`
	Prompt   int     `json:"prompt"`   // prompt tokens with the text
	Baseline int     `json:"baseline"` // prompt tokens with "."
	Tokens   int     `json:"tokens"`   // the difference: the text's tokens
	Cost     float64 `json:"cost"`
}

// tokenRoutes pins each count to one provider, so the text and the
// baseline are counted by the same one: the model's maker where it
// serves the model (DeepSeek's own is excluded by this account's
// data settings), and for one model, two others to compare.
var tokenRoutes = []struct{ model, provider string }{
	{models[0], ""}, // one provider serves it
	{models[1], ""},
	{models[2], "z-ai"},
	{models[2], "coreweave"},
	{models[2], "relace"},
	{models[3], "morph"},
	{probeModel, "novita"},
}

func runTokens(ctx context.Context, e *env) error {
	code, err := os.ReadFile("ch06/testdata/duration/duration.go")
	if err != nil {
		return fmt.Errorf("read code text: %w", err)
	}
	prose, err := os.ReadFile(filepath.Join(e.dir, "prose.txt"))
	if err != nil {
		return fmt.Errorf("read prose text: %w", err)
	}
	texts := []struct{ name, text string }{
		{"Go code (duration.go)", string(code)},
		{"English prose", string(prose)},
	}
	out := &results{file: filepath.Join(e.dir, "tokens.jsonl")}
	var routes []string
	for i := range tokenRoutes {
		routes = append(routes, strconv.Itoa(i))
	}
	return perModel(routes, func(i string) error {
		n, _ := strconv.Atoi(i)
		route := tokenRoutes[n]
		c, t := e.client(route.model, filepath.Join(e.dir, "tokens",
			slug(route.model)), "count-"+cmp.Or(route.provider, "any"))
		if route.provider != "" {
			c.Providers = []string{route.provider}
		}
		var provider string
		count := func(s string) (int, error) {
			reply, err := c.Generate(ctx, agent.Request{MaxTokens: 400,
				Messages: []agent.Message{{Role: "user", Content: s}}})
			if err != nil {
				return 0, fmt.Errorf("count tokens: %w", err)
			}
			e.spend.add(reply.Usage.Cost)
			if reply.Usage.PromptTokens == 0 {
				return 0, errors.New("count tokens: usage reported 0")
			}
			return reply.Usage.PromptTokens, nil
		}
		base, err := count(".")
		if err != nil {
			return err
		}
		for _, x := range texts {
			prompt, err := count(x.text)
			if err != nil {
				return err
			}
			r := tokenRun{Model: route.model, Provider: cmp.Or(route.provider, provider),
				Text: x.name, Bytes: len(x.text), Words: len(strings.Fields(x.text)),
				Prompt: prompt, Baseline: base, Tokens: prompt - base}
			fmt.Printf("tokens %s (%s) %s: %d\n", r.Model, r.Provider, x.name, r.Tokens)
			if err := out.add(r); err != nil {
				return err
			}
		}
		return t.err()
	})
}

// The probe at several temperatures (chapter 3). It sends its own
// requests so it can choose the provider: "any" lets OpenRouter pick
// one that reports logprobs, as the book's Probe does, and a provider
// name pins that one. The summary reads the recordings, since they hold
// both the reply's text and the logprobs to compare it with.

var probeRoutes = map[string]string{
	"any":    `{"require_parameters": true}`,
	"novita": `{"require_parameters": true, "order": ["Novita"], "allow_fallbacks": false}`,
}

func runProbe(ctx context.Context, e *env) error {
	route := cmp.Or(os.Getenv("PROBE_ROUTE"), "novita")
	routing, ok := probeRoutes[route]
	if !ok {
		return fmt.Errorf("no probe route %q", route)
	}
	dir := filepath.Join(e.dir, "probe", route)
	return perModel([]string{"0", "0.7", "1", "1.5"}, func(ts string) error {
		saved, err := filepath.Glob(filepath.Join(dir, "t"+ts+"-*.json"))
		if err != nil {
			return fmt.Errorf("list probe recordings: %w", err)
		}
		t := &tee{dir: dir, script: "t" + ts, n: len(saved)}
		hc := &http.Client{Transport: t}
		body := fmt.Sprintf(`{"model": %q, "messages": [{"role": "user", `+
			`"content": "Describe Go in exactly three words."}], `+
			`"temperature": %s, "max_tokens": 10, "logprobs": true, `+
			`"top_logprobs": 3, "provider": %s}`, probeModel, ts, routing)
		for range cmp.Or(e.n, 10) {
			if err := e.spend.check(); err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				openrouter.DefaultBaseURL+"/chat/completions",
				strings.NewReader(body))
			if err != nil {
				return fmt.Errorf("build probe request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+e.key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := hc.Do(req)
			if err != nil {
				return fmt.Errorf("send probe: %w", err)
			}
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("read probe response: %w", err)
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("probe: status %d", resp.StatusCode)
			}
			fmt.Printf("probe %s t=%s\n", route, ts)
		}
		return t.err()
	})
}

// Stream timing: time to the first piece and to the whole reply, with
// and without streaming (chapter 6).

const explain = "In about 150 words, explain what context.Context " +
	"is for in Go."

type streamRun struct {
	Model     string  `json:"model"`
	Run       int     `json:"run"`
	Mode      string  `json:"mode"`                // "stream" or "generate"
	First     float64 `json:"first,omitzero"`      // seconds to the first piece
	FirstText float64 `json:"first_text,omitzero"` // to the first text
	Total     float64 `json:"total"`
	MaxGap    float64 `json:"max_gap,omitzero"` // longest wait between pieces
	Deltas    int     `json:"deltas,omitzero"`
	usage
}

func (r streamRun) model() string { return r.Model }

func runStream(ctx context.Context, e *env) error {
	out := &results{file: filepath.Join(e.dir, "stream.jsonl")}
	req := agent.Request{Messages: []agent.Message{
		{Role: "user", Content: explain}}}
	return perModel(models, func(model string) error {
		saved, err := next[streamRun](out.file, model)
		if err != nil {
			return err
		}
		first := (saved-1)/2 + 1 // two saved results per run
		for i := range cmp.Or(e.n, 5) {
			if err := e.spend.check(); err != nil {
				return err
			}
			n := first + i
			dir := filepath.Join(e.dir, "stream", slug(model))
			modes := []string{"stream", "generate"}
			if n%2 == 0 {
				modes = []string{"generate", "stream"} // alternate the order
			}
			for _, mode := range modes {
				c, t := e.client(model, dir, fmt.Sprintf("%s-%02d", mode, n))
				r := streamRun{Model: model, Run: n, Mode: mode}
				var err error
				if mode == "stream" {
					err = timeStream(ctx, c, req, &r)
				} else {
					start := time.Now()
					var reply agent.Reply
					reply, err = c.Generate(ctx, req)
					r.Total, r.usage = since(start), fromUsage(reply.Usage)
				}
				if err := cmp.Or(err, t.err()); err != nil {
					return fmt.Errorf("%s: %w", mode, err)
				}
				e.spend.add(r.Cost)
				fmt.Printf("stream %s %d %s: first %.2fs, total %.2fs\n",
					model, n, mode, r.FirstText, r.Total)
				if err := out.add(r); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func timeStream(ctx context.Context, c *openrouter.Client,
	req agent.Request, r *streamRun) error {
	start := time.Now()
	last := start
	for d, err := range c.Stream(ctx, req) {
		if err != nil {
			return err
		}
		now := time.Now()
		if r.Deltas == 0 {
			r.First = since(start)
		}
		if d.Text != "" && r.FirstText == 0 {
			r.FirstText = since(start)
		}
		if gap := now.Sub(last).Round(time.Millisecond).Seconds(); r.Deltas > 0 &&
			gap > r.MaxGap {
			r.MaxGap = gap
		}
		last = now
		r.Deltas++
		if d.Usage != nil {
			r.usage = fromUsage(*d.Usage)
		}
	}
	r.Total = since(start)
	return nil
}

// What tools cost per call (chapters 1 and 4): the task alone, the task
// with the four tool definitions, and the task with the same definitions
// pasted in as text, which separates the definitions' own tokens from
// what the provider adds when a request carries tools.

type overheadRun struct {
	Model     string `json:"model"`
	TaskOnly  int    `json:"task_only"`
	WithTools int    `json:"with_tools"`
	AsText    int    `json:"as_text"` // the definitions as JSON text
}

func runOverhead(ctx context.Context, e *env) error {
	tools := agent.Tools(agent.FS{})
	var defs []agent.ToolDef
	type wireDef struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  jsontext.Value `json:"parameters"`
		} `json:"function"`
	}
	var wire []wireDef
	for _, t := range tools {
		defs = append(defs, t.Def)
		var w wireDef
		w.Type = "function"
		w.Function.Name = t.Def.Name
		w.Function.Description = t.Def.Description
		w.Function.Parameters = t.Def.Parameters
		wire = append(wire, w)
	}
	text, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("overhead: %w", err)
	}
	out := &results{file: filepath.Join(e.dir, "overhead.jsonl")}
	return perModel(models, func(model string) error {
		c, t := e.client(model, filepath.Join(e.dir, "overhead", slug(model)),
			"count")
		prompt := func(content string, defs []agent.ToolDef) (int, error) {
			reply, err := c.Generate(ctx, agent.Request{MaxTokens: 400,
				Tools:    defs,
				Messages: []agent.Message{{Role: "user", Content: content}}})
			if err != nil {
				return 0, fmt.Errorf("overhead: %w", err)
			}
			e.spend.add(reply.Usage.Cost)
			return reply.Usage.PromptTokens, nil
		}
		r := overheadRun{Model: model}
		if r.TaskOnly, err = prompt(fixTask, nil); err != nil {
			return err
		}
		if r.WithTools, err = prompt(fixTask, defs); err != nil {
			return err
		}
		if r.AsText, err = prompt(fixTask+"\n\n"+string(text), nil); err != nil {
			return err
		}
		fmt.Printf("overhead %s: %+v\n", model, r)
		if err := out.add(r); err != nil {
			return err
		}
		return t.err()
	})
}

// The same bug fixed by a workflow (chapter 1): code runs the tests and
// reads the files, one model call proposes an edit, and code applies it
// and runs the tests again. No tools, no loop.

const workflowPrompt = "The tests in this Go project fail. Below are " +
	"the test output and the source files. Reply with only a JSON " +
	`object {"path": ..., "old": ..., "new": ...} describing one edit ` +
	"that fixes the bug: old is text that appears exactly once in the " +
	"file, and new replaces it."

type workflowRun struct {
	Model string `json:"model"`
	Run   int    `json:"run"`
	call
	Edit   string `json:"edit"` // the JSON the model wrote
	Passed bool   `json:"passed"`
}

func (r workflowRun) model() string { return r.Model }

func runWorkflow(ctx context.Context, e *env) error {
	project, err := readProject("ch06/testdata/duration")
	if err != nil {
		return err
	}
	out := &results{file: filepath.Join(e.dir, "workflow.jsonl")}
	return perModel(models, func(model string) error {
		first, err := next[workflowRun](out.file, model)
		if err != nil {
			return err
		}
		for i := range cmp.Or(e.n, 10) {
			if err := e.spend.check(); err != nil {
				return err
			}
			r, err := workflowOnce(ctx, e, model, first+i, project)
			if err != nil {
				return err
			}
			e.spend.add(r.Cost)
			fmt.Printf("workflow %s %d: passed %v, $%.5f\n", model, r.Run,
				r.Passed, r.Cost)
			if err := out.add(r); err != nil {
				return err
			}
		}
		return nil
	})
}

func workflowOnce(ctx context.Context, e *env, model string, n int,
	project agent.FS) (workflowRun, error) {
	files := maps.Clone(project)
	tools := agent.Tools(files)
	report, err := tools[3].Run(ctx, nil) // run_tests
	if err != nil {
		return workflowRun{}, fmt.Errorf("workflow: %w", err)
	}
	var b strings.Builder
	b.WriteString(workflowPrompt + "\n\nTest output:\n" + report)
	for _, name := range files.List() {
		fmt.Fprintf(&b, "\n\nFile %s:\n%s", name, files[name])
	}
	c, t := e.client(model, filepath.Join(e.dir, "workflow", slug(model)),
		fmt.Sprintf("run-%02d", n))
	m := &meter{m: c}
	reply, err := m.Generate(ctx, agent.Request{Messages: []agent.Message{
		{Role: "user", Content: b.String()}}})
	if err != nil {
		return workflowRun{}, fmt.Errorf("workflow: %w", err)
	}
	r := workflowRun{Model: model, Run: n, call: m.calls[0]}
	edit := strings.TrimSpace(reply.Message.Content)
	edit = strings.TrimPrefix(edit, "```json")
	edit = strings.TrimSuffix(strings.TrimPrefix(edit, "```"), "```")
	r.Edit = strings.TrimSpace(edit)
	if _, err := tools[2].Run(ctx, jsontext.Value(r.Edit)); err == nil {
		report, err := tools[3].Run(ctx, nil)
		r.Passed = err == nil && !strings.Contains(report, "FAIL") &&
			strings.Contains(report, "ok")
	}
	return r, t.err()
}
