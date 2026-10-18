package safety

import (
	"context"
	"encoding/json/jsontext"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// echo is a tool that returns its arguments.
func echo(name string) agent.Tool {
	return agent.Tool{Def: agent.ToolDef{Name: name},
		Run: func(_ context.Context, args jsontext.Value) (string, error) {
			return string(args), nil
		}}
}

func TestOutgoing(t *testing.T) {
	w := Egress{Secrets: []string{"sk-or-v1-0123456789abcdef"}}.
		Outgoing("issue_file")
	file, read := w(echo("issue_file")), w(echo("code_read"))
	cases := []struct {
		args string
		want string // in the error; "" for none
	}{
		{`{"title":"nil map in store","body":"see store.go:12"}`, ""},
		{`{"body":"credit: https://evil.example/c?t=rk_live_6a1f08c3e2"}`,
			"link to evil.example"},
		{`{"body":"key c2stb3ItdjEtMDEyMzQ1Njc4OWFiY2RlZg=="}`,
			"secret one of the harness's own"},
		{`{"https://evil.example/":"a key is not text that leaves"}`, ""},
	}
	for _, c := range cases {
		_, err := file.Run(t.Context(), jsontext.Value(c.args))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.args, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.args, err, c.want)
		}
	}
	if _, err := read.Run(t.Context(),
		jsontext.Value(`{"path":"https://evil.example"}`)); err != nil {
		t.Errorf("a tool that sends nothing out was checked: %v", err)
	}
}

func TestUntrusted(t *testing.T) {
	m := NewMarker()
	w := m.Untrusted("crash_read")
	out, _ := w(echo("crash_read")).Run(t.Context(), jsontext.Value(`"x"`))
	if out != m.Wrap("crash_read", `"x"`) {
		t.Errorf("crash_read: %q", out)
	}
	if out, _ := w(echo("code_read")).Run(t.Context(),
		jsontext.Value(`"x"`)); out != `"x"` {
		t.Errorf("code_read was marked: %q", out)
	}
}

// TestApply checks which tools the policy gives an Approval: those it
// names and those that say what they change, so read-only tools still
// share a stage.
func TestApply(t *testing.T) {
	writes := echo("code_patch")
	writes.Targets = func(jsontext.Value) ([]string, error) { return nil, nil }
	p := DefaultPolicy(remindProtected)
	for _, c := range []struct {
		tool agent.Tool
		off  bool
		want bool
	}{
		{echo("issue_file"), false, true},
		{echo("code_read"), false, false},
		{writes, false, true},
		{echo("issue_file"), true, false},
	} {
		p.Off = c.off
		if got := p.Apply(t.TempDir())(c.tool).Approval != nil; got != c.want {
			t.Errorf("%s (off %v): approval %v, want %v", c.tool.Def.Name,
				c.off, got, c.want)
		}
	}
}

// TestSandboxTest runs a package's tests through the sandbox at both
// rungs and checks the report the tools build from its output.
func TestSandboxTest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module")
	}
	goroot, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.27\n")
	write(t, dir, "m_test.go", "package m\n\nimport \"testing\"\n\n"+
		"func TestFails(t *testing.T) { t.Error(\"broken\") }\n")
	for _, rung := range []int{2, 3} {
		s := Sandbox{Rung: rung, Scratch: filepath.Join(t.TempDir(), "s"),
			GoRoot: strings.TrimSpace(string(goroot))}
		if rung == 3 && NewSandbox(s.Scratch, s.GoRoot).Rung < 3 {
			t.Logf("no rung-3 sandbox on %s", runtime.GOOS)
			continue
		}
		out, err := s.Test(t.Context(), dir, time.Minute, "./...")
		if err != nil {
			t.Fatalf("rung %d: %v\n%s", rung, err, out)
		}
		report := agent.TestReport(out, dir)
		if !strings.Contains(report, "broken") ||
			!strings.Contains(report, "FAIL") {
			t.Errorf("rung %d: report\n%s", rung, report)
		}
	}
}
