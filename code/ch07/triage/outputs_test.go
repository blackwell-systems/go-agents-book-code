package triage

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
)

var update = flag.Bool("update", false, "rewrite the golden tool outputs")

// TestToolOutputs saves what the tools return for a few calls, for the
// chapter to show, and checks they haven't changed.
func TestToolOutputs(t *testing.T) {
	_, tools := newTestEnv(t, "m1-done-after-delete")
	_, deadlock := newTestEnv(t, "dl1-lock-order")
	type obj = map[string]any
	// One raw report, as remind writes it, with its stack and logs
	// shortened for the page.
	e, _ := newTestEnv(t, "m1-done-after-delete")
	r, err := e.report("cr_1e2ea0")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(r.Stack, "\n")
	r.Stack = strings.Join(lines[:9], "\n") + fmt.Sprintf("\n[%d more lines]", len(lines)-9)
	r.Logs = append([]string{fmt.Sprintf("[%d earlier lines]", len(r.Logs)-1)}, r.Logs[len(r.Logs)-1:]...)
	raw, err := json.Marshal(r, jsontext.Multiline(true), json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "report.json", string(raw))

	def := tools["code_read"].Def
	wire, err := json.Marshal(map[string]any{"type": "function", "function": map[string]any{
		"name": def.Name, "description": def.Description, "parameters": def.Parameters}},
		jsontext.Multiline(true), json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "code_read_def.json", string(wire))
	for _, c := range []struct {
		file  string
		tools map[string]agent.Tool
		name  string
		args  any
	}{
		{"crash_list.txt", tools, "crash_list", obj{}},
		{"crash_read.txt", tools, "crash_read", obj{"id": "cr_1e2ea0"}},
		{"crash_goroutines.txt", deadlock, "crash_goroutines", obj{"id": "cr_ea1512"}},
		{"code_patch_miss.txt", tools, "code_patch", obj{"path": "store/store.go",
			"old": "    case r.Deleted:\n        r = nil", "new": "x"}},
	} {
		if c.name == "code_patch" {
			if _, err := call(t, c.tools, "code_read", obj{"path": "store/store.go"}); err != nil {
				t.Fatal(err)
			}
		}
		out, err := call(t, c.tools, c.name, c.args)
		if err != nil {
			out = "error: " + err.Error()
		}
		golden(t, c.file, out)
	}
}

// golden checks out against a saved file, or saves it with -update.
func golden(t *testing.T, file, out string) {
	t.Helper()
	path := filepath.Join("..", "testdata", "out", "tools", file)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(out+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(out+"\n"), want) {
		t.Errorf("%s changed; run go test -update", file)
	}
}
