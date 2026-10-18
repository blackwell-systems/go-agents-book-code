package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"
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
	err := json.Unmarshal(args, &v, json.RejectUnknownMembers(true)) // <1>
	if err != nil {
		return v, &toolError{ErrToolArgs, fmt.Sprintf(
			"bad arguments: %s; send JSON like %s", argError(err), example)}
	}
	return v, nil
}

// argError says why arguments didn't decode, in the same words every
// time. json/v2 deliberately varies the wording of its errors between
// runs, and this text goes to the model and into saved runs.
func argError(err error) string {
	if _, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		return err.Error() // syntax errors read the same every time
	}
	se, ok := errors.AsType[*json.SemanticError](err)
	if !ok || se.GoType == nil {
		return "the JSON doesn't fit the arguments"
	}
	where := "the arguments"
	if p := strings.TrimPrefix(string(se.JSONPointer), "/"); p != "" {
		where = strconv.Quote(p)
	}
	if errors.Is(se.Err, json.ErrUnknownName) { // <2>
		return "unknown field " + where
	}
	return fmt.Sprintf("%s: got %s, want %s", where,
		kindOf(se.JSONKind), goKind(se.GoType.Kind()))
}

// end::decode[]

// kindOf names a kind of JSON value.
func kindOf(k jsontext.Kind) string {
	switch k {
	case 'n':
		return "null"
	case 'f', 't':
		return "true or false"
	case '"':
		return "a string"
	case '0':
		return "a number"
	case '{':
		return "an object"
	case '[':
		return "an array"
	}
	return "a value"
}

// goKind names what JSON a kind of Go value takes.
func goKind(k reflect.Kind) string {
	switch {
	case k == reflect.String:
		return "a string"
	case k == reflect.Bool:
		return "true or false"
	case k >= reflect.Int && k <= reflect.Uintptr:
		return "a whole number"
	case k == reflect.Float32 || k == reflect.Float64:
		return "a number"
	case k == reflect.Slice || k == reflect.Array:
		return "an array"
	case k == reflect.Struct || k == reflect.Map:
		return "an object"
	}
	return "something else"
}

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
		Run: func(_ context.Context, args jsontext.Value) (string, error) {
			a, err := decodeArgs[pathArgs](args, `{"path": "a.go"}`) // <2>
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
		Run: func(_ context.Context, args jsontext.Value) (string, error) {
			a, err := decodeArgs[editArgs](args,
				`{"path": "a.go", "old": "x := 1", "new": "x := 2"}`)
			if err != nil {
				return "", err
			}
			if a.Old == "" {
				return "", errors.New("old is empty; send the text to replace")
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

func runTests(fs FS) Tool {
	return Tool{
		Def: ToolDef{
			Name:        "run_tests",
			Description: "Run the project's tests and report the results.",
			Parameters:  jsontext.Value(`{"type": "object"}`),
		},
		Run: func(ctx context.Context, _ jsontext.Value) (string, error) {
			return goTest(ctx, fs, testTimeout)
		},
	}
}
