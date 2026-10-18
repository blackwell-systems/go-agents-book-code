//go:build darwin

package safety

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSeatbelt runs the helper as test code at rung 2 and inside the
// deny-by-default profile, and checks what it could do: read a planted
// file outside the workspace, write outside it, connect to an address
// off this machine, connect to a listener on this machine's loopback
// address, which the profile allows for httptest, and read a sysctl the
// profile doesn't list.
func TestSeatbelt(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("no sandbox-exec")
	}
	secret := filepath.Join(t.TempDir(), "dotssh", "id_test")
	write(t, filepath.Dir(filepath.Dir(secret)), "dotssh/id_test", "not a key")
	outside := filepath.Join(t.TempDir(), "planted")
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
	probes := []struct {
		args  []string
		rung2 string
		rung3 string
	}{
		{[]string{"read", secret}, "read: true", "read: false"},
		{[]string{"write", outside}, "write: true", "write: false"},
		{[]string{"dial", "192.0.2.1:9"}, "denied: false", "denied: true"}, // TEST-NET-1
		{[]string{"dial", ln.Addr().String()}, "dial: true", "dial: true"},
		{[]string{"sysctl", "kern.osrelease"}, "sysctl: true", "sysctl: true"},
		{[]string{"sysctl", "kern.boottime"}, "sysctl: true", "sysctl: false"}, // not listed
	}
	for _, rung := range []string{"rung 2", "rung 3"} {
		for _, p := range probes {
			r := helper(t, p.args...)
			want := p.rung2
			if rung == "rung 3" {
				wrap, err := Seatbelt(r.Dir, r.Scratch,
					[]string{filepath.Dir(os.Args[0])}, nil) // the helper binary
				if err != nil {
					t.Fatal(err)
				}
				r.Wrap, want = wrap, p.rung3
			}
			out, err := r.Run(t.Context(), time.Minute)
			if err != nil {
				t.Fatalf("%s %v: %v\n%s", rung, p.args, err, out)
			}
			if !strings.Contains(out, want) {
				t.Errorf("%s %s: got %q, want %q", rung, p.args[0], strings.TrimSpace(out), want)
			}
		}
	}
}

// TestSeatbeltPaths checks that the profile refuses paths it can't
// resolve, so a rule can't silently match nothing.
func TestSeatbeltPaths(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"relative/dir", "~/.ssh", filepath.Join(dir, "missing")} {
		if _, err := Seatbelt(dir, dir, []string{bad}, nil); err == nil {
			t.Errorf("Seatbelt accepted %q", bad)
		}
	}
}

// TestSeatbeltSysctls checks that the profile allows reading sysctls
// only by name: every sysctl-read rule has a filter.
func TestSeatbeltSysctls(t *testing.T) {
	dir := t.TempDir()
	profile := seatbeltProfile(map[string]string{"dir": dir,
		"scratch": dir})
	all := strings.Count(profile, "sysctl-read")
	named := len(regexp.MustCompile(`sysctl-read\s+\(sysctl-name`).
		FindAllString(profile, -1))
	if all == 0 || named != all {
		t.Errorf("%d sysctl-read rules, %d with a name filter:\n%s",
			all, named, profile)
	}
}
