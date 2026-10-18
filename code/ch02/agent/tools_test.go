package agent

import (
	"encoding/json/jsontext"
	"strings"
	"testing"
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
		{"run_tests", `{}`, "FAIL", false},
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

func TestEditThenTestsPass(t *testing.T) {
	fs := newFS()
	args := `{"path": "duration.go", "old": "'m': time.Second,", "new": "'m': time.Minute,"}`
	if _, err := tool(t, fs, "edit_file").Run(t.Context(), jsontext.Value(args)); err != nil {
		t.Fatal(err)
	}
	got, err := tool(t, fs, "run_tests").Run(t.Context(), nil)
	if err != nil || !strings.HasPrefix(got, "ok") {
		t.Errorf("run_tests after the fix = %q, %v", got, err)
	}
}
