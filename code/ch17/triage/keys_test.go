package triage

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// TestIssueKeys checks that with Keys, a repeated issue_file or
// issue_comment with the same idempotency key files and comments once,
// and that both tools say they send keys; without Keys, neither does,
// and a repeat comments twice, while the second filing is refused,
// since an issue for the fingerprint is open. Neither is Idempotent: a
// new call has a new key.
func TestIssueKeys(t *testing.T) {
	for _, keys := range []bool{true, false} {
		issues, err := tracker.Open("", time.Now)
		if err != nil {
			t.Fatal(err)
		}
		tools := map[string]agent.Tool{}
		for _, tl := range Tools(&Env{Issues: issues, Keys: keys}) {
			tools[tl.Def.Name] = tl
		}
		for _, name := range []string{"issue_file", "issue_comment"} {
			if tools[name].Keyed != keys || tools[name].Idempotent {
				t.Errorf("keys %v: %s Keyed %v, Idempotent %v", keys,
					name, tools[name].Keyed, tools[name].Idempotent)
			}
		}
		file := jsontext.Value(`{"title":"Panic","body":"b",` +
			`"fingerprint":"fp1"}`)
		comment := jsontext.Value(`{"id":1,"body":"again"}`)
		for range 2 { // the same call, run twice: a retry
			ctx := agent.WithKey(context.Background(),
				agent.Key("run-1", "c1"))
			_, err := tools["issue_file"].Run(ctx, file)
			if err != nil && !strings.Contains(err.Error(),
				"already filed") {
				t.Fatal(err)
			}
			ctx = agent.WithKey(context.Background(),
				agent.Key("run-1", "c2"))
			if _, err := tools["issue_comment"].Run(ctx,
				comment); err != nil {
				t.Fatal(err)
			}
		}
		all := issues.Search(tracker.Query{})
		want := map[bool]int{true: 1, false: 2}[keys]
		if len(all) != 1 || len(all[0].Comments) != want {
			t.Errorf("keys %v: %d issues, %d comments on #1; want 1, %d",
				keys, len(all), len(all[0].Comments), want)
		}
	}
}

// TestRefileRefused checks the model's path to a duplicate that keys
// can't close: a new call, with a new key, filing the same fingerprint
// again after the first call failed after taking effect.
func TestRefileRefused(t *testing.T) {
	issues, err := tracker.Open("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var file agent.Tool
	for _, tl := range Tools(&Env{Issues: issues, Keys: true}) {
		if tl.Def.Name == "issue_file" {
			file = tl
		}
	}
	args := jsontext.Value(`{"title":"Panic","body":"b","fingerprint":"fp1"}`)
	for i, call := range []string{"c1", "c2"} {
		ctx := agent.WithKey(context.Background(), agent.Key("run-1", call))
		_, err := file.Run(ctx, args)
		if (err != nil) != (i == 1) {
			t.Errorf("filing %d: %v", i+1, err)
		}
	}
	if n := len(issues.Search(tracker.Query{})); n != 1 {
		t.Errorf("%d issues, want 1", n)
	}
}

// TestPatchedLookup checks code_patch's Lookup against the workspace:
// the change found, not found, and a case it can't tell.
func TestPatchedLookup(t *testing.T) {
	e, _ := newTestEnv(t, "m1-done-after-delete")
	tests := []struct {
		args  string
		found bool
		err   bool
	}{
		{`{"path":"store/store.go","old":"\t\tr = nil","new":"X"}`, false, false},
		{`{"path":"store/new_test.go","old":"","new":"package store"}`, false, false},
		{`{"path":"store/store.go","old":"zzz","new":"package store"}`, true, false},
		{`{"path":"store/store.go","old":"package","new":"package store"}`, false, true},
	}
	for _, tt := range tests {
		_, found, err := e.patched(context.Background(),
			agent.ToolCall{Arguments: jsontext.Value(tt.args)}, "")
		if found != tt.found || (err != nil) != tt.err {
			t.Errorf("%s: found %v, %v", tt.args, found, err)
		}
	}
}

// TestKeepToolState checks that a restarted run's tools remember what
// the first process's did: a file read, a note, and the gate's refusal.
func TestKeepToolState(t *testing.T) {
	e, _ := newTestEnv(t, "m1-done-after-delete")
	path := filepath.Join(t.TempDir(), "env.json")
	if err := e.Keep(path); err != nil {
		t.Fatal(err)
	}
	if err := e.markRead("store/store.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.addNote("k1", "fp1", "cause is store.Get"); err != nil {
		t.Fatal(err)
	}
	if err := e.gated("fp1", "refused: passes on the original"); err != nil {
		t.Fatal(err)
	}
	e2, _ := newTestEnv(t, "m1-done-after-delete")
	if err := e2.Keep(path); err != nil {
		t.Fatal(err)
	}
	if !e2.hasRead("store/store.go") || !e2.refusedGroup("fp1") ||
		!strings.Contains(e2.Notes("fp1"), "cause is store.Get") {
		t.Errorf("restarted: read %v, refused %v, notes %q",
			e2.hasRead("store/store.go"), e2.refusedGroup("fp1"),
			e2.Notes("fp1"))
	}
	if n, _ := e2.addNote("k1", "fp1", "cause is store.Get"); n != 1 {
		t.Errorf("the same call's note was added again: %d notes", n)
	}
}

// TestBatchFilingOrders checks the batch, where two fingerprints each
// cover two causes: filing one issue per cause, in either order, makes
// one issue per new group, as the grader's "one issue per group" check
// counts them, and filing a cause again is still refused.
func TestBatchFilingOrders(t *testing.T) {
	tasks, err := catalog.Load(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := catalog.Batch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(batch.Reports)
	if err != nil {
		t.Fatal(err)
	}
	var reports []Report
	if err := json.Unmarshal(b, &reports); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Report{}
	for _, r := range reports {
		byID[r.ID] = r
	}
	type cause struct{ group, fp string }
	var causes []cause
	shared := map[string]int{}
	for g, gr := range batch.Groups {
		if gr.Status == "duplicate" {
			continue
		}
		fp := Fingerprint(byID[batch.Expected[g][0]])
		causes = append(causes, cause{g, fp})
		shared[fp]++
	}
	slices.SortFunc(causes, func(a, b cause) int {
		return strings.Compare(a.group, b.group)
	})
	if n := len(slices.Collect(maps.Values(shared))); n == len(causes) {
		t.Fatal("no fingerprint covers two causes; the test proves nothing")
	}
	reversed := slices.Clone(causes)
	slices.Reverse(reversed)
	for _, order := range [][]cause{causes, reversed} {
		issues, err := tracker.Open("", time.Now)
		if err != nil {
			t.Fatal(err)
		}
		e := &Env{Reports: reports, Issues: issues, Keys: true}
		var file agent.Tool
		for _, tl := range Tools(e) {
			if tl.Def.Name == "issue_file" {
				file = tl
			}
		}
		for i, c := range order {
			args, err := json.Marshal(map[string]string{
				"title": "Cause of " + c.group, "body": "b",
				"fingerprint": c.fp})
			if err != nil {
				t.Fatal(err)
			}
			ctx := agent.WithKey(context.Background(),
				agent.Key("run-1", fmt.Sprint("c", i)))
			if _, err := file.Run(ctx, args); err != nil {
				t.Errorf("filing %s: %v", c.group, err)
			}
		}
		if n := len(issues.Search(tracker.Query{})); n != len(causes) {
			t.Errorf("%d issues for %d new groups", n, len(causes))
		}
		again, err := json.Marshal(map[string]string{
			"title": "Cause of " + order[0].group, "body": "b",
			"fingerprint": strings.ToUpper(order[0].fp) + " "})
		if err != nil {
			t.Fatal(err)
		}
		ctx := agent.WithKey(context.Background(), agent.Key("run-1", "x"))
		if _, err := file.Run(ctx, again); err == nil {
			t.Error("the same cause, refiled with its fingerprint " +
				"spelled differently, was filed")
		}
	}
}
