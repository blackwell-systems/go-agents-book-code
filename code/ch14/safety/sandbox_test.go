//go:build unix

package safety

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperMode returns the mode the test binary was started in as a
// helper process, after "--" in its arguments, or "" for a normal run.
// The runner replaces the environment, so the mode can't travel there.
func helperMode() []string {
	if i := slices.Index(os.Args, "--"); i >= 0 {
		return os.Args[i+1:]
	}
	return nil
}

// TestHelper is not a test: it is what the runner starts in place of
// go test, to act as test code would.
func TestHelper(t *testing.T) {
	mode := helperMode()
	if mode == nil {
		t.Skip("helper process only")
	}
	switch mode[0] {
	case "env":
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
	case "spawn": // start a child that outlives us, then hang
		child := exec.CommandContext(context.Background(), "sleep", "30")
		if err := child.Start(); err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		fmt.Println("child", child.Process.Pid)
		time.Sleep(time.Minute)
	case "flood":
		line := strings.Repeat("x", 1023) + "\n"
		for range 4096 {
			fmt.Print(line)
		}
	case "read":
		_, err := os.ReadFile(mode[1])
		fmt.Println("read:", err == nil)
	case "write":
		err := os.WriteFile(mode[1], []byte("x"), 0o644)
		fmt.Println("write:", err == nil)
	case "dial":
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(context.Background(), "tcp", mode[1])
		if err == nil {
			c.Close()
		}
		fmt.Println("dial:", err == nil, "denied:", errors.Is(err, syscall.EPERM))
	case "proc": // is this process ID there to ask about?
		_, err := os.Stat("/proc/" + mode[1])
		fmt.Println("proc:", err == nil)
	case "sysctl": // read one kernel value by name
		fmt.Println("sysctl:", readSysctl(mode[1]) == nil)
	case "linger": // leave a child holding the output, then exit
		child := exec.CommandContext(context.Background(), "sleep", "30")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		fmt.Println("child", child.Process.Pid)
	}
	os.Exit(0)
}

// helper returns a runner that starts this test binary as its "test
// code", in the given mode.
func helper(t *testing.T, mode ...string) TestRunner {
	return TestRunner{Dir: t.TempDir(), Scratch: t.TempDir(),
		Command: append([]string{os.Args[0], "-test.run=^TestHelper$", "--"}, mode...)}
}

func TestRunnerEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-not-a-real-key-0000")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	r := helper(t, "env")
	out, err := r.Run(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"OPENROUTER_API_KEY", "SSH_AUTH_SOCK"} {
		if strings.Contains(out, leak) {
			t.Errorf("%s reached the test code", leak)
		}
	}
	home := "HOME=" + filepath.Join(r.Scratch, "home")
	if !strings.Contains(out, home+"\n") || !strings.Contains(out, "GOPROXY=off") {
		t.Errorf("environment:\n%s", out)
	}
}

// TestRunnerKillsGroup checks that a timeout kills the test's child
// too, not only the process the runner started.
func TestRunnerKillsGroup(t *testing.T) {
	r := helper(t, "spawn")
	out, err := r.Run(t.Context(), 2*time.Second)
	if !errors.Is(err, ErrTestsTimedOut) {
		t.Fatalf("err = %v, want ErrTestsTimedOut\n%s", err, out)
	}
	f := strings.Fields(out)
	i := slices.Index(f, "child")
	if i < 0 || i+1 >= len(f) {
		t.Fatalf("no child pid in %q", out)
	}
	pid, err := strconv.Atoi(f[i+1])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil { // the child, or its zombie, is still there
		if time.Now().After(deadline) {
			t.Fatalf("child %d outlived the run", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunnerCapsOutput(t *testing.T) {
	r := helper(t, "flood")
	r.MaxOutput = 64 << 10
	out, err := r.Run(t.Context(), time.Minute)
	if !errors.Is(err, ErrOutputCapped) || len(out) != 64<<10 {
		t.Errorf("err = %v, %d bytes kept; want ErrOutputCapped, 65536", err, len(out))
	}
}

// TestRunnerLinger checks that a test which exits while a child it left
// still holds the output open is a finished run, not an error, and that
// the child is killed with the rest of the group.
func TestRunnerLinger(t *testing.T) {
	r := helper(t, "linger")
	out, err := r.Run(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	f := strings.Fields(out)
	i := slices.Index(f, "child")
	if i < 0 || i+1 >= len(f) {
		t.Fatalf("no child pid in %q", out)
	}
	pid, _ := strconv.Atoi(f[i+1])
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d outlived the run", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestBubblewrapArgs checks the mount order: the empty directories come
// before the binds that must show through them, so a workspace under
// /tmp or a toolchain under home isn't hidden.
func TestBubblewrapArgs(t *testing.T) {
	name, args := Bubblewrap("/tmp/w", "/tmp/s", "/home/u", "/home/u/sdk/go",
		[]string{"auth/", "go.mod"})("go", "test", "./...")
	got := name + " " + strings.Join(args, " ")
	order := []string{"--ro-bind / /", "--tmpfs /tmp", "--tmpfs /run",
		"--tmpfs /home/u", "--ro-bind /home/u/sdk/go /home/u/sdk/go",
		"--bind /tmp/w /tmp/w", "--ro-bind-try /tmp/w/auth /tmp/w/auth",
		"--ro-bind-try /tmp/w/go.mod /tmp/w/go.mod", "--chdir /tmp/w",
		"--unshare-all",
		"--die-with-parent", "-- go test ./..."}
	at := 0
	for _, want := range order {
		i := strings.Index(got[at:], want)
		if i < 0 {
			t.Fatalf("%q lacks %q after position %d", got, want, at)
		}
		at += i + len(want)
	}
}

// TestSandboxReads checks Sandbox.Reads against the sandbox itself: for
// each path, what Reads says test code could read is what test code
// run in the sandbox does read.
func TestSandboxReads(t *testing.T) {
	bin := filepath.Dir(os.Args[0]) // the helper, readable like a toolchain
	s := NewSandbox(t.TempDir(), bin)
	if s.Rung < 3 {
		t.Skip("no rung-3 sandbox here")
	}
	dir := t.TempDir()
	files := map[string]string{"workspace": filepath.Join(dir, "f"),
		"scratch":   filepath.Join(s.Scratch, "f"),
		"elsewhere": filepath.Join(t.TempDir(), "key")}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files["system"] = "/bin/sh"
	wrap, err := s.wrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range files {
		want, err := s.Reads(dir, f)
		if err != nil {
			t.Fatal(err)
		}
		r := helper(t, "read", f)
		r.Dir, r.Scratch, r.Wrap = dir, s.Scratch, wrap
		out, err := r.Run(t.Context(), time.Minute)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if got := strings.Contains(out, "read: true"); got != want {
			t.Errorf("%s: test code read it: %v; Reads says %v", name, got, want)
		}
	}
	if ok, _ := s.Reads(dir, files["elsewhere"]); ok {
		t.Errorf("Reads: a file in another temporary directory is readable")
	}
}

// TestBubblewrapProcesses checks, on Linux with bwrap, that test code
// in the sandbox can't see the harness's process, and so can't ask the
// kernel about it, while at rung 2 it can.
func TestBubblewrapProcesses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap is for Linux")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bwrap")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	for _, want := range []string{"proc: true", "proc: false"} {
		r := helper(t, "proc", pid)
		if want == "proc: false" {
			r.Wrap = Bubblewrap(r.Dir, r.Scratch, home,
				filepath.Dir(os.Args[0]), nil)
		}
		out, err := r.Run(t.Context(), time.Minute)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out, want) {
			t.Errorf("got %q, want %q", strings.TrimSpace(out), want)
		}
	}
}
