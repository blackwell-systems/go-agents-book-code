package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type readArgs struct {
	Path  string `json:"path" desc:"a file path, relative to the project"`
	Start int    `json:"start,omitzero" desc:"the first line"`
}

type nested struct {
	Groups []struct {
		Reports []string `json:"reports"`
		Status  string   `json:"status" enum:"fixed,duplicate"`
		Note    string   `json:"note,omitzero" enum:"short,long"`
	} `json:"groups"`
}

// TestSchema checks the generated schema, byte for byte, including
// property order, and an optional field left out of required.
func TestSchema(t *testing.T) {
	tool := NewTool("read", "Read a file.",
		func(context.Context, readArgs) (string, error) { return "", nil })
	want := `{"type":"object","properties":{"path":{"type":"string",` +
		`"description":"a file path, relative to the project"},` +
		`"start":{"type":"integer","description":"the first line"}},` +
		`"required":["path"],"additionalProperties":false}`
	if got := string(tool.Def.Parameters); got != want {
		t.Errorf("schema:\n got %s\nwant %s", got, want)
	}
	if tool.Def.Strict {
		t.Error("a schema with an optional field can't be strict")
	}
	none := NewTool("list", "List.",
		func(context.Context, struct{}) (string, error) { return "", nil })
	want = `{"type":"object","properties":{},"required":[],"additionalProperties":false}`
	if got := string(none.Def.Parameters); got != want {
		t.Errorf("schema with no arguments:\n got %s\nwant %s", got, want)
	}
	if !none.Def.Strict {
		t.Error("a schema with every field required should be strict")
	}
	tool = NewTool("submit", "Submit.",
		func(context.Context, nested) (string, error) { return "", nil })
	for _, part := range []string{`"enum":["fixed","duplicate"]`,
		`"enum":["short","long"]`, `"items":{"type":"object"`} {
		if !strings.Contains(string(tool.Def.Parameters), part) {
			t.Errorf("schema lacks %s:\n%s", part, tool.Def.Parameters)
		}
	}
}

type recursive struct {
	Children []recursive `json:"children"`
}

// TestSchemaRefuses checks the types a tool's arguments can't have.
func TestSchemaRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want string
	}{
		{"map", reflect.TypeFor[struct {
			M map[string]string `json:"m"`
		}](), "map"},
		{"interface", reflect.TypeFor[struct {
			V any `json:"v"`
		}](), "interface"},
		{"pointer", reflect.TypeFor[struct {
			P *int `json:"p"`
		}](), "ptr"},
		{"recursive", reflect.TypeFor[recursive](), "contains itself"},
		{"untagged", reflect.TypeFor[struct{ Path string }](), "no json name"},
		{"skipped", reflect.TypeFor[struct {
			Path string `json:"-"`
		}](), "no json name"},
		{"bytes", reflect.TypeFor[struct {
			B []byte `json:"b"`
		}](), "base64"},
	} {
		_, err := schemaFor(tc.typ, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("NewTool with a bad type should panic")
		}
	}()
	NewTool("bad", "", func(context.Context, recursive) (string, error) {
		return "", nil
	})
}

// TestDecodeStrict checks each way arguments can be wrong, and that
// the message names the field and shows the shape to send.
func TestDecodeStrict(t *testing.T) {
	var got nested
	tool := NewTool("submit", "Submit.",
		func(_ context.Context, a nested) (string, error) {
			got = a
			return "ok", nil
		})
	for _, tc := range []struct{ args, want string }{
		{`{"groups": [{"reports": ["a"], "status": "fixed"}]}`, ""},
		{`{"groups": [{"reports": ["a"], "status": "fixed", "note": null}]}`, ""},
		{`[]`, "the arguments must be a JSON object"},
		{`{"groups": , "x": 1}`, "the arguments aren't valid JSON"},
		{`{}`, `missing required field "groups"`},
		{`{"groups": null}`, `missing required field "groups"`},
		{`{"groups": [{"reports": ["a"]}]}`,
			`missing required field "groups[0].status"`},
		{`{"groups": [{"reports": ["a"], "status": "done"}]}`,
			`"groups[0].status" is "done", which isn't one of "fixed", "duplicate"`},
		{`{"groups": [{"reports": ["a"], "status": "fixed", "extra": 1}]}`,
			`unknown field`},
		{`{"groups": [{"reports": "a", "status": "fixed"}]}`, `"groups/0/reports"`},
	} {
		_, err := tool.Run(t.Context(), jsontext.Value(tc.args))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: err = %v", tc.args, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.args, err, tc.want)
		case tc.want != "" && !errors.Is(err, ErrToolArgs):
			t.Errorf("%s: err = %v, want ErrToolArgs", tc.args, err)
		case tc.want != "" && !strings.Contains(err.Error(), `send {"groups": [{"reports": [string, ...]`):
			t.Errorf("%s: err = %v, want the shape", tc.args, err)
		}
	}
	if len(got.Groups) != 1 || got.Groups[0].Status != "fixed" {
		t.Errorf("decoded %+v", got)
	}
}
