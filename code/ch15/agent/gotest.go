package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// testTimeout is how long run_tests may take. go test gets a shorter
// -timeout of its own, so a hung test is reported by go test, with the
// stacks of its goroutines, before the tool gives up.
const testTimeout = 2 * time.Minute

// maxReport caps what run_tests sends back, so a test that prints
// megabytes can't fill the model's context.
const maxReport = 8 << 10

// ErrTestsTimedOut means go test ran past the tool's deadline.
var ErrTestsTimedOut = errors.New("tests timed out")

// tag::go-test[]

// goTest copies the project to a temporary directory and runs go test
// there. Failing tests and compile errors are results, returned as
// text; only a timeout or a missing go command is an error.
func goTest(ctx context.Context, fs FS, timeout time.Duration) (
	string, error) {
	dir, err := os.MkdirTemp("", "run_tests-")
	if err != nil {
		return "", fmt.Errorf("run_tests: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := withGoMod(fs).WriteDir(dir); err != nil { // <1>
		return "", fmt.Errorf("run_tests: %w", err)
	}
	return GoTest(ctx, dir, timeout, "./...")
}

// end::go-test[]

// tag::go-test-run[]

// GoTest runs go test in dir with args, such as a package pattern, and
// returns a report the model can read. The triage tools (chapter 7)
// call it on the agent's workspace.
func GoTest(ctx context.Context, dir string, timeout time.Duration,
	args ...string) (string, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, ErrTestsTimedOut)
	defer cancel()
	inner := fmt.Sprintf("-timeout=%s", timeout*3/4) // <1>
	cmd := exec.CommandContext(ctx, "go", append([]string{"test", "-json",
		"-count=1", inner}, args...)...)
	cmd.Dir = dir
	cmd.Env = testEnv() // <2>
	if slices.Contains(args, "-race") {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=1") // the race detector needs cgo
	}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil { // <3>
		err := fmt.Errorf("go test: %w", ctx.Err())
		if cause := context.Cause(ctx); !errors.Is(err, cause) {
			err = fmt.Errorf("%w: %w", err, cause)
		}
		return "", err
	}
	_, exited := errors.AsType[*exec.ExitError](err)
	if err != nil && !exited {
		return "", fmt.Errorf("run_tests: %w", err) // <4>
	}
	if err != nil && stdout.Len() == 0 { // <5>
		return "", fmt.Errorf("run_tests: %s",
			strings.TrimSpace(stderr.String()))
	}
	return report(&stdout, stderr.String(), dir), nil
}

// end::go-test-run[]

// tag::test-env[]

// testEnv is the environment go test runs with: only what the go
// command needs, so the code under test can't read your API key, and
// settings that keep it offline and away from your other projects.
func testEnv() []string {
	env := []string{
		"GOWORK=off", "GOPROXY=off", "GOFLAGS=", "GOENV=off",
		"GOTOOLCHAIN=local", "CGO_ENABLED=0",
	}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE",
		"GOPATH", "GOMODCACHE", "XDG_CACHE_HOME", // Linux cache
		"USERPROFILE", "LOCALAPPDATA", "TMP", "TEMP", // Windows
		"GOROOT"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// end::test-env[]

// withGoMod returns the files plus a go.mod, if they don't have one.
func withGoMod(fs FS) FS {
	out := FS{"go.mod": "module project\n\ngo 1.22\n"}
	maps.Copy(out, fs)
	return out
}

// isResult reports whether a line is a package's result line.
func isResult(line string) bool {
	return line == "PASS\n" || line == "FAIL\n" ||
		strings.HasPrefix(line, "ok  \t") || strings.HasPrefix(line, "FAIL\t")
}

// clean removes what changes from run to run: timings, and the path of
// the temporary directory, which would also show the model your home
// directory.
func clean(s, dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		s = strings.ReplaceAll(s, resolved+string(filepath.Separator), "")
	}
	s = strings.ReplaceAll(s, dir+string(filepath.Separator), "")
	return strings.Trim(timings.ReplaceAllString(s, ""), "\n")
}

// timings matches the durations go test prints, such as "(0.00s)" and
// "\t0.326s". They change on every run and tell the model nothing, so
// the report leaves them out, and the same code gives the same report.
var timings = regexp.MustCompile(`(?m)( \(\d+\.\d+s\)|\t\d+\.\d+s)$`)

// A testEvent is one line of go test -json output.
type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

// tag::report[]

// report turns go test -json output into what a person would want to
// read: the output of failed tests, build errors, and each package's
// result, without the output of tests that passed or the path of the
// temporary directory.
func report(stdout io.Reader, stderr, dir string) string {
	var b, results strings.Builder
	pending := map[string][]string{} // output of tests still running
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var ev testEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			b.WriteString(sc.Text() + "\n") // not JSON: keep it as is
			continue
		}
		switch {
		case ev.Test == "" && isResult(ev.Output):
			results.WriteString(ev.Output) // ok, FAIL: written last
		case ev.Test == "":
			b.WriteString(ev.Output) // build errors
		case ev.Action == "output" && !strings.HasPrefix(ev.Output, "=== "):
			pending[ev.Test] = append(pending[ev.Test], ev.Output)
		case ev.Action == "fail":
			b.WriteString(strings.Join(pending[ev.Test], ""))
			delete(pending, ev.Test)
		case ev.Action == "pass" || ev.Action == "skip":
			delete(pending, ev.Test)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(pending)) {
		b.WriteString(strings.Join(pending[name], "")) // never finished
	}
	b.WriteString(stderr + results.String())
	return limit(clean(b.String(), dir), maxReport)
}

// end::report[]

// TestReport turns go test -json output from a runner other than
// GoTest, such as chapter 14's sandbox, into run_tests' report; dir is
// where it ran, which the report leaves out.
func TestReport(output, dir string) string {
	return report(strings.NewReader(output), "", dir)
}

// limit cuts s to at most n bytes, saying how much was cut.
func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n-- // don't cut a character in half
	}
	return s[:n] + fmt.Sprintf("\n[%d more bytes not shown]", len(s)-n)
}
