// Package harness runs chapter 16's task on the book's own agent
// package, as chapter 14 left it: the loop, the client, and the
// approval gate that pauses a run for a person.
package harness

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch14/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch14/safety"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// tag::tools[]

// tools are the task's three tools as the agent package's Tool.
// issue_file stays a plain function; its Approval says why the gate
// must ask a person first.
func tools(env *task.Env) []agent.Tool {
	issue := agent.NewTool("issue_file", task.IssueDoc, env.FileIssue)
	issue.Approval = func(agent.ToolCall, []string) (string, error) {
		return "files an issue in the tracker", nil
	}
	submit := agent.NewTool("submit_diagnosis", task.SubmitDoc,
		env.Submit)
	submit.Final = true
	return []agent.Tool{
		agent.NewTool("run_tests", task.TestsDoc, env.RunTests),
		issue,
		submit,
	}
}

// end::tools[]

// tag::run[]

// Run diagnoses the crash group. When the run pauses for a person, Run
// asks env for each decision and runs the conversation again.
func Run(ctx context.Context, c task.Config, env *task.Env) (
	task.Outcome, error) {
	var out task.Outcome
	client := &openrouter.Client{BaseURL: c.BaseURL, APIKey: c.APIKey,
		Model: task.Model, HTTP: c.HTTP}
	m := agent.WrapModel(agent.Streamed(client),
		agent.Retrying(agent.Policy{}))
	gate := &safety.Gate{} // one gate for the run and its resumptions
	ts := agent.WrapTools(tools(env), gate.Wrap)
	msgs := []agent.Message{
		{Role: "system", Content: task.Prompt},
		{Role: "user", Content: task.Crash},
	}
	lim := agent.Limits{MaxTurns: 10}
	res, err := agent.RunStream(ctx, m, ts, msgs, lim)
	for paused(err) {
		out.Pauses++
		if out.Saved, err = size(res); err != nil { // <1>
			return out, err
		}
		lim.Approver, lim.Before = person(env), &res
		res, err = agent.RunStream(ctx, m, ts, res.Messages, lim) // <2>
	}
	if err != nil {
		return out, fmt.Errorf("diagnose: %w", err)
	}
	return out, nil
}

// end::run[]

// paused reports whether a run stopped to wait for a person.
func paused(err error) bool {
	_, ok := errors.AsType[*agent.ApprovalPending](err)
	return ok
}

// tag::person[]

// person is the Approver that a resumed run asks: env's decision, for
// the request whose hash the gate showed.
func person(env *task.Env) agent.Approver {
	return agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		var a task.IssueArgs
		if err := json.Unmarshal(r.Call.Arguments, &a); err != nil {
			return agent.ApprovalDecision{}, fmt.Errorf(
				"decode %s's arguments: %w", r.Call.Name, err)
		}
		d := agent.ApprovalDecision{Approved: env.Decide(a),
			Hash: r.Hash, By: "person"}
		if !d.Approved {
			d.Reason = task.Denied
		}
		return d, nil
	})
}

// end::person[]

// size is what a caller keeps to resume a paused run: the conversation,
// whose last reply holds the waiting calls, as JSON.
func size(res agent.Result) (int, error) {
	b, err := json.Marshal(res.Messages)
	if err != nil {
		return 0, fmt.Errorf("measure conversation: %w", err)
	}
	return len(b), nil
}
