package safety

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// tools returns a read-only tool, a tool that writes a file, and an
// issue tool, all recording their calls in ran, with the policy and the
// gate around them as the program puts them.
func tools(t *testing.T, dir string, g *Gate, ran *[]string) []agent.Tool {
	t.Helper()
	type pathArgs struct {
		Path string `json:"path"`
	}
	read := agent.NewTool("code_read", "Read a file.",
		func(_ context.Context, a pathArgs) (string, error) {
			*ran = append(*ran, "code_read "+a.Path)
			return "package auth", nil
		})
	read.ReadOnly = true
	write := agent.NewTool("file_write", "Write a file.",
		func(_ context.Context, a pathArgs) (string, error) {
			*ran = append(*ran, "file_write "+a.Path)
			return "written", nil
		})
	write.Targets = func(args jsontext.Value) ([]string, error) {
		var a pathArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("targets: %w", err)
		}
		return []string{a.Path}, nil
	}
	issue := agent.NewTool("issue_file", "File an issue.",
		func(_ context.Context, _ struct {
			Body string `json:"body"`
		}) (string, error) {
			*ran = append(*ran, "issue_file")
			return "filed #1", nil
		})
	ts := agent.WrapTools([]agent.Tool{read, write, issue},
		DefaultPolicy(remindProtected).Apply(dir))
	return agent.WrapTools(ts, g.Wrap)
}

// TestPauseResume runs a reply whose second call needs a person: the
// run pauses with the call and the one after it pending, and resumes
// from its conversation once a decision can arrive. A denial on resume
// reaches the model, and sticks to the file under another spelling.
func TestPauseResume(t *testing.T) {
	dir := workspace(t)
	var ran []string
	g := &Gate{Dir: dir}
	ts := tools(t, dir, g, &ran)
	m := &agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("c1", "code_read", `{"path":"auth/auth.go"}`),
			agent.Call("c2", "file_write", `{"path":"auth/auth.go"}`),
			agent.Call("c3", "issue_file", `{"body":"a crash"}`)),
		agent.Calls(agent.Call("c4", "file_write", `{"path":"AUTH/auth.go"}`)),
		agent.Says("done"),
	}}
	start := []agent.Message{{Role: "user", Content: "triage"}}
	res, err := agent.Run(t.Context(), m, ts, start,
		agent.Limits{MaxTurns: 5}) // no approver: the run pauses
	p, ok := errors.AsType[*agent.ApprovalPending](err)
	if !ok || len(p.Requests) != 1 || p.Requests[0].Call.ID != "c2" {
		t.Fatalf("first run: %v, want pending for c2", err)
	}
	ids := func(cs []agent.ToolCall) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	if got := ids(res.Pending); !slices.Equal(got, []string{"c2", "c3"}) {
		t.Errorf("pending %v, want [c2 c3]", got)
	}
	if !slices.Equal(ran, []string{"code_read auth/auth.go"}) {
		t.Errorf("before the pause, ran %v", ran)
	}

	var asked []string
	person := agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		asked = append(asked, r.Call.ID)
		d := agent.ApprovalDecision{Hash: r.Hash, By: "test"}
		d.Approved = r.Call.Name == "issue_file"
		d.Reason = "leave auth alone"
		return d, nil
	})
	res, err = agent.Run(t.Context(), m, ts, res.Messages,
		agent.Limits{MaxTurns: 5, Approver: person})
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !slices.Equal(asked, []string{"c2", "c3"}) { // c4: denied earlier
		t.Errorf("asked about %v, want [c2 c3]", asked)
	}
	if !slices.Equal(ran, []string{"code_read auth/auth.go", "issue_file"}) {
		t.Errorf("ran %v", ran)
	}
	answers := map[string]agent.Message{}
	for _, msg := range res.Messages {
		if msg.Role == "tool" {
			answers[msg.ToolCallID] = msg
		}
	}
	for id, want := range map[string]string{
		"c2": "denied: leave auth alone",
		"c4": "denied: auth/auth.go was denied earlier",
	} {
		a := answers[id]
		if !a.IsError || !strings.HasPrefix(strings.ToLower(a.Content),
			strings.ToLower(want)) {
			t.Errorf("%s answered %q, want %q", id, a.Content, want)
		}
	}
	if len(res.Pending) != 0 {
		t.Errorf("still pending: %v", ids(res.Pending))
	}
}

// TestGateWrap checks the middleware's other paths: a call that needs
// no decision runs without asking, and a target that takes a detour is
// refused before anyone is asked.
func TestGateWrap(t *testing.T) {
	dir := workspace(t)
	if err := os.Symlink("../auth", filepath.Join(dir, "store", "a")); err != nil {
		t.Fatal(err)
	}
	var ran []string
	ts := tools(t, dir, &Gate{Dir: dir}, &ran)
	never := agent.ApproverFunc(func(context.Context,
		agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		t.Error("asked a person")
		return agent.ApprovalDecision{}, nil
	})
	m := &agent.Fake{Script: []agent.Message{
		agent.Calls(agent.Call("c1", "file_write", `{"path":"store/store.go"}`),
			agent.Call("c2", "file_write", `{"path":"store/a/auth.go"}`),
			agent.Call("c3", "file_write", `{"path":"store/../auth/auth.go"}`)),
		agent.Says("done"),
	}}
	res, err := agent.Run(t.Context(), m, ts,
		[]agent.Message{{Role: "user", Content: "fix it"}},
		agent.Limits{MaxTurns: 3, Approver: never})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"file_write store/store.go"}) {
		t.Errorf("ran %v", ran)
	}
	for _, msg := range res.Messages[3:5] {
		if !msg.IsError || !strings.Contains(msg.Content, "denied") {
			t.Errorf("%s: %q, want a denial", msg.ToolCallID, msg.Content)
		}
	}
}
