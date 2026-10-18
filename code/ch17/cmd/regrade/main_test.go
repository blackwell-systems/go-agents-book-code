package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestPatch: a code_patch call is applied as the tool applies it, and
// one whose old text isn't found exactly once is an error.
func TestPatch(t *testing.T) {
	ws := t.TempDir()
	if err := patch(ws, patchCall{Path: "api/new_test.go",
		New: "package api\n"}); err != nil {
		t.Fatalf("new file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.go"),
		[]byte("x := 1\ny := 2\ny := 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		old string
		ok  bool
	}{{"x := 1", true}, {"z := 3", false}, {"y := 2", false}} {
		err := patch(ws, patchCall{Path: "a.go", Old: tc.old, New: "x := 0"})
		if (err == nil) != tc.ok {
			t.Errorf("old %q: %v", tc.old, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(ws, "a.go"))
	if err != nil || string(b) != "x := 0\ny := 2\ny := 2\n" {
		t.Errorf("a.go = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "api", "new_test.go")); err != nil {
		t.Error(err)
	}
}

// TestSubmitted: the arguments are joined from their chunks, the last
// submit_diagnosis by index wins, and other lines are skipped.
func TestSubmitted(t *testing.T) {
	chunk := func(index int, name, args string) string {
		return `data: {"choices":[{"delta":{"tool_calls":[{"index":` +
			strconv.Itoa(index) + `,"function":{"name":"` + name +
			`","arguments":` + quote(t, args) + `}}]}}]}` + "\n"
	}
	stream := ": keep-alive\n" +
		chunk(0, "submit_diagnosis", `{"groups":`) +
		chunk(0, "", `[1]}`) +
		"data: {not json\n" +
		chunk(1, "submit_diagnosis", `{"groups":`) +
		chunk(1, "", `[2]}`) +
		chunk(2, "code_read", `{}`) +
		"data: [DONE]\n"
	got, err := submitted(stream)
	if err != nil || got != `{"groups":[2]}` {
		t.Errorf("submitted = %q, %v", got, err)
	}
	got, err = submitted(chunk(0, "code_read", `{}`))
	if err != nil || got != "" {
		t.Errorf("no answer: %q, %v", got, err)
	}
}

// TestReadRun: over three recorded calls, readRun keeps the patches
// that went through, in order, skips a refused one, keeps a result the
// context policy later cleared, ignores the reviewer's recordings, and
// returns the last call's answer.
func TestReadRun(t *testing.T) {
	dir := t.TempDir()
	call := func(id, path, old, text string) map[string]any {
		args, err := json.Marshal(patchCall{Path: path, Old: old, New: text})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": id, "function": map[string]any{
				"name": "code_patch", "arguments": string(args)}}}}
	}
	result := func(id, text string) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id,
			"content": text}
	}
	data := "<data from=\"code_patch\" id=abc>\n"
	write := func(name string, msgs []any, response string) {
		b, err := json.Marshal(map[string]any{
			"request":       map[string]any{"messages": msgs},
			"response_text": response,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	user := map[string]any{"role": "user", "content": "triage"}
	first := []any{user,
		call("c1", "api/x_test.go", "", "package api\n"),
		result("c1", data+"changed api/x_test.go; run the tests"),
		call("c2", "auth/auth.go", "a", "b"),
		result("c2", data+"refused: protected: auth/"),
	}
	answer := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,` +
		`"function":{"name":"submit_diagnosis","arguments":"{}"}}]}}]}` + "\n"
	write("s-01.json", []any{user}, "")
	write("s-02.json", first, "")
	write("s-review-01.json", []any{call("r1", "x.go", "", "")}, "")
	write("s-03.json", []any{user,
		call("c1", "api/x_test.go", "", "package api\n"),
		result("c1", "[cleared to save space: 40 lines]"),
		call("c3", "api/api.go", "old", "new"),
		result("c3", data+"changed api/api.go, but the run's tests fail"),
	}, answer)
	calls, final, err := readRun(dir, "s-")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range calls {
		paths = append(paths, c.Path)
	}
	if want := []string{"api/x_test.go", "api/api.go"}; !slices.Equal(paths,
		want) {
		t.Errorf("patches %v, want %v", paths, want)
	}
	if final != "{}" {
		t.Errorf("answer %q", final)
	}
	if _, _, err := readRun(dir, "none-"); err == nil ||
		!strings.Contains(err.Error(), "no recordings") {
		t.Errorf("no recordings: %v", err)
	}
}

func quote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
