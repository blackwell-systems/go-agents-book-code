package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// tag::schema[]

// A schema is the subset of JSON Schema the book generates: the types
// a tool's arguments can have, with no field the struct doesn't list.
// A field that may be left out is left out of Required.
type schema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitzero"`
	Enum        []string `json:"enum,omitzero"`
	Items       *schema  `json:"items,omitzero"`
	Properties  props    `json:"properties,omitzero"`
	Required    []string `json:"required,omitzero"`
	NoExtras    *bool    `json:"additionalProperties,omitzero"`

	optional bool // the field may be left out (omitzero in its json tag)
	field    int  // the struct field index, for properties
}

// end::schema[]

// tag::props[]

// props are an object's properties, in the order of the struct's
// fields. A Go map would lose that order, and the model reads the
// fields in the order the schema lists them.
type props []prop

type prop struct {
	name string
	s    *schema
}

// MarshalJSONTo writes the properties as one JSON object, in order.
// encoding/json/v2 calls it for any type that has it. The encoder's
// errors pass through unchanged, so json/v2 can say where in the value
// encoding failed.
//
//nolint:wrapcheck // json/v2 wraps a marshaler's errors itself
func (ps props) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for _, p := range ps {
		if err := enc.WriteToken(jsontext.String(p.name)); err != nil {
			return err
		}
		if err := json.MarshalEncode(enc, p.s); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// end::props[]

// strictOK reports whether a schema meets providers' strict modes,
// which require every field at every depth to be listed as required.
func strictOK(s *schema) bool {
	for _, p := range s.Properties {
		if p.s.optional || !strictOK(p.s) {
			return false
		}
	}
	return s.Items == nil || strictOK(s.Items)
}

// tag::schema-for[]

// schemaFor builds the schema for a Go type. It refuses types a tool's
// arguments shouldn't have: maps (open-ended keys), interfaces and
// pointers (no single shape), and types that contain themselves.
func schemaFor(t reflect.Type, seen []reflect.Type) (*schema, error) {
	switch t.Kind() {
	case reflect.String:
		return &schema{Type: "string"}, nil
	case reflect.Bool:
		return &schema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64:
		return &schema{Type: "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return &schema{Type: "number"}, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("[]byte is base64 in JSON; use a string")
		}
		items, err := schemaFor(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return &schema{Type: "array", Items: items}, nil
	case reflect.Struct:
		if slices.Contains(seen, t) { // <1>
			return nil, fmt.Errorf("%s contains itself", t)
		}
		return objectSchema(t, append(seen, t))
	}
	return nil, fmt.Errorf("a %s can't be a tool argument", t.Kind()) // <2>
}

// end::schema-for[]

// tag::object-schema[]

// objectSchema builds the schema for a struct from its fields' tags:
// json for the name (and omitzero for a field that may be left out),
// desc for a description the model reads, and enum for the only values
// a string may take.
func objectSchema(t reflect.Type, seen []reflect.Type) (*schema, error) {
	no := false
	s := &schema{Type: "object", NoExtras: &no,
		Properties: props{}, Required: []string{}} // <1>
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return nil, fmt.Errorf("field %s has no json name", f.Name)
		}
		fs, err := schemaFor(f.Type, seen)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fs.Description = f.Tag.Get("desc")
		for v := range strings.SplitSeq(f.Tag.Get("enum"), ",") {
			if v != "" {
				fs.Enum = append(fs.Enum, v)
			}
		}
		fs.field = i
		fs.optional = strings.Contains(opts, "omitzero")
		if !fs.optional {
			s.Required = append(s.Required, name) // <2>
		}
		s.Properties = append(s.Properties, prop{name, fs})
	}
	return s, nil
}

// end::object-schema[]
