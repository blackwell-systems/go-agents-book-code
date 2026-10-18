package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

// Tools returns the four tools this chapter's agent uses, all working on
// fs: list_files, read_file, edit_file, and run_tests.
func Tools(fs FS) []Tool {
	return []Tool{
		listFiles(fs),
		readFile(fs),
		editFile(fs),
		runTests(fs),
	}
}

func listFiles(fs FS) Tool {
	return Tool{
		Def: ToolDef{
			Name:        "list_files",
			Description: "List the files in the project.",
			Parameters:  jsontext.Value(`{"type": "object"}`),
		},
		Run: func(context.Context, jsontext.Value) (string, error) {
			return strings.Join(fs.List(), "\n"), nil
		},
	}
}

// tag::decode[]

// decodeArgs decodes a tool call's arguments. When they don't decode,
// the error says what was wrong and shows the shape the tool expects,
// so the model can fix its next call.
func decodeArgs[T any](args jsontext.Value, example string) (T, error) {
	var v T
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("bad arguments: %w; send JSON like %s",
			err, example)
	}
	return v, nil
}

// end::decode[]

// tag::read-file[]

type pathArgs struct {
	Path string `json:"path"` // <1>
}

func readFile(fs FS) Tool {
	return Tool{
		Def: ToolDef{
			Name:        "read_file",
			Description: "Read a file in the project.",
			Parameters: jsontext.Value(`{
				"type": "object",
				"properties": {"path": {"type": "string"}},
				"required": ["path"]
			}`),
		},
		Run: func(_ context.Context,
			args jsontext.Value) (string, error) {
			a, err := decodeArgs[pathArgs](args,
				`{"path": "a.go"}`) // <2>
			if err != nil {
				return "", err
			}
			return fs.Read(a.Path)
		},
	}
}

// end::read-file[]

type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func editFile(fs FS) Tool {
	return Tool{
		Def: ToolDef{
			Name: "edit_file",
			Description: "Replace text in a file. old must appear " +
				"exactly once in the file.",
			Parameters: jsontext.Value(`{
				"type": "object",
				"properties": {
					"path": {"type": "string"},
					"old": {"type": "string"},
					"new": {"type": "string"}
				},
				"required": ["path", "old", "new"]
			}`),
		},
		Run: func(_ context.Context,
			args jsontext.Value) (string, error) {
			a, err := decodeArgs[editArgs](args,
				`{"path": "a.go", "old": "x := 1", "new": "x := 2"}`)
			if err != nil {
				return "", err
			}
			s, err := fs.Read(a.Path)
			if err != nil {
				return "", err
			}
			if n := strings.Count(s, a.Old); n != 1 {
				return "", fmt.Errorf("old text appears %d times in %s, "+
					"want exactly once", n, a.Path)
			}
			fs[a.Path] = strings.Replace(s, a.Old, a.New, 1)
			return "edited " + a.Path, nil
		},
	}
}

// fixedLine is the line that makes the duration tests pass. The fake
// run_tests looks for it; chapter 4 runs the real tests instead.
const fixedLine = "'m': time.Minute,"

func runTests(fs FS) Tool {
	return Tool{
		Def: ToolDef{
			Name:        "run_tests",
			Description: "Run the project's tests and report the results.",
			Parameters:  jsontext.Value(`{"type": "object"}`),
		},
		Run: func(context.Context, jsontext.Value) (string, error) {
			src, err := fs.Read("duration.go")
			if err != nil {
				return "", errors.New("run_tests: duration.go is missing")
			}
			if strings.Contains(src, fixedLine) {
				return "ok  \tduration\t0.002s", nil
			}
			return failingTests, nil
		},
	}
}

const failingTests = `--- FAIL: TestParse (0.00s)
    duration_test.go:25: Parse("45m") = 45s, want 45m0s
    duration_test.go:25: Parse("1h30m") = 1h0m30s, want 1h30m0s
FAIL
FAIL	duration	0.002s`
