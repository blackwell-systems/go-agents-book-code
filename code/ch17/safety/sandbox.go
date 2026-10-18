package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// tag::runner[]

// A TestRunner runs go test in a workspace the second rung up the
// chapter's ladder: none of the user's environment, a private home and
// build cache, no network for modules, a cap on output, and the whole
// process group killed when time runs out. It stops mistakes and
// runaway tests; it doesn't stop hostile test code, which still runs as
// you and can read what you can. Wrap, if set, puts the command inside
// a rung-3 sandbox (Seatbelt on macOS, bubblewrap on Linux).
type TestRunner struct {
	Dir       string // the workspace
	Scratch   string // private, per run: home, build cache, temp files
	MaxOutput int    // bytes kept; 0 means 1 MiB
	Wrap      func(name string, args ...string) (string, []string)
	Command   []string // the command before args; "go test" if empty
	Env       []string // extra variables, added after the runner's own
}

// end::runner[]

// tag::env[]

// env is the whole environment the test run gets. Nothing is inherited
// but PATH, so the agent's API key, a cloud token, or an SSH agent
// socket in the user's environment never reaches test code.
func (t TestRunner) env() []string {
	s := t.Scratch
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(s, "home"),
		"GOCACHE=" + filepath.Join(s, "cache"), // <1>
		"GOMODCACHE=" + filepath.Join(s, "mod"),
		"GOPATH=" + filepath.Join(s, "gopath"),
		"GOTMPDIR=" + filepath.Join(s, "tmp"),
		"TMPDIR=" + filepath.Join(s, "tmp"),
		"GOPROXY=off", "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off",
		"GOFLAGS=", "CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1",
	}
}

// end::env[]

// ErrOutputCapped is the cause recorded when a test run's output passed
// MaxOutput; the rest was dropped as it was read.
var ErrOutputCapped = errors.New("test output over the limit")

// tag::run[]

// Run runs go test with args in the workspace, for at most timeout, and
// returns its output, cut at MaxOutput as it is read. A timeout kills
// every process the run started, not only the go command.
func (t TestRunner) Run(ctx context.Context, timeout time.Duration,
	args ...string) (string, error) {
	if err := t.scratch(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		ErrTestsTimedOut)
	defer cancel()
	name, argv := t.command(args)
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Dir = t.Dir
	cmd.Env = append(t.env(), t.Env...)
	if slices.Contains(args, "-race") {
		// The race detector needs cgo.
		cmd.Env = append(cmd.Env, "CGO_ENABLED=1")
	}
	out := &capped{max: cmpInt(t.MaxOutput, 1<<20)}
	cmd.Stdout, cmd.Stderr = out, out
	group(cmd) // <1>
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	killGroup(cmd) // <2>
	return out.String(), runError(ctx, err, out.over)
}

// end::run[]

// command returns the command line: go test, or Command, with args,
// inside Wrap if there is one.
func (t TestRunner) command(args []string) (string, []string) {
	command := t.Command
	if len(command) == 0 {
		command = []string{"go", "test"}
	}
	name := command[0]
	argv := append(slices.Clone(command[1:]), args...)
	if t.Wrap != nil {
		name, argv = t.Wrap(name, argv...)
	}
	return name, argv
}

// scratch makes the run's private directories.
func (t TestRunner) scratch() error {
	for _, d := range []string{"home", "cache", "mod", "gopath", "tmp"} {
		if err := os.MkdirAll(filepath.Join(t.Scratch, d), 0o700); err != nil {
			return fmt.Errorf("run tests: %w", err)
		}
	}
	return nil
}

// runError turns how the command ended into the run's error. A failing
// test is a result, not an error.
func runError(ctx context.Context, err error, over bool) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("run tests: %w", context.Cause(ctx))
	case over:
		return fmt.Errorf("run tests: %w", ErrOutputCapped)
	}
	var exit *exec.ExitError
	switch {
	case err == nil, errors.As(err, &exit):
		return nil
	case errors.Is(err, exec.ErrWaitDelay):
		return nil // a test left a child holding the output; the group kill ended it
	}
	return fmt.Errorf("run tests: %w", err)
}

// ErrTestsTimedOut is the cause when a test run takes too long.
var ErrTestsTimedOut = errors.New("tests took too long")

func cmpInt(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// capped keeps the first max bytes written to it and drops the rest, so
// a test that prints forever can't fill memory.
type capped struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room < len(p) {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	n, err := c.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("capture output: %w", err)
	}
	return n, nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// tag::seatbelt[]

// Seatbelt returns a Wrap for macOS that runs the command under
// sandbox-exec with a deny-by-default profile, as OpenAI's Codex CLI
// does: test code may read the system's own files, the Go toolchain,
// the paths in read, the workspace, and the scratch directory, write
// only the last two, run programs, read the kernel values in sysctls,
// and use the network only on this machine's loopback address, which
// httptest servers need. Everything else is denied, including your home
// directory, other networks, and the system services a program would
// use to reach your keychain.
// Apple marks sandbox-exec deprecated; Codex CLI and Claude Code both
// still use it as their macOS sandbox.
func Seatbelt(dir, scratch string, read, fixed []string) (
	func(string, ...string) (string, []string), error) {
	paths := map[string]string{"dir": dir, "scratch": scratch}
	for i, r := range read {
		paths[fmt.Sprint("read", i)] = r
	}
	for k, p := range paths {
		abs, err := realPath(p) // <1>
		if err != nil {
			return nil, err
		}
		paths[k] = abs
	}
	profile := seatbeltProfile(paths) + denyWrites(paths["dir"], fixed)
	return func(name string, args ...string) (string, []string) {
		head := []string{"-p", profile, name}
		return "sandbox-exec", append(head, args...)
	}, nil
}

// end::seatbelt[]

// tag::seatbelt-profile[]

// systemReads are the system's own directories test code may read.
var systemReads = []string{"/usr", "/System", "/bin", "/sbin", "/dev",
	"/Library/Developer", "/Applications/Xcode.app",
	"/private/var/db/timezone"}

func seatbeltProfile(paths map[string]string) string {
	var reads strings.Builder
	for _, p := range slices.Concat(systemReads,
		slices.Sorted(maps.Values(paths))) {
		fmt.Fprintf(&reads, "\n  (subpath %q)", p)
	}
	return `(version 1)
(deny default)
(allow process-exec* process-fork file-map-executable)
` + sysctlRule() + `
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow file-read-metadata)
(allow file-read* (literal "/")` + reads.String() + `)
(allow file-write* (literal "/dev/null")` +
		fmt.Sprintf(" (subpath %q) (subpath %q))",
			paths["dir"], paths["scratch"]) + `
(allow file-ioctl (literal "/dev/null"))
(allow network-bind network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`
}

// end::seatbelt-profile[]

// tag::sysctls[]

// sysctls are the kernel values test code may read: those go test, the
// compiler, and the race detector read on macOS, found by running the
// sandbox tests with every read reported. A name ending in a dot is a
// prefix. Every other sysctl is denied, but the list doesn't stop test
// code reading another process's starting environment through sysctl:
// macOS answers that for any live process of the same user, whatever
// the profile says, so a secret at rung 3 belongs in a file, not in the
// environment of a process you run.
var sysctls = []string{
	"hw.machine", "hw.ncpu", "hw.optional.", "hw.pagesize_compat",
	"kern.bootargs", "kern.hostname", "kern.iossupportversion",
	"kern.ipc.somaxconn", "kern.maxfilesperproc",
	"kern.osproductversion", "kern.osrelease", "kern.ostype",
	"kern.osvariant_status", "kern.version",
	"security.mac.lockdown_mode_state",
}

// end::sysctls[]

// sysctlRule allows reading the sysctls, by name, and nothing else.
func sysctlRule() string {
	var b strings.Builder
	b.WriteString("(allow sysctl-read")
	for _, s := range sysctls {
		kind := "sysctl-name"
		if strings.HasSuffix(s, ".") {
			kind = "sysctl-name-prefix"
		}
		fmt.Fprintf(&b, "\n  (%s %q)", kind, s)
	}
	b.WriteString(")")
	return b.String()
}

// tag::deny-writes[]

// denyWrites returns the rules that make fixed, paths relative to the
// workspace dir, read-only inside it, such as the protected paths and
// the existing tests. A later rule wins over an earlier one, so these
// come after the rule that lets test code write the workspace.
func denyWrites(dir string, fixed []string) string {
	var b strings.Builder
	for _, f := range fixed {
		p, kind := under(dir, f), "literal"
		if strings.HasSuffix(f, "/") {
			kind = "subpath"
		}
		fmt.Fprintf(&b, "(deny file-write* (%s %q))\n", kind, p)
	}
	return b.String()
}

// end::deny-writes[]

// under returns the path of f, relative and slash-separated, in dir.
func under(dir, f string) string {
	return filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(f, "/")))
}

// realPath resolves symbolic links, since Seatbelt matches real paths:
// on macOS the temporary directory is under /var, a link to
// /private/var. A path given to the sandbox must be absolute and must
// exist, so a typo can't leave a rule that matches nothing.
func realPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("sandbox path %q: not absolute", p)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("sandbox path: %w", err)
	}
	return r, nil
}

// tag::bwrap[]

// Bubblewrap returns a Wrap for Linux that runs the command under
// bwrap: the root file system read-only, empty /tmp, /run, and home,
// then the workspace and scratch directory writable and the Go
// toolchain readable on top of them, and every namespace unshared,
// which takes away the network and puts the run in a process namespace
// of its own, so nothing it starts outlives it. Order matters: a later
// mount covers an earlier one, so the empty directories go first.
func Bubblewrap(dir, scratch, home, goroot string,
	fixed []string) func(string, ...string) (string, []string) {
	head := []string{"--ro-bind", "/", "/",
		"--tmpfs", "/tmp", "--tmpfs", "/run", "--tmpfs", home,
		"--dev", "/dev", "--proc", "/proc",
		"--ro-bind", goroot, goroot,
		"--bind", dir, dir, "--bind", scratch, scratch}
	for _, f := range fixed { // read-only on top of the workspace
		p := under(dir, f)
		head = append(head, "--ro-bind-try", p, p)
	}
	head = append(head, "--chdir", dir, "--unshare-all",
		"--die-with-parent", "--new-session", "--")
	return func(name string, args ...string) (string, []string) {
		argv := append(slices.Clone(head), name)
		return "bwrap", append(argv, args...)
	}
}

// end::bwrap[]

// tag::sandbox[]

// A Sandbox runs the triage tools' go test commands at a rung of the
// ladder: 2, the hardened runner, or 3, the runner inside the operating
// system's sandbox. Scratch is the run's private directory, shared by
// its test runs so the build cache stays warm.
type Sandbox struct {
	Rung    int
	Scratch string
	GoRoot  string // the toolchain, readable at rung 3

	// Fixed names what test code may not write at rung 3, relative to
	// the workspace: directories with a trailing slash, and files.
	Fixed []string
}

// end::sandbox[]

// NewSandbox returns a Sandbox at rung 3 where this system has one
// that the book's code builds, Seatbelt on macOS or bubblewrap on
// Linux, and at rung 2 elsewhere.
func NewSandbox(scratch, goroot string) Sandbox {
	s := Sandbox{Rung: 2, Scratch: scratch, GoRoot: goroot}
	tool := map[string]string{"darwin": "sandbox-exec",
		"linux": "bwrap"}[runtime.GOOS]
	if _, err := exec.LookPath(tool); tool != "" && err == nil {
		s.Rung = 3
	}
	return s
}

// tag::sandbox-test[]

// Test runs go test -json with args in dir, for at most timeout, and
// returns its output for the tools' report.
func (s Sandbox) Test(ctx context.Context, dir string,
	timeout time.Duration, args ...string) (string, error) {
	r := TestRunner{Dir: dir, Scratch: s.Scratch,
		Command: []string{"go", "test", "-json", "-count=1",
			fmt.Sprintf("-timeout=%s", timeout*3/4)}} // <1>
	if s.Rung >= 3 {
		wrap, err := s.wrap(dir)
		if err != nil {
			return "", err
		}
		r.Wrap = wrap
	}
	return r.Run(ctx, timeout, args...)
}

// end::sandbox-test[]

// wrap returns the rung-3 sandbox for a run in dir.
func (s Sandbox) wrap(dir string) (func(string, ...string) (string,
	[]string), error) {
	if err := os.MkdirAll(s.Scratch, 0o700); err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return Seatbelt(dir, s.Scratch, []string{s.GoRoot}, s.Fixed)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	return Bubblewrap(dir, s.Scratch, home, s.GoRoot, s.Fixed), nil
}

// tag::reads[]

// Reads says whether test code that this Sandbox runs in dir could read
// path: at rung 2, whatever you can; at rung 3, a path under one the
// profile or the bubblewrap command line leaves readable.
func (s Sandbox) Reads(dir, path string) (bool, error) {
	if s.Rung < 3 {
		return true, nil
	}
	p, err := realPath(path)
	if err != nil {
		return false, err
	}
	ours := []string{s.GoRoot, dir, s.Scratch}
	if runtime.GOOS == "darwin" {
		return p == "/" || Inside(p, slices.Concat(systemReads, ours)...), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("sandbox: %w", err)
	}
	hidden := []string{home, "/tmp", "/run"} // see Bubblewrap
	return Inside(p, ours...) || !Inside(p, hidden...), nil
}

// end::reads[]

// Inside says whether path is one of roots or inside one, comparing
// real paths; a root that doesn't exist holds nothing.
func Inside(path string, roots ...string) bool {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		p = filepath.Clean(path)
	}
	for _, r := range roots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, p)
		if err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
