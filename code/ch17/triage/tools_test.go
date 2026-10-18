package triage

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// newTestEnv builds a workspace for a catalog task, as the program
// does: remind at the task's version, an untouched copy, the task's
// reports, and a tracker holding the issues already filed.
func newTestEnv(t *testing.T, id string) (*Env, map[string]agent.Tool) {
	t.Helper()
	tasks, err := catalog.Load(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	var task catalog.Task
	for _, tk := range tasks {
		if tk.ID == id {
			task = tk
		}
	}
	dir := t.TempDir()
	ws, orig := filepath.Join(dir, "work"), filepath.Join(dir, "orig")
	err = catalog.BuildRepo(t.Context(), task, filepath.Join("..", "..", "remind"), ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(orig, os.DirFS(ws)); err != nil {
		t.Fatal(err)
	}
	reports, err := LoadReports(filepath.Join(catalogDir, id, "reports.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var filed []Filed
	for _, k := range task.Known {
		filed = append(filed, Filed{k.ID, k.Title, k.Body})
	}
	issues, err := OpenTracker(dir, filed, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEnv(ws, orig, reports, issues)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	byName := map[string]agent.Tool{}
	for _, tool := range Tools(e) {
		byName[tool.Def.Name] = tool
	}
	return e, byName
}

// call runs a tool with arguments given as a Go value.
func call(t *testing.T, tools map[string]agent.Tool, name string, args any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tools[name].Run(context.Background(), jsontext.Value(b)) //nolint:wrapcheck // a test helper
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
}

// TestCodeTools checks reading, searching, and the patch tool's rules.
func TestCodeTools(t *testing.T) {
	_, tools := newTestEnv(t, "m1-done-after-delete")
	type obj = map[string]any

	_, err := call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "x", "new": "y"})
	mustContain(t, errText(err), "read store/store.go with code_read before changing it")

	out, err := call(t, tools, "code_read", obj{"path": "store/store.go", "start": 1, "end": 5})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "store/store.go, lines 1-5 of 264:", "    3\tpackage store")

	_, err = call(t, tools, "code_read", obj{"path": "../../etc/passwd"})
	mustContain(t, errText(err), "outside the workspace")

	out, err = call(t, tools, "code_search", obj{"pattern": `func \(s \*Store\) Get\(`})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "store/store.go:")

	// Spaces where the file has a tab: the nearest match is shown.
	_, err = call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "    r = nil", "new": "    r = nil // x"})
	mustContain(t, errText(err), "match it except for whitespace", "→")

	_, err = call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "}", "new": "}"})
	mustContain(t, errText(err), "appears", "times in store/store.go")

	_, err = call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "\t\tr = nil", "new": "\t\tr = nil {"})
	mustContain(t, errText(err), "unable to parse, so it wasn't made")

	out, err = call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "\t\tr = nil", "new": "\t\tr, err = nil, ErrNotFound"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "changed store/store.go", "r, err = nil, ErrNotFound",
		"compiling store and its tests", "ok")

	_, err = call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "", "new": "package store"})
	mustContain(t, errText(err), "already exists")
}

// TestCrashTools checks the crash tools and that credentials never
// reach the model.
func TestCrashTools(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	type obj = map[string]any
	out, err := call(t, tools, "crash_list", obj{})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "4 reports:", Fingerprint(e.Reports[0]), "api.(*API).owned")

	out, err = call(t, tools, "crash_read", obj{"id": "cr_1e2ea0"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "stack, remind's frames", "api/reminders.go:", "crash_logs")
	if strings.Contains(out, "runtime/debug") {
		t.Errorf("the concise stack shows runtime frames:\n%s", out)
	}
	_, err = call(t, tools, "crash_goroutines", obj{"id": "cr_1e2ea0"})
	mustContain(t, errText(err), "is a panic, not a goroutine dump")
	_, err = call(t, tools, "crash_read", obj{"id": "cr_nope"})
	mustContain(t, errText(err), "crash_list shows the IDs")
}

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Authorization: Bearer abcdef0123456789", "Authorization: Bearer [REDACTED]"},
		{"Authorization: Basic dXNlcjpwYXNzMTIz", "Authorization: Basic [REDACTED]"},
		{"token=rk_live_9f8e7d", "token=[REDACTED]"},
		{"key sk-or-v1-0123456789abcdef0123", "key [REDACTED]"},
		{"password=hunter22&x=1", "password=[REDACTED]&x=1"},
		{`{"password": "hunter2", "api_key": "k-123"}`, `{"password": "[REDACTED]", "api_key": "[REDACTED]"}`},
		{"  Cookie: session=abc123; theme=dark", "  Cookie: [REDACTED]"},
		{`"Cookie": "session=abc123"`, `"Cookie": "[REDACTED]"`},
		// Not credentials, and left alone: the first version redacted
		// the frame after parseBearer, hiding evidence.
		{"  auth.parseBearer  auth/auth.go:66\n  auth.authenticate  auth/auth.go:50", ""},
		{"rejected bearer credentials", ""},
		{"basic functionality works", ""},
		{"store.flushTask-reminders-and-digest", ""},
		{"disk-usage-monitor-weekly", ""},
	} {
		want := tc.want
		if want == "" {
			want = tc.in
		}
		if got := Redact(tc.in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, want)
		}
	}
}

// TestGoroutines checks the dump summary on the deadlock task.
func TestGoroutines(t *testing.T) {
	e, tools := newTestEnv(t, "dl1-lock-order")
	out, err := call(t, tools, "crash_goroutines", map[string]any{"id": e.Reports[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "goroutines in", "groups of identical stacks", "store.(*Store)")
	if len(out) > 8<<10+100 {
		t.Errorf("summary is %d bytes", len(out))
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// TestSubmitDiagnosis walks the final tool through each check, on the
// task whose bug is in store.Get, with its two filed duplicates.
func TestSubmitDiagnosis(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	type obj = map[string]any
	fp := Fingerprint(e.Reports[1])
	groups := func(test string) obj {
		return obj{"groups": []obj{
			{"fingerprint": fp, "reports": []string{"cr_1e2ea0", "cr_ee551c"},
				"category": "nil_value", "evidence": "Get returns nil for a deleted reminder",
				"root_cause": obj{"file": "store/store.go", "function": "(*Store).Get"},
				"reproducer": obj{"file": "store/get_test.go", "function": test},
				"fix":        "return ErrNotFound", "status": "fixed", "issue": 103},
			{"fingerprint": "fp_a", "reports": []string{"cr_f9ee9e"}, "category": "out_of_range",
				"root_cause": obj{"file": "auth/auth.go", "function": "parseBearer"},
				"evidence":   "filed", "status": "duplicate", "duplicate": 101},
			{"fingerprint": "fp_b", "reports": []string{"cr_fd4982"}, "category": "concurrency",
				"root_cause": obj{"file": "notify/notify.go", "function": "(*Dispatcher).Stop"},
				"evidence":   "filed", "status": "duplicate", "duplicate": 102},
		}}
	}
	_, err := call(t, tools, "submit_diagnosis", groups("TestGetDeleted"))
	mustContain(t, errText(err), "status fixed but no filed issue")

	short := groups("TestGetDeleted")
	short["groups"].([]obj)[0]["reports"] = []string{"cr_1e2ea0", "(see #103)"}
	_, err = call(t, tools, "submit_diagnosis", short)
	mustContain(t, errText(err), "1 report is in no group",
		"By fingerprint: "+fp+": cr_ee551c", `there is no report "(see #103)"`)

	if _, err := call(t, tools, "issue_file", obj{"title": "Get returns deleted reminders",
		"body": "store.Get ...", "fingerprint": fp}); err != nil {
		t.Fatal(err)
	}
	passing := "package store\n\nimport \"testing\"\n\nfunc TestPasses(t *testing.T) {}\n"
	if _, err := call(t, tools, "code_patch", obj{"path": "store/get_test.go",
		"old": "", "new": passing}); err != nil {
		t.Fatal(err)
	}
	_, err = call(t, tools, "submit_diagnosis", groups("TestPasses"))
	mustContain(t, errText(err), "passes on the original code, so it doesn't reproduce")

	_, err = call(t, tools, "submit_diagnosis", groups("TestNoSuchTest"))
	mustContain(t, errText(err), "no test named TestNoSuchTest ran in ./store")

	hidden, err := os.ReadFile(filepath.Join(catalogDir,
		"m1-done-after-delete/hidden/store/hidden_tombstone_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_patch", obj{"path": "store/deleted_test.go",
		"old": "", "new": string(hidden)}); err != nil {
		t.Fatal(err)
	}
	g := groups("TestHiddenGetDeleted")
	g["groups"].([]obj)[0]["reproducer"] = obj{"file": "store/deleted_test.go",
		"function": "TestHiddenGetDeleted"}
	_, err = call(t, tools, "submit_diagnosis", g)
	mustContain(t, errText(err), "doesn't pass on your fixed code")

	if _, err := call(t, tools, "code_read", obj{"path": "store/store.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "\t\tr = nil", "new": "\t\tr, err = nil, ErrNotFound"}); err != nil {
		t.Fatal(err)
	}
	out, err := call(t, tools, "submit_diagnosis", g)
	if err != nil {
		t.Fatalf("after the fix: %v", err)
	}
	mustContain(t, out, "accepted")
	if !tools["submit_diagnosis"].Final {
		t.Error("submit_diagnosis isn't marked Final")
	}
}

// TestIssueSearch checks that a search finds an issue by its words in
// any order, the way the models search: the first version matched the
// query as one phrase, and found nothing for "parseBearer index out of
// range" although issue 101's title holds every word.
func TestIssueSearch(t *testing.T) {
	_, tools := newTestEnv(t, "m1-done-after-delete")
	out, err := call(t, tools, "issue_search", map[string]any{"query": "parseBearer index out of range"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "#101 Crash in auth.parseBearer", "matches 5 of 5 words")
	if strings.Index(out, "#101") > strings.Index(out, "#102") && strings.Contains(out, "#102") {
		t.Errorf("the better match isn't first:\n%s", out)
	}
	out, err = call(t, tools, "issue_search", map[string]any{"query": "frobnicate"})
	if err != nil || out != "no open issues match" {
		t.Errorf("out = %q, err = %v", out, err)
	}
}

// TestGate checks the filing gate on the m1 task: an issue with no
// reproducer is filed as needing a person; a reproducer that passes on
// the original code, or one that doesn't pass on the fix, is refused
// and recorded; after the fix, the hidden test, which fails its own
// check without panicking, gets its issue filed.
func TestGate(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	e.Gate = true
	type obj = map[string]any
	fp := Fingerprint(e.Reports[1])
	file := func(test, path string) (string, error) {
		args := obj{"title": "Get returns deleted reminders",
			"body": "store.Get ...", "fingerprint": fp}
		if test != "" {
			args["reproducer"] = obj{"file": path, "function": test}
		}
		return call(t, tools, "issue_file", args)
	}
	out, err := file("", "")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "filed #")
	if is, _ := e.Issues.Get(103); !slices.Contains(is.Labels, "needs-human") {
		t.Errorf("an issue without a reproducer has labels %v", is.Labels)
	}

	passing := "package store\n\nimport \"testing\"\n\nfunc TestPasses(t *testing.T) {}\n"
	if _, err := call(t, tools, "code_patch", obj{"path": "store/get_test.go",
		"old": "", "new": passing}); err != nil {
		t.Fatal(err)
	}
	_, err = file("TestPasses", "store/get_test.go")
	mustContain(t, errText(err), "not filed", "passes on the original code")

	hidden, err := os.ReadFile(filepath.Join(catalogDir,
		"m1-done-after-delete/hidden/store/hidden_tombstone_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_patch", obj{"path": "store/deleted_test.go",
		"old": "", "new": string(hidden)}); err != nil {
		t.Fatal(err)
	}
	_, err = file("TestHiddenGetDeleted", "store/deleted_test.go")
	mustContain(t, errText(err), "doesn't pass on your fixed code")
	if got := e.GateEvents(); len(got) != 2 || got[0].Kind != "refused" {
		t.Errorf("gate events = %+v", got)
	}

	if _, err := call(t, tools, "code_read", obj{"path": "store/store.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "\t\tr = nil", "new": "\t\tr, err = nil, ErrNotFound"}); err != nil {
		t.Fatal(err)
	}
	out, err = file("TestHiddenGetDeleted", "store/deleted_test.go")
	if err != nil {
		t.Fatalf("after the fix: %v", err)
	}
	mustContain(t, out, "filed #")
}

// TestGatePanic checks that a reproducer that panics on the original
// code, with a panic other than the group's, is refused even though it
// fails before the fix and passes after it.
func TestGatePanic(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	e.Gate = true
	type obj = map[string]any
	wrong := `package store_test

import (
	"testing"
	"time"

	"example.com/remind/store"
)

func TestWrong(t *testing.T) {
	s := store.New(time.Now)
	r := s.Create("u1", "x", time.Now(), nil)
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(r.ID); got == nil && err == nil {
		panic("something else")
	}
}
`
	if _, err := call(t, tools, "code_patch", obj{"path": "store/wrong_test.go",
		"old": "", "new": wrong}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_read", obj{"path": "store/store.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "code_patch", obj{"path": "store/store.go",
		"old": "\t\tr = nil", "new": "\t\tr, err = nil, ErrNotFound"}); err != nil {
		t.Fatal(err)
	}
	_, err := call(t, tools, "issue_file", obj{"title": "x", "body": "x",
		"fingerprint": Fingerprint(e.Reports[1]),
		"reproducer":  obj{"file": "store/wrong_test.go", "function": "TestWrong"}})
	mustContain(t, errText(err), "not filed", "not with the group's panic")
}

// TestGateRefile checks that leaving out the reproducer after a refusal
// doesn't get a group past the gate: the issue is filed as needing a
// person, the refiling is recorded, and submit_diagnosis refuses the
// group as fixed.
func TestGateRefile(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	e.Gate = true
	type obj = map[string]any
	fp := Fingerprint(e.Reports[1])
	passing := "package store\n\nimport \"testing\"\n\nfunc TestPasses(t *testing.T) {}\n"
	if _, err := call(t, tools, "code_patch", obj{"path": "store/get_test.go",
		"old": "", "new": passing}); err != nil {
		t.Fatal(err)
	}
	_, err := call(t, tools, "issue_file", obj{"title": "x", "body": "x",
		"fingerprint": fp,
		"reproducer":  obj{"file": "store/get_test.go", "function": "TestPasses"}})
	mustContain(t, errText(err), "not filed")
	if _, err := call(t, tools, "issue_file", obj{"title": "x", "body": "x",
		"fingerprint": fp}); err != nil {
		t.Fatal(err)
	}
	if ev := e.GateEvents(); len(ev) != 2 || ev[1].Kind != "refiled" {
		t.Errorf("gate events = %+v", ev)
	}
	_, err = call(t, tools, "submit_diagnosis", obj{"groups": []obj{{
		"fingerprint": fp, "reports": []string{"cr_1e2ea0", "cr_ee551c"},
		"category": "nil_value", "evidence": "x", "status": "fixed",
		"root_cause": obj{"file": "store/store.go", "function": "(*Store).Get"},
		"reproducer": obj{"file": "store/get_test.go", "function": "TestPasses"},
		"fix":        "x", "issue": 103}}})
	mustContain(t, errText(err), "the gate refused its reproducer")
}

// TestOwnPanic checks the panic match: numbers in a panic are data, and
// a panic with no data needs the function that crashed on the stack.
func TestOwnPanic(t *testing.T) {
	index := Report{Kind: "panic", Panic: "runtime error: index out of range [1] with length 1"}
	nilDeref := Report{Kind: "panic",
		Panic: "runtime error: invalid memory address or nil pointer dereference",
		Stack: "goroutine 1 [running]:\nexample.com/remind/api.(*API).owned(0x1)\n\t/app/x.go:1 +0x1\n"}
	for _, tc := range []struct {
		out   string
		group []Report
		want  bool
	}{
		{"panic: runtime error: index out of range [2] with length 2", []Report{index}, true},
		{"panic: runtime error: slice bounds out of range [:3]", []Report{index}, false},
		{"panic: runtime error: invalid memory address or nil pointer dereference\n" +
			"example.com/remind/api.(*API).owned(...)", []Report{nilDeref}, true},
		{"panic: runtime error: invalid memory address or nil pointer dereference\n" +
			"example.com/remind/store.(*Store).Get(...)", []Report{nilDeref}, false},
	} {
		if got := Remind.ownPanic(tc.out, tc.group); got != tc.want {
			t.Errorf("%.60q: %v, want %v", tc.out, got, tc.want)
		}
	}
}
