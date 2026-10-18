package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// duration loads the chapter's duration project, with its bug.
func duration(t *testing.T) FS {
	t.Helper()
	fs := FS{}
	for _, name := range []string{"duration.go", "duration_test.go"} {
		b, err := os.ReadFile("../testdata/duration/" + name)
		if err != nil {
			t.Fatal(err)
		}
		fs[name] = string(b)
	}
	return fs
}

func TestRunTests(t *testing.T) {
	fixed := duration(t)
	fixed["duration.go"] = strings.Replace(fixed["duration.go"],
		"'m': time.Second,", "'m': time.Minute,", 1)
	tests := []struct {
		name string
		fs   FS
		want []string // all in the report
		not  []string // none in the report
	}{
		{"failing", duration(t),
			[]string{"--- FAIL: TestParse", `Parse("1h30m") = 1h0m30s`, "FAIL\tproject"},
			[]string{"=== RUN", `"Action"`}},
		{"passing", fixed, []string{"ok  \tproject"}, []string{"FAIL"}},
		{"build error", FS{"a.go": "package a\n\nfunc f() { x }\n"},
			[]string{"undefined: x", "FAIL"}, nil},
		{"no API key", FS{"a_test.go": `package a

import ("os"; "testing")

func TestKey(t *testing.T) {
	if os.Getenv("OPENROUTER_API_KEY") != "" { t.Fatal("key visible") }
}
`}, []string{"ok  \tproject"}, nil},
	}
	t.Setenv("OPENROUTER_API_KEY", "secret")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := goTest(t.Context(), tt.fs, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("report lacks %q:\n%s", w, got)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(got, n) {
					t.Errorf("report has %q:\n%s", n, got)
				}
			}
		})
	}
}

func TestRunTestsLimits(t *testing.T) {
	t.Parallel() // most of it is waiting for a hanging test
	// A test that never finishes is stopped by go test's own -timeout,
	// and reported like any other failure.
	hang := FS{"a_test.go": `package a

import ("testing"; "time")

func TestHang(t *testing.T) { time.Sleep(time.Hour) }
`}
	// Compile and link the hanging test first, under a generous
	// deadline, so the timed run below finds it in the build cache.
	// That run gets two minutes, so go test's own -timeout is 90 s:
	// a slow go command startup under load can't use it up, and the
	// hang is still reported by name.
	buildTest(t, hang)
	got, err := goTest(t.Context(), hang, 2*time.Minute)
	if err != nil || !strings.Contains(got, "test timed out") {
		t.Errorf("hanging test: %q, %v", got, err)
	}

	// With no time at all, the tool's own deadline fires.
	_, err = goTest(t.Context(), hang, time.Millisecond)
	if !errors.Is(err, ErrTestsTimedOut) {
		t.Errorf("err = %v, want ErrTestsTimedOut", err)
	}

	// A test that prints a lot is cut to maxReport.
	loud := FS{"a_test.go": `package a

import ("strings"; "testing")

func TestLoud(t *testing.T) { t.Fatal(strings.Repeat("x", 100000)) }
`}
	got, err = goTest(t.Context(), loud, time.Minute)
	if err != nil || len(got) > maxReport+100 ||
		!strings.Contains(got, "more bytes not shown") {
		t.Errorf("loud test: %d bytes, %v", len(got), err)
	}
}

// buildTest compiles fs's test binary with go test -c, discarding it,
// so a later goTest of the same files starts from the build cache.
func buildTest(t *testing.T, fs FS) {
	t.Helper()
	dir := t.TempDir()
	if err := withGoMod(fs).WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", os.DevNull)
	cmd.Dir, cmd.Env = dir, testEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
}

func TestWriteDirStaysInDir(t *testing.T) {
	err := FS{"../escape.go": "package x"}.WriteDir(t.TempDir())
	if err == nil {
		t.Error("WriteDir wrote outside its directory")
	}
}

func TestReportOrderAndPaths(t *testing.T) {
	// A test that exits partway never reports a result of its own; its
	// output still comes before the package's result line, and the
	// temporary directory's path is gone.
	exits := FS{"a_test.go": `package a

import ("os"; "testing")

func TestExit(t *testing.T) { t.Log("about to exit"); os.Exit(3) }
`}
	got, err := goTest(t.Context(), exits, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(got, "about to exit"), strings.Index(got, "FAIL\tproject")
	if i < 0 || j < 0 || i > j {
		t.Errorf("want the test's output before the result:\n%s", got)
	}
	if strings.Contains(got, "run_tests-") {
		t.Errorf("report shows the temporary directory:\n%s", got)
	}
}
