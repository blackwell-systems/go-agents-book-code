package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/durable"
	"github.com/blackwell-systems/go-agents-book/code/ch17/service"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

// groupModel is a scripted model for one crash group's run: it lists
// the crashes, files an issue naming one root cause that every group
// shares, and submits what the tracker said: needs_human with the
// issue it filed, or duplicate of the issue the cause record named.
type groupModel struct {
	mu    sync.Mutex
	calls int
}

var (
	reportIDs  = regexp.MustCompile(`\b(?:cr_[0-9a-f]+|crash-\d{6}-[0-9a-f]{8})\b`)
	fingerID   = regexp.MustCompile(`\bfp_[0-9a-f]{6}\b`)
	issueNamed = regexp.MustCompile(`#(\d+)`)
)

func (m *groupModel) Generate(_ context.Context,
	req agent.Request) (agent.Reply, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	turn, last := 0, ""
	for _, msg := range req.Messages {
		switch msg.Role {
		case "assistant":
			turn++
		case "tool":
			last = msg.Content
		}
	}
	reply := func(msg agent.Message) (agent.Reply, error) {
		return agent.Reply{Message: msg, FinishReason: "stop",
			Usage: agent.Usage{Cost: 0.001}}, nil
	}
	call := func(name, args string) (agent.Reply, error) {
		return reply(agent.Calls(agent.ToolCall{ID: fmt.Sprintf("c%d",
			turn), Name: name, Arguments: jsontext.Value(args)}))
	}
	cause := `{"file":"api/api.go","function":"(*API).Routes"}`
	switch turn {
	case 0:
		return call("crash_list", `{}`)
	case 1:
		fp := fingerID.FindString(last)
		return call("issue_file", `{"title":"t","body":"b","fingerprint":"`+
			fp+`","root_cause":`+cause+`}`)
	case 2:
		var list string
		for _, msg := range req.Messages {
			if msg.Role == "tool" && list == "" {
				list = msg.Content // crash_list's answer
			}
		}
		fp := fingerID.FindString(list)
		reps, _ := jsonList(reportIDs.FindAllString(list, -1))
		n := issueNamed.FindStringSubmatch(last)
		status, field := "needs_human", "issue"
		if !strings.HasPrefix(last, "filed #") {
			status, field = "duplicate", "duplicate"
		}
		return call("submit_diagnosis", `{"groups":[{"fingerprint":"`+fp+
			`","reports":`+reps+`,"category":"other","root_cause":`+cause+
			`,"evidence":"e","status":"`+status+`","`+field+`":`+n[1]+`}]}`)
	}
	return reply(agent.Says("done"))
}

func jsonList(ids []string) (string, error) {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return `["` + strings.Join(ids, `","`) + `"]`, nil
}

type streamedFake struct{ *groupModel }

func (f streamedFake) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return agent.Streamed(f.groupModel).Stream(ctx, req)
}

// TestCatalogCommand runs the command on g3-mixed-batch's five groups
// with a scripted model whose groups all name one root cause: the
// first files it, the cause record turns the others into duplicates of
// that issue, every run is checkpointed with its workspace, and the
// report and the merge run. A reviewer accepts every diagnosis, and
// the result carries the reviews for the eval's record.
func TestCatalogCommand(t *testing.T) {
	t.Parallel()
	m := &groupModel{}
	var out bytes.Buffer
	state := t.TempDir()
	getenv := func(string) string { return "" }
	c := config{task: "g3-mixed-batch", repo: "..", context: "none",
		parallel: 2, maxTurns: 6, maxCost: 0.5, state: state,
		approve: "later", review: true}
	ms := models{main: streamedFake{m}, review: verdictModel{"stop"}}
	res, err := catalogRun(t.Context(), getenv, &out, c, ms,
		commandApprover(c.approve, &out), "group-")
	if err != nil {
		t.Fatalf("runCatalog: %v\n%s", err, out.String())
	}
	checkNotes(t, res, false)
	text := out.String()
	filed := strings.Count(text, "needs_human ")
	dups := strings.Count(text, "duplicate ")
	if filed != 1 || dups != 4 {
		t.Errorf("%d groups filed, %d duplicates; want 1 and 4:\n%s",
			filed, dups, text)
	}
	if !strings.Contains(text, "graded against the catalog") ||
		!strings.Contains(text, "1 diagnoses;") {
		t.Errorf("no grade, or the duplicates weren't joined:\n%s", text)
	}
	// Every run saved its workspace, which a resume checks.
	runs, err := service.New(context.Background(), service.Config{
		Dir: state + "/service", Secret: randomSecret(),
		Triager: noTriager{}})
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(context.Background()) //nolint:errcheck // test end
	for _, r := range runs.Runs() {
		cp, err := durable.Open(state+"/checkpoints", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		st, err := cp.Load()
		cp.Close()
		if err != nil || st.Phase != agent.PhaseDone ||
			!strings.HasSuffix(st.Workspace, r.ID+".work/remind") {
			t.Errorf("run %s: phase %s, workspace %q, %v", r.ID, st.Phase,
				st.Workspace, err)
		}
	}

	// Started again on the same state, nothing runs twice.
	calls := m.calls
	out.Reset()
	if err := runCatalog(t.Context(), getenv, &out, c, ms); err != nil {
		t.Fatalf("again: %v\n%s", err, out.String())
	}
	if m.calls != calls {
		t.Errorf("a second start made %d model calls", m.calls-calls)
	}
}

type noTriager struct{}

func (noTriager) Triage(context.Context, service.Run, []triage.Report,
	agent.Approver, float64) (service.Outcome, error) {
	return service.Outcome{}, nil
}

func TestOnlyTasks(t *testing.T) {
	set := []string{"d2-clock-no-minutes", "h1-paging-commit"}
	if got, err := onlyTasks(set, []string{"h1-paging-commit"}); err != nil ||
		len(got) != 1 {
		t.Errorf("onlyTasks = %v, %v", got, err)
	}
	if _, err := onlyTasks(set, []string{"h1-paging-comit"}); err == nil {
		t.Error("a task not in the set was accepted")
	}
}

// TestBatchCap: -max-cost is the whole batch's. Five groups whose runs
// each cost about $0.003, under a $0.005 cap, stop when the batch has
// spent it, overshooting by at most one call per run at once.
func TestBatchCap(t *testing.T) {
	t.Parallel()
	m := &groupModel{}
	var out bytes.Buffer
	c := config{task: "g3-mixed-batch", repo: "..", context: "none",
		parallel: 2, maxTurns: 6, maxCost: 0.005, state: t.TempDir(),
		approve: "later"}
	err := runCatalog(t.Context(), func(string) string { return "" },
		&out, c, models{main: streamedFake{m}})
	if err != nil {
		t.Fatalf("runCatalog: %v\n%s", err, out.String())
	}
	m.mu.Lock()
	spent := 0.001 * float64(m.calls)
	m.mu.Unlock()
	if spent > 0.005+2*0.001+1e-9 {
		t.Errorf("the batch spent $%.3f against a $0.005 cap:\n%s", spent,
			out.String())
	}
	if spent < 0.004 {
		t.Errorf("the batch spent only $%.3f; the cap stopped it early", spent)
	}
}

// TestCatalogDefended runs the command with every chapter 14 defense
// on each group's run: a sandbox, the gate with the eval's scripted
// approver, and the egress check. One cause is still filed once. The
// reviewer runs out of tokens every time, which lets each diagnosis
// through and is counted in the result's review errors.
func TestCatalogDefended(t *testing.T) {
	t.Parallel()
	m := &groupModel{}
	var out bytes.Buffer
	c := config{task: "g3-mixed-batch", repo: "..", context: "none",
		parallel: 2, maxTurns: 6, maxCost: 0.5, state: t.TempDir(),
		approve: "files", defenses: "all", minRung: 2, review: true}
	ms := models{main: streamedFake{m}, review: verdictModel{"length"}}
	res, err := catalogRun(t.Context(), func(string) string { return "" },
		&out, c, ms, commandApprover(c.approve, &out), "group-")
	if err != nil {
		t.Fatalf("runCatalog: %v\n%s", err, out.String())
	}
	checkNotes(t, res, true)
	text := out.String()
	if strings.Count(text, "needs_human ") != 1 ||
		strings.Count(text, "duplicate ") != 4 {
		t.Errorf("want 1 filed and 4 duplicates:\n%s", text)
	}
	if !strings.Contains(text, "defenses on") {
		t.Errorf("no sign the defenses were on:\n%s", text)
	}
}

// TestStoppedBatchCost: a batch stopped partway reports what its runs
// spent, so the eval's record of it isn't $0.
func TestStoppedBatchCost(t *testing.T) {
	t.Parallel()
	m := &groupModel{}
	ctx, cancel := context.WithCancel(t.Context())
	c := config{task: "g3-mixed-batch", repo: "..", context: "none",
		parallel: 2, maxTurns: 6, maxCost: 0.5, state: t.TempDir(),
		approve: "later"}
	go func() {
		for {
			m.mu.Lock()
			n := m.calls
			m.mu.Unlock()
			if n >= 4 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res, err := catalogRun(ctx, func(string) string { return "" },
		&bytes.Buffer{}, c, models{main: streamedFake{m}}, nil, "group-")
	if err == nil {
		t.Fatal("the batch wasn't stopped")
	}
	if res.Cost == 0 {
		t.Errorf("a stopped batch reported $0 after %d calls", m.calls)
	}
}

// checkNotes checks that a batch's result carries what the eval
// records for a single task too: the runs' prompt hash and every
// review, with the failed ones counted, so a batch record isn't
// missing its reviews.
func checkNotes(t *testing.T, res batchResult, failed bool) {
	t.Helper()
	if res.PromptHash == "" || strings.Contains(res.PromptHash, ",") {
		t.Errorf("prompt hash %q, want one", res.PromptHash)
	}
	if len(res.Review) == 0 {
		t.Fatal("no reviews recorded")
	}
	want := 0
	if failed {
		want = len(res.Review)
	}
	if res.ReviewErrors != want {
		t.Errorf("%d of %d reviews failed, want %d", res.ReviewErrors,
			len(res.Review), want)
	}
}

// verdictModel is a reviewer that accepts every diagnosis, or, with
// finish "length", runs out of tokens before it answers.
type verdictModel struct{ finish string }

func (v verdictModel) Generate(context.Context, agent.Request) (agent.Reply,
	error) {
	return agent.Reply{Message: agent.Says(`{"verdict":"accept",` +
		`"group":"","problem":"none","reasons":"fine"}`),
		FinishReason: v.finish}, nil
}

func (v verdictModel) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return agent.Streamed(v).Stream(ctx, req)
}
