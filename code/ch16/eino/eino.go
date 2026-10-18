// Package eino runs chapter 16's task on CloudWeGo's Eino: its ADK
// ChatModelAgent for the loop, its Runner for interrupts, and the
// OpenAI-compatible chat model from eino-ext.
package eino

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// The interrupt's info goes into a gob-encoded checkpoint, so its type
// has to be registered.
func init() { schema.RegisterName[task.IssueArgs]("ch16.IssueArgs") }

// tag::issue[]

// fileIssue is issue_file. The first time it runs it interrupts the
// run; when the run resumes, the person's decision arrives as resume
// data addressed to this call.
func fileIssue(env *task.Env) func(context.Context, task.IssueArgs) (
	string, error) {
	return func(ctx context.Context, a task.IssueArgs) (string, error) {
		if was, _, _ := tool.GetInterruptState[any](ctx); !was {
			return "", tool.Interrupt(ctx, a) // <1>
		}
		target, has, approved := tool.GetResumeContext[bool](ctx) // <2>
		if !target {
			return "", tool.Interrupt(ctx, a) // another call resumed
		}
		if !has || !approved {
			return task.Denied, nil
		}
		return env.FileIssue(ctx, a)
	}
}

// end::issue[]

// tag::tools[]

// tools are the task's three tools as Eino tools. InferTool builds each
// schema from the arguments' struct tags.
func tools(env *task.Env) ([]tool.BaseTool, error) {
	run, err := utils.InferTool("run_tests", task.TestsDoc,
		env.RunTests)
	if err != nil {
		return nil, fmt.Errorf("define run_tests: %w", err)
	}
	issue, err := utils.InferTool("issue_file", task.IssueDoc,
		fileIssue(env))
	if err != nil {
		return nil, fmt.Errorf("define issue_file: %w", err)
	}
	submit, err := utils.InferTool("submit_diagnosis", task.SubmitDoc,
		env.Submit)
	if err != nil {
		return nil, fmt.Errorf("define submit_diagnosis: %w", err)
	}
	return []tool.BaseTool{run, issue, submit}, nil
}

// end::tools[]

// tag::agent[]

// newAgent builds the agent: the model client, the tools, and the loop.
func newAgent(ctx context.Context, c task.Config, env *task.Env) (
	adk.Agent, error) {
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: c.BaseURL, APIKey: c.APIKey, HTTPClient: c.HTTP,
		Model: task.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	ts, err := tools(env)
	if err != nil {
		return nil, err
	}
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "triage",
		Instruction: task.Prompt,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{ // <1>
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: ts},
			ReturnDirectly:  map[string]bool{"submit_diagnosis": true},
		},
		MaxIterations: 10,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return a, nil
}

// end::agent[]

// tag::run[]

// Run diagnoses the crash group. When the run interrupts for a person,
// Run asks env for each decision and resumes from the checkpoint.
func Run(ctx context.Context, c task.Config, env *task.Env) (
	task.Outcome, error) {
	var out task.Outcome
	a, err := newAgent(ctx, c, env)
	if err != nil {
		return out, err
	}
	store := &memStore{}
	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: a,
		CheckPointStore: store}) // <1>
	const id = "run-7f3a"
	msgs := []adk.Message{schema.UserMessage(task.Crash)}
	events := r.Run(ctx, msgs, adk.WithCheckPointID(id))
	for {
		targets, err := drain(events, env)
		if err != nil || targets == nil {
			return out, err
		}
		out.Pauses++
		out.Saved = max(out.Saved, store.size(id))
		events, err = r.ResumeWithParams(ctx, id,
			&adk.ResumeParams{Targets: targets}) // <2>
		if err != nil {
			return out, fmt.Errorf("resume: %w", err)
		}
	}
}

// end::run[]

// tag::drain[]

// drain reads a run's events to the end. If the run interrupted, it
// returns the person's decision for each waiting call, keyed by the
// ID Eino gave the interrupt.
func drain(events *adk.AsyncIterator[*adk.AgentEvent], env *task.Env) (
	map[string]any, error) {
	var targets map[string]any
	for {
		ev, ok := events.Next()
		if !ok {
			if targets != nil && len(targets) == 0 {
				return nil, errNothingToDecide
			}
			return targets, nil
		}
		if ev.Err != nil {
			return nil, fmt.Errorf("run agent: %w", ev.Err)
		}
		if ev.Action == nil || ev.Action.Interrupted == nil {
			continue
		}
		targets = map[string]any{}
		for _, ic := range ev.Action.Interrupted.InterruptContexts {
			a, ok := ic.Info.(task.IssueArgs)
			if ok && ic.IsRootCause {
				targets[ic.ID] = env.Decide(a)
			}
		}
	}
}

// end::drain[]

var errNothingToDecide = errors.New("interrupted with nothing to decide")

// memStore is a CheckPointStore in memory; Eino ships the interface and
// leaves the storage to you.
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, id string) ([]byte, bool,
	error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[id]
	return b, ok, nil
}

func (s *memStore) Set(_ context.Context, id string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[id] = b
	return nil
}

func (s *memStore) size(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m[id])
}
