package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// tag::new-tool[]

// NewTool makes a Tool from a Go function whose arguments are the
// fields of a struct, A. The schema the model sees is generated from A,
// so the two can't drift apart. NewTool panics on a type that can't be
// a tool's arguments, which is a programmer's mistake.
func NewTool[A any](name, description string,
	run func(ctx context.Context, args A) (string, error)) Tool {
	t := reflect.TypeFor[A]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("agent: tool %s: arguments must be a struct",
			name))
	}
	s, err := schemaFor(t, nil)
	if err != nil {
		panic(fmt.Sprintf("agent: tool %s: %v", name, err))
	}
	params, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("agent: tool %s: %v", name, err))
	}
	return Tool{
		Def: ToolDef{Name: name, Description: description,
			Parameters: params, Strict: strictOK(s)},
		Run: func(ctx context.Context, args jsontext.Value) (string, error) {
			a, err := decodeStrict[A](args, s)
			if err != nil {
				return "", err
			}
			return run(ctx, a)
		},
	}
}

// end::new-tool[]

// tag::decode-strict[]

// decodeStrict decodes a call's arguments into A and checks them
// against A's schema: every required field present and not null, no
// field the schema doesn't list, and strings within their enumerations.
// Each error names the field and shows the shape to send.
func decodeStrict[A any](args jsontext.Value, s *schema) (A, error) {
	var a A
	if !args.IsValid() { // <1>
		var v any
		err := json.Unmarshal(args, &v)
		return a, badArgs("the arguments aren't valid JSON: "+argError(err), s)
	}
	if err := missing(args, s, ""); err != nil { // <2>
		return a, badArgs(err.Error(), s)
	}
	err := json.Unmarshal(args, &a, json.RejectUnknownMembers(true))
	if err != nil {
		return a, badArgs(argError(err), s) // <3>
	}
	if err := outsideEnum(reflect.ValueOf(a), s, ""); err != nil { // <4>
		return a, badArgs(err.Error(), s)
	}
	return a, nil
}

// end::decode-strict[]

// badArgs is the error the model reads: what was wrong, and the shape
// the tool expects.
func badArgs(problem string, s *schema) error {
	return &toolError{ErrToolArgs, fmt.Sprintf(
		"bad arguments: %s; send %s", problem, shape(s))}
}

// tag::missing[]

// missing reports the first required field that is absent or null in
// v, at any depth. json/v2 has no notion of a required field: a field
// left out decodes as its zero value, the same as one sent as zero, so
// the check reads the JSON itself.
func missing(v jsontext.Value, s *schema, path string) error {
	switch s.Type {
	// tag::missing-object[]
	case "object":
		var fields map[string]jsontext.Value
		if json.Unmarshal(v, &fields) != nil {
			return fmt.Errorf("%s must be a JSON object", where(path))
		}
		for _, p := range s.Properties {
			fv, ok := fields[p.name]
			absent := !ok || string(fv) == "null"
			if absent && !p.s.optional {
				return fmt.Errorf("missing required field %q",
					join(path, p.name))
			}
			if !absent {
				if err := missing(fv, p.s, join(path, p.name)); err != nil {
					return err
				}
			}
		}
	// end::missing-object[]
	case "array":
		var items []jsontext.Value
		isArray := json.Unmarshal(v, &items) == nil
		if !isArray {
			return nil // the decode reports the wrong type
		}
		for i, it := range items {
			err := missing(it, s.Items, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// end::missing[]

// outsideEnum reports the first string in v that isn't one of its
// field's allowed values. An optional field left empty is allowed.
func outsideEnum(v reflect.Value, s *schema, path string) error {
	switch v.Kind() {
	case reflect.String:
		if s.Enum == nil || (s.optional && v.String() == "") {
			return nil
		}
		if !slices.Contains(s.Enum, v.String()) {
			return fmt.Errorf("%s is %q, which isn't %s",
				where(path), v.String(), enumList(s.Enum))
		}
	case reflect.Slice:
		for i := range v.Len() {
			err := outsideEnum(v.Index(i), s.Items, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return err
			}
		}
	case reflect.Struct:
		for _, p := range s.Properties {
			err := outsideEnum(v.Field(p.s.field), p.s, join(path, p.name))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// shape describes the arguments a schema expects, for an error the
// model reads: {"path": string, "start_line": integer, optional}.
func shape(s *schema) string {
	switch s.Type {
	case "object":
		var parts []string
		for _, p := range s.Properties {
			part := fmt.Sprintf("%q: %s", p.name, shape(p.s))
			if p.s.optional {
				part += ", optional"
			}
			parts = append(parts, part)
		}
		return "{" + strings.Join(parts, "; ") + "}"
	case "array":
		return "[" + shape(s.Items) + ", ...]"
	}
	if s.Enum != nil {
		return enumList(s.Enum)
	}
	return s.Type
}

func enumList(enum []string) string {
	var vs []string
	for _, v := range enum {
		vs = append(vs, fmt.Sprintf("%q", v))
	}
	return "one of " + strings.Join(vs, ", ")
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func where(path string) string {
	if path == "" {
		return "the arguments"
	}
	return fmt.Sprintf("%q", path)
}
