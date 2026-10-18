package durable

// The crash sweep: a small triage run, crashed at every point where a
// crash can land, then restarted until it finishes. The tracker is the
// other side, so its records survive every crash, as a real service's
// would; everything else the process held is lost.

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

var update = flag.Bool("update", false, "rewrite testdata/sweep.txt")

// tag::crasher[]

// A crasher stops the process at its at'th crash point by panicking,
// the nearest a test can come to pulling the plug. Deferred calls still
// run, so a crashed life releases its lock, as the kernel would.
type crasher struct {
	at    int    // crash at this point; 0 means never
	on    string // or crash at the first point with this name
	n     int    // points passed so far
	where string // the point it crashed at
}

type crash struct{ at string }

func (c *crasher) point(name string) {
	c.n++
	if c.n == c.at || name == c.on {
		c.where = name
		panic(crash{name})
	}
}

// end::crasher[]

// tag::variants[]

// A variant is one way of making a run durable, from naive to the
// chapter's design.
type variant struct {
	name       string
	afterReply bool // save after the reply, and after each result
	key        bool // send idempotency keys; the tracker honors them
	marker     bool // save an attempt marker before side effects
}

var variants = []variant{
	{"save once per turn", false, false, false},
	{"save after the reply", true, false, false},
	{"+ idempotency keys", true, true, false},
	{"+ attempt markers, no keys", true, false, true},
	{"keys and markers", true, true, true},
}

// end::variants[]

// script is the run: a search and a filing, then a second filing and a
// comment on the first issue, then an answer.
var script = []agent.Message{
	{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "c1", Name: "issue_search", Arguments: jsontext.Value(`{}`)},
		{ID: "c2", Name: "issue_file",
			Arguments: jsontext.Value(`{"title":"Panic in store.Get"}`)},
	}},
	{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "c3", Name: "issue_file",
			Arguments: jsontext.Value(`{"title":"Deadlock in notify"}`)},
		{ID: "c4", Name: "issue_comment",
			Arguments: jsontext.Value(`{"id":1,"body":"Seen again."}`)},
	}},
	{Role: "assistant", Content: "Filed #1 and #2; commented on #1."},
}

const (
	runID      = "run-1"
	maxResumes = 10
)

// A sim is one scenario: a variant, its checkpoint directory, and the
// tracker, which outlives each process.
type sim struct {
	v      variant
	dir    string
	issues *tracker.Store
	calls  int // model calls, over every life
	asks   int // questions put to a person
}

// reply is a scripted model: it answers by how many replies the
// conversation already holds, so a resumed run gets the same reply.
func (s *sim) reply(msgs []agent.Message) agent.Message {
	s.calls++
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			n++
		}
	}
	return script[n]
}

func (s *sim) tools(c *crasher) map[string]agent.Tool {
	file := func(ctx context.Context, args jsontext.Value) (string,
		error) {
		var a struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("issue_file: %w", err)
		}
		key, _ := agent.KeyFrom(ctx) // "" without keys: a plain create
		is, _, err := s.issues.CreateOnce(key, a.Title, "", nil, "")
		if err != nil {
			return "", fmt.Errorf("issue_file: %w", err)
		}
		c.point("tracker accepted")
		return fmt.Sprintf("filed #%d", is.ID), nil
	}
	comment := func(ctx context.Context, args jsontext.Value) (string,
		error) {
		var a struct {
			ID   int    `json:"id"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("issue_comment: %w", err)
		}
		key, _ := agent.KeyFrom(ctx)
		if _, _, err := s.issues.CommentOnce(a.ID, key,
			a.Body); err != nil {
			return "", fmt.Errorf("issue_comment: %w", err)
		}
		c.point("tracker accepted")
		return fmt.Sprintf("commented on #%d", a.ID), nil
	}
	search := func(context.Context, jsontext.Value) (string, error) {
		return fmt.Sprintf("%d open issues", len(s.issues.Search(
			tracker.Query{Status: "open"}))), nil
	}
	return map[string]agent.Tool{
		"issue_search":  {ReadOnly: true, Run: search},
		"issue_file":    {Idempotent: s.v.key, Run: file},
		"issue_comment": {Idempotent: s.v.key, Run: comment},
	}
}

// person is the on-call engineer, scripted: asked whether a call took
// effect, they look in the tracker and answer truthfully.
func (s *sim) person() agent.Approver {
	return agent.ApproverFunc(func(_ context.Context,
		r agent.ApprovalRequest) (agent.ApprovalDecision, error) {
		s.asks++
		return agent.ApprovalDecision{Approved: !s.tookEffect(r.Call),
				Hash: r.Hash, By: "on-call", Reason: "it is in the tracker"},
			nil
	})
}

func (s *sim) tookEffect(call agent.ToolCall) bool {
	var a struct {
		Title string `json:"title"`
		ID    int    `json:"id"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(call.Arguments, &a); err != nil {
		return false
	}
	if call.Name == "issue_file" {
		return len(s.issues.Search(tracker.Query{Text: a.Title})) > 0
	}
	is, err := s.issues.Get(a.ID)
	if err != nil {
		return false
	}
	for _, c := range is.Comments {
		if c.Body == a.Body {
			return true
		}
	}
	return false
}

// tag::life[]

// life is one process: it opens the run, restores or starts it, and
// runs it until it is done or the crasher stops it.
func (s *sim) life(ctx context.Context, c *crasher) error {
	cp, err := Open(s.dir, runID)
	if err != nil {
		return err
	}
	defer cp.Close()
	cp.noSync, cp.crash = true, c.point
	st, err := cp.Restore(ctx, maxResumes)
	if errors.Is(err, ErrNoCheckpoint) {
		st = agent.NewState(runID, []agent.Message{
			{Role: "user", Content: "Triage the new crash reports."}})
		err = cp.Save(ctx, st)
	}
	if err != nil {
		return err
	}
	tools := s.tools(c)
	for st.Phase != agent.PhaseDone {
		if err := s.step(ctx, cp, &st, tools, c); err != nil {
			return err
		}
	}
	return nil
}

// end::life[]

// tag::step[]

// step does what the state's phase says comes next, saving where the
// variant saves.
func (s *sim) step(ctx context.Context, cp *Checkpoint, st *agent.State,
	tools map[string]agent.Tool, c *crasher) error {
	switch st.Phase {
	case agent.PhaseModel:
		m := s.reply(st.Messages)
		c.point("reply received")
		st.Messages = append(st.Messages, m)
		st.Turns++
		st.Phase = agent.PhaseTools
		if len(m.ToolCalls) == 0 {
			st.Phase = agent.PhaseDone
			return cp.Save(ctx, *st)
		}
		if !s.v.afterReply {
			return nil // the naive loop saves at the end of the turn
		}
		return cp.Save(ctx, *st) // the calls and their IDs are fixed
		// end::step[]
		// tag::step-tools[]
	case agent.PhaseTools:
		for _, call := range st.Unanswered() {
			msg, err := s.call(ctx, cp, st, tools[call.Name], call, c)
			if err != nil {
				return err
			}
			st.Answer(msg)
			if s.v.afterReply {
				if err := cp.Save(ctx, *st); err != nil {
					return err
				}
				c.point("result saved")
			}
		}
		st.Phase = agent.PhaseModel
		return cp.Save(ctx, *st)
	}
	return fmt.Errorf("phase %q", st.Phase)
}

// end::step-tools[]

// tag::call[]

// call runs one tool call, or settles it on the resolution ladder if a
// marker says an earlier process started it.
func (s *sim) call(ctx context.Context, cp *Checkpoint, st *agent.State,
	t agent.Tool, call agent.ToolCall, c *crasher) (agent.Message,
	error) {
	key := agent.Key(st.RunID, call.ID)
	if a, ok := st.Attempt(call.ID); ok { // <1>
		res, err := agent.Resolve(ctx, t, st.RunID, call, a, s.person())
		if err != nil || !res.Run {
			return res.Answer, err //nolint:wrapcheck // as is
		}
	} else if s.v.marker && !t.ReadOnly { // <2>
		st.Attempts = append(st.Attempts, agent.Attempt{CallID: call.ID,
			Tool: call.Name, Key: key, Started: time.Now().UTC()})
		if err := cp.Save(ctx, *st); err != nil {
			return agent.Message{}, err
		}
		c.point("marker saved")
	}
	if s.v.key {
		ctx = agent.WithKey(ctx, key) // <3>
	}
	out, err := t.Run(ctx, call.Arguments)
	c.point("tool returned")
	msg := agent.Message{Role: "tool", ToolCallID: call.ID,
		Content: out}
	if err != nil {
		msg.IsError, msg.Content = true, err.Error()
	}
	return msg, nil
}

// end::call[]

// survive runs one life and reports whether it crashed.
func (s *sim) survive(ctx context.Context, c *crasher) (crashed bool,
	err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	return false, s.life(ctx, c)
}

// An outcome is what one scenario left in the tracker.
type outcome struct {
	where      string // the point the first crash hit
	again      bool   // whether the restart crashed too
	duplicates int    // issues or comments made more than once
	missing    int    // ones never made
	asks       int
	extraCalls int // model calls beyond the script's three

	messages []agent.Message // the finished run's conversation
}

// scenario crashes the first life at point first and the second at
// point second (0: no crash), then restarts until the run is done.
func scenario(t *testing.T, v variant, dir string, first,
	second int) outcome {
	t.Helper()
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{v: v, dir: dir, issues: issues}
	var o outcome
	for life := range maxResumes {
		c := &crasher{}
		switch life {
		case 0:
			c.at = first
		case 1:
			c.at = second
		}
		crashed, err := s.survive(t.Context(), c)
		if err != nil {
			t.Fatalf("%s, crashes at %d and %d: life %d: %v", v.name,
				first, second, life, err)
		}
		if !crashed {
			break
		}
		if life == 0 {
			o.where = c.where
		}
		o.again = o.again || life == 1
	}
	o.asks, o.extraCalls = s.asks, s.calls-len(script)
	o.duplicates, o.missing = count(s.issues)
	return o
}

// count compares the tracker with what the script makes: two issues,
// and one comment on the first.
func count(issues *tracker.Store) (dup, missing int) {
	want := map[string]int{"Panic in store.Get": 1,
		"Deadlock in notify": 1, "comment": 1}
	got := map[string]int{}
	for _, is := range issues.Search(tracker.Query{}) {
		got[is.Title]++
		got["comment"] += len(is.Comments)
	}
	for k, n := range want {
		dup += max(got[k]-n, 0)
		missing += max(n-got[k], 0)
	}
	return dup, missing
}

// TestCrashSweep crashes each variant at every point, once, and again
// at every point of the restart, and counts what the tracker shows.
func TestCrashSweep(t *testing.T) {
	base := t.TempDir()
	byPoint := map[string][]int{} // single crashes with duplicates
	var totals strings.Builder
	for vi, v := range variants {
		var all outcome
		single, double, withDups := 0, 0, 0
		points := pointsIn(t, v, filepath.Join(base, "dry"+v.name))
		for first := 1; first <= points; first++ {
			for second := 0; ; second++ {
				o := scenario(t, v,
					fresh(t, filepath.Join(base, "run")), first, second)
				if second > 0 && !o.again {
					break // the restart passed fewer points than this
				}
				if second == 0 {
					single++
					if byPoint[o.where] == nil {
						byPoint[o.where] = make([]int, len(variants))
					}
					if o.duplicates > 0 {
						byPoint[o.where][vi]++
					}
				} else {
					double++
				}
				if o.duplicates > 0 {
					withDups++
				}
				all.duplicates += o.duplicates
				all.missing += o.missing
				all.asks += o.asks
				all.extraCalls += o.extraCalls
			}
		}
		fmt.Fprintf(&totals, "%-28s %6d %6d %9d %5d %7d %5d %6d\n",
			v.name, single, double, withDups, all.duplicates,
			all.missing, all.asks, all.extraCalls)
		check(t, v, all, withDups)
	}
	golden(t, table(byPoint, totals.String()))
}

// pointOrder lists the crash points in the order one tool call meets
// them; the two inside a save come last, since every save has them.
var pointOrder = []string{"reply received", "marker saved",
	"tracker accepted", "tool returned", "result saved", "mid-save",
	"after rename"}

// table prints the sweep: first, for runs crashed once, how many of the
// crashes at each point left duplicates, per variant; then the totals
// over every scenario, crashed once or twice.
func table(byPoint map[string][]int, totals string) string {
	var b strings.Builder
	b.WriteString("Runs crashed once that left duplicates, by where " +
		"the crash landed\n\n")
	fmt.Fprintf(&b, "%-18s", "crash point")
	for i := range variants {
		fmt.Fprintf(&b, " %6s", fmt.Sprintf("v%d", i+1))
	}
	b.WriteString("\n")
	for _, p := range pointOrder {
		if byPoint[p] == nil {
			panic("no crash landed at " + p)
		}
		fmt.Fprintf(&b, "%-18s", p)
		for _, n := range byPoint[p] {
			fmt.Fprintf(&b, " %6d", n)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nAll scenarios\n\n")
	fmt.Fprintf(&b, "%-28s %6s %6s %9s %5s %7s %5s %6s\n", "variant",
		"once", "twice", "with dups", "dups", "missing", "asks",
		"calls+")
	b.WriteString(totals)
	b.WriteString("\n")
	for i, v := range variants {
		fmt.Fprintf(&b, "v%d: %s\n", i+1, v.name)
	}
	return b.String()
}

// check says what each variant must show.
func check(t *testing.T, v variant, o outcome, withDups int) {
	t.Helper()
	switch {
	case o.missing > 0:
		t.Errorf("%s: %d effects never happened", v.name, o.missing)
	case !v.key && !v.marker && withDups == 0:
		t.Errorf("%s: no duplicates; the sweep missed the window",
			v.name)
	case (v.key || v.marker) && o.duplicates > 0:
		t.Errorf("%s: %d duplicates", v.name, o.duplicates)
	case v.key && o.asks > 0:
		t.Errorf("%s: asked a person %d times", v.name, o.asks)
	case v.marker && !v.key && o.asks == 0:
		t.Errorf("%s: never asked a person", v.name)
	}
}

// pointsIn counts the crash points of an uncrashed run.
func pointsIn(t *testing.T, v variant, dir string) int {
	t.Helper()
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{v: v, dir: dir, issues: issues}
	c := &crasher{}
	if crashed, err := s.survive(t.Context(), c); crashed || err != nil {
		t.Fatalf("%s: uncrashed run: %v", v.name, err)
	}
	return c.n
}

func golden(t *testing.T, got string) {
	t.Helper()
	path := filepath.Join("testdata", "sweep.txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("sweep table changed; run with -update if that is "+
			"intended:\n%s", got)
	}
	t.Logf("\n%s", got)
}

// TestFinishedRun checks that restarting a finished run asks the model
// for nothing and runs no tool.
func TestFinishedRun(t *testing.T) {
	v := variants[len(variants)-1]
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{v: v, dir: t.TempDir(), issues: issues}
	for range 3 {
		if crashed, err := s.survive(t.Context(),
			&crasher{}); crashed || err != nil {
			t.Fatal(err)
		}
	}
	if s.calls != len(script) {
		t.Errorf("%d model calls over three starts, want %d", s.calls,
			len(script))
	}
	if dup, missing := count(issues); dup+missing > 0 {
		t.Errorf("%d duplicates, %d missing", dup, missing)
	}
}

// TestCrashLoop checks that a run that crashes on every start stops
// at the resume limit.
func TestCrashLoop(t *testing.T) {
	v := variants[len(variants)-1]
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{v: v, dir: t.TempDir(), issues: issues}
	for life := range maxResumes + 1 {
		crashed, err := s.survive(t.Context(), &crasher{on: "reply received"})
		if !crashed || err != nil {
			t.Fatalf("life %d: crashed %v, %v", life, crashed, err)
		}
	}
	_, err = s.survive(t.Context(), &crasher{on: "reply received"})
	if !errors.Is(err, ErrMaxResumes) {
		t.Errorf("after %d restarts: %v, want ErrMaxResumes",
			maxResumes, err)
	}
	if s.calls != maxResumes+1 {
		t.Errorf("%d model calls, want %d", s.calls, maxResumes+1)
	}
}

// fresh empties dir for the next scenario and returns it: one directory
// reused costs less than thousands of new ones.
func fresh(t *testing.T, dir string) string {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}
