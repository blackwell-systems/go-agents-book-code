// Package genkit runs chapter 16's task on Genkit for Go: its Generate
// loop, its tool interrupts, and the OpenRouter plugin.
package genkit

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai/openrouter"
	"github.com/firebase/genkit/go/plugins/middleware"
	"github.com/openai/openai-go/option"

	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// tag::tools[]

// tools defines the task's three tools in g. Genkit builds each schema
// from the arguments' struct tags. issue_file is a plain tool; the
// approval middleware in Run interrupts it.
func tools(g *genkit.Genkit, env *task.Env) (
	*ai.ToolAction[task.IssueArgs, string], []ai.ToolRef) {
	run := genkit.DefineTool(g, "run_tests", task.TestsDoc,
		func(tc *ai.ToolContext, a task.TestArgs) (string, error) {
			return env.RunTests(tc, a)
		})
	issue := genkit.DefineTool(g, "issue_file", task.IssueDoc,
		func(tc *ai.ToolContext, a task.IssueArgs) (string, error) {
			return env.FileIssue(tc, a)
		})
	submit := genkit.DefineTool(g, "submit_diagnosis", task.SubmitDoc,
		func(tc *ai.ToolContext, d task.Diagnosis) (string, error) {
			return env.Submit(tc, d)
		})
	return issue, []ai.ToolRef{run, issue, submit}
}

// end::tools[]

// tag::setup[]

// options sets up Genkit with the OpenRouter plugin and returns what
// every Generate call in a run shares: the model, the tools, the turn
// limit, and the approval middleware.
func options(ctx context.Context, c task.Config, env *task.Env) (
	*genkit.Genkit, *ai.ToolAction[task.IssueArgs, string],
	[]ai.GenerateOption) {
	g := genkit.Init(ctx, genkit.WithPlugins(&openrouter.OpenRouter{
		APIKey: c.APIKey,
		Opts: []option.RequestOption{option.WithBaseURL(c.BaseURL),
			option.WithHTTPClient(c.HTTP)},
	}))
	issue, ts := tools(g, env)
	return g, issue, []ai.GenerateOption{
		ai.WithModel(openrouter.ModelRef(task.Model, nil)),
		ai.WithTools(ts...), ai.WithMaxTurns(10),
		ai.WithUse(&middleware.ToolApproval{ // <1>
			AllowedTools: []string{"run_tests", "submit_diagnosis"}}),
	}
}

// end::setup[]

// tag::run[]

// Run diagnoses the crash group. When the run interrupts for a person,
// Run asks env for each decision and generates again from the history.
func Run(ctx context.Context, c task.Config, env *task.Env) (
	task.Outcome, error) {
	var out task.Outcome
	g, issue, common := options(ctx, c, env)
	ask := []ai.GenerateOption{ai.WithSystem(task.Prompt),
		ai.WithPrompt(task.Crash)}
	resp, err := genkit.Generate(ctx, g, slices.Concat(common, ask)...)
	for err == nil && resp.FinishReason == ai.FinishReasonInterrupted {
		var next []ai.GenerateOption
		next, err = decide(resp, issue, env)
		if err != nil {
			return out, err
		}
		out.Pauses++
		if out.Saved, err = size(resp.History()); err != nil {
			return out, err
		}
		next = append(next, ai.WithMessages(resp.History()...)) // <1>
		resp, err = genkit.Generate(ctx, g,
			slices.Concat(common, next)...)
	}
	if err != nil {
		return out, fmt.Errorf("generate: %w", err)
	}
	return out, nil
}

// end::run[]

var approved = map[string]any{"toolApproved": true}

// tag::decide[]

// decide asks env about each interrupted call. An approved call is
// restarted with the middleware's flag; a denied one gets a response.
func decide(resp *ai.ModelResponse, issue *ai.ToolAction[task.IssueArgs,
	string], env *task.Env) ([]ai.GenerateOption, error) {
	var restarts, responses []*ai.Part
	for _, p := range resp.Interrupts() {
		a, err := issueArgs(p)
		if err != nil {
			return nil, err
		}
		if !env.Decide(a) {
			r, err := issue.RespondWith(p, task.Denied)
			if err != nil {
				return nil, fmt.Errorf("deny: %w", err)
			}
			responses = append(responses, r)
			continue
		}
		r, err := issue.RestartWith(p,
			ai.WithResumedMetadata[task.IssueArgs](approved))
		if err != nil {
			return nil, fmt.Errorf("approve: %w", err)
		}
		restarts = append(restarts, r)
	}
	return []ai.GenerateOption{ai.WithToolRestarts(restarts...),
		ai.WithToolResponses(responses...)}, nil
}

// end::decide[]

// issueArgs decodes an interrupted call's input, which Genkit holds as
// any, into issue_file's arguments.
func issueArgs(p *ai.Part) (task.IssueArgs, error) {
	var a task.IssueArgs
	b, err := json.Marshal(p.ToolRequest.Input)
	if err != nil {
		return a, fmt.Errorf("encode tool input: %w", err)
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("decode tool input: %w", err)
	}
	return a, nil
}

// size is the history's size as JSON, what a caller keeps to resume.
func size(msgs []*ai.Message) (int, error) {
	b, err := json.Marshal(msgs)
	if err != nil {
		return 0, fmt.Errorf("measure history: %w", err)
	}
	return len(b), nil
}
