package triage

import (
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// TestDiagnoseOnly: the run has no tool that changes remind's code,
// test_write creates only new test files, the gate accepts a
// reproducer that fails on the original code with no fix to pass, and
// no group can be fixed.
func TestDiagnoseOnly(t *testing.T) {
	e, _ := newTestEnv(t, "m1-done-after-delete")
	e.DiagnoseOnly, e.Gate = true, true
	tools := map[string]bool{}
	byName := map[string]agent.Tool{}
	for _, tool := range DiagnoseTools(e) {
		tools[tool.Def.Name] = true
		byName[tool.Def.Name] = tool
	}
	if tools["code_patch"] || !tools["test_write"] {
		t.Fatalf("diagnose-only tools %v", tools)
	}
	repro := "package store\n\nimport \"testing\"\n\n" +
		"func TestRepro(t *testing.T) { t.Fatal(\"reproduced\") }\n"
	for _, tt := range []struct {
		path, content string
		ok            bool
	}{
		{"store/store.go", "package store\n", false},      // not a test
		{"store/store_test.go", "package store\n", false}, // exists
		{"store/repro_test.go", repro, true},
	} {
		_, err := call(t, byName, "test_write",
			map[string]any{"path": tt.path, "content": tt.content})
		if (err == nil) != tt.ok {
			t.Errorf("test_write %s: %v", tt.path, err)
		}
	}
	fp := Fingerprint(e.Reports[0])
	refusal, err := e.gate(t.Context(), fp,
		Location{File: "store/repro_test.go", Function: "TestRepro"})
	if err != nil || refusal != "" {
		t.Errorf("gate in diagnose-only mode: %q, %v", refusal, err)
	}
	err = e.checkGroups([]Diagnosis{{Fingerprint: fp, Status: "fixed",
		Reports: reportIDsOf(e)}})
	if err == nil || !strings.Contains(err.Error(), "can't change code") {
		t.Errorf("a fixed group in diagnose-only mode: %v", err)
	}
}

func reportIDsOf(e *Env) []string {
	var ids []string
	for _, r := range e.Reports {
		ids = append(ids, r.ID)
	}
	return ids
}
