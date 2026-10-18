package agent

import (
	"encoding/json/jsontext"
	"strings"
	"testing"
	"unicode/utf8"
)

func newFS() FS {
	return FS{"duration.go": "var units = map[byte]time.Duration{\n\t'm': time.Second,\n}\n"}
}

func tool(t *testing.T, fs FS, name string) Tool {
	t.Helper()
	for _, tl := range Tools(fs) {
		if tl.Def.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return Tool{}
}

func TestTools(t *testing.T) {
	tests := []struct {
		tool, args string
		want       string // in the result, or in the error when wantErr
		wantErr    bool
	}{
		{"list_files", `{}`, "duration.go", false},
		{"read_file", `{"path": "duration.go"}`, "time.Second", false},
		{"read_file", `{"path": "nope.go"}`, `no file named "nope.go"`, true},
		{"read_file", `{"path": `, "bad arguments", true},
		{"edit_file", `{"path": "duration.go", "old": "nothing", "new": "x"}`, "appears 0 times", true},
		{"edit_file", `{"path": "duration.go", "old": "time", "new": "x"}`, "appears 2 times", true},
		{"read_file", `{"path": 5}`, `send JSON like {"path": "a.go"}`, true},
		{"read_file", `{"path": 5}`, `"path": got a number, want a string`, true},
		{"read_file", `[]`, `the arguments: got an array, want an object`, true},
		{"read_file", `{"Path": "duration.go"}`, `unknown field "Path"`, true},
		{"edit_file", `{"path": "duration.go", "old_str": "x", "new_str": "y"}`,
			`unknown field "old_str"`, true},
		{"edit_file", `{"path": "duration.go", "old": "", "new": "x"}`, "old is empty", true},
	}
	for _, tt := range tests {
		t.Run(tt.tool+" "+tt.args, func(t *testing.T) {
			got, err := tool(t, newFS(), tt.tool).Run(t.Context(), jsontext.Value(tt.args))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Errorf("error %v, want one containing %q", err, tt.want)
				}
				return
			}
			if err != nil || !strings.Contains(got, tt.want) {
				t.Errorf("= %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestArgErrorKinds(t *testing.T) {
	type args struct {
		N int64   `json:"n"`
		F float32 `json:"f"`
		L []int   `json:"l"`
	}
	for in, want := range map[string]string{
		`{"n": "1"}`: `"n": got a string, want a whole number`,
		`{"f": []}`:  `"f": got an array, want a number`,
		`{"l": {}}`:  `"l": got an object, want an array`,
	} {
		_, err := decodeArgs[args](jsontext.Value(in), `{}`)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestLimitKeepsCharacters(t *testing.T) {
	s := strings.Repeat("é", 10) // two bytes each
	got := limit(s, 5)
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "éé\n[") {
		t.Errorf("limit = %q", got)
	}
}
