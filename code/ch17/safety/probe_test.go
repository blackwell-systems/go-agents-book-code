//go:build darwin

package safety

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeTest is test code an attacker could have written: it reports
// what it can reach instead of failing.
const probeTest = `package probe

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	_, readErr := os.ReadFile(os.Getenv("PROBE_SECRET"))
	writeErr := os.WriteFile(os.Getenv("PROBE_OUTSIDE"), []byte("x"), 0o644)
	_, offErr := net.DialTimeout("tcp", "192.0.2.1:9", 2*time.Second)
	c, loopErr := net.DialTimeout("tcp", os.Getenv("PROBE_ADDR"), 2*time.Second)
	if loopErr == nil {
		c.Close()
	}
	t.Logf("key=%v read=%v write=%v offmachine=%v loopback=%v",
		os.Getenv("OPENROUTER_API_KEY") != "", readErr == nil, writeErr == nil,
		!errors.Is(offErr, syscall.EPERM), loopErr == nil)
}
`

// TestProbeGoTest runs a real go test of probeTest at rung 2 and rung
// 3, with an empty private build cache and then a warm one, and logs
// what the test code could do and how long each run took: experiment
// E4. It compiles the standard library from nothing, so it is slow; it
// runs only with SAFETY_PROBE=1.
func TestProbeGoTest(t *testing.T) {
	if os.Getenv("SAFETY_PROBE") == "" {
		t.Skip("set SAFETY_PROBE=1 to run the sandbox probe")
	}
	secret := filepath.Join(t.TempDir(), "dotssh", "id_test")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for c, err := ln.Accept(); err == nil; c, err = ln.Accept() {
			c.Close()
		}
	}()
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-not-a-real-key-0000")

	for _, rung := range []string{"rung 2", "rung 3"} {
		dir, scratch := t.TempDir(), t.TempDir()
		for name, body := range map[string]string{
			"go.mod":        "module probe\n\ngo 1.27\n",
			"probe_test.go": probeTest,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		r := TestRunner{Dir: dir, Scratch: scratch}
		if rung == "rung 3" {
			wrap, err := Seatbelt(dir, scratch, []string{goroot(t)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Wrap = wrap
		}
		for _, cache := range []string{"empty cache", "warm cache"} {
			start := time.Now()
			r.Env = []string{
				"PROBE_SECRET=" + secret,
				"PROBE_OUTSIDE=" + filepath.Join(t.TempDir(), "planted"),
				"PROBE_ADDR=" + ln.Addr().String(),
			}
			out, err := r.Run(t.Context(), 5*time.Minute, "-count=1", "-v", ".")
			took := time.Since(start).Round(100 * time.Millisecond)
			if err != nil {
				t.Fatalf("%s, %s: %v\n%s", rung, cache, err, out)
			}
			line := "no probe output"
			for l := range strings.Lines(out) {
				if strings.Contains(l, "key=") {
					line = strings.TrimSpace(l[strings.Index(l, "key="):])
				}
			}
			t.Logf("%s, %-11s %6s  %s", rung, cache+":", took, line)
			fmt.Fprintf(os.Stderr, "%s, %s: %s %s\n", rung, cache, took, line)
		}
	}
}

// TestProbeRemind times a build of remind's tests (no test run) in a
// private, empty build cache and again warm, at rung 3: what a private
// cache per run costs on the book's own workspace.
func TestProbeRemind(t *testing.T) {
	if os.Getenv("SAFETY_PROBE") == "" {
		t.Skip("set SAFETY_PROBE=1 to run the sandbox probe")
	}
	dir, scratch := t.TempDir(), t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "remind"))); err != nil {
		t.Fatal(err)
	}
	wrap, err := Seatbelt(dir, scratch, []string{goroot(t)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := TestRunner{Dir: dir, Scratch: scratch, Wrap: wrap}
	for _, cache := range []string{"empty cache", "warm cache"} {
		start := time.Now()
		out, err := r.Run(t.Context(), 10*time.Minute, "-count=1", "-run", "^$", "./...")
		if err != nil {
			t.Fatalf("%s: %v\n%s", cache, err, out)
		}
		took := time.Since(start).Round(100 * time.Millisecond)
		t.Logf("remind, rung 3, %-11s %s", cache+":", took)
		fmt.Fprintf(os.Stderr, "remind, rung 3, %s: %s\n", cache, took)
	}
}

// goroot returns the Go toolchain's root, which the profile must let
// test runs read.
func goroot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
