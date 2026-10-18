package history

import (
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
)

// gitIn runs git in dir with a fixed author and no user configuration.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE=2026-08-01T10:00:00Z",
		"GIT_COMMITTER_DATE=2026-08-01T10:00:00Z",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo returns a repository with n commits to a.go.
func newRepo(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	for i := range n {
		write(t, dir, "a.go", "package a\n\nfunc F() int { return "+
			strconv.Itoa(i)+" }\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", "change "+strconv.Itoa(i))
	}
	return dir
}

// h1 builds the catalog's h1-paging-commit workspace, whose history has
// the commit that dropped the clamp on offset.
func h1(t *testing.T) (*Repo, catalog.Task) {
	t.Helper()
	tasks, err := catalog.Load("../../catalog/testdata/tasks")
	if err != nil {
		t.Fatal(err)
	}
	var task catalog.Task
	for _, tk := range tasks {
		if tk.ID == "h1-paging-commit" {
			task = tk
		}
	}
	dir := filepath.Join(t.TempDir(), "remind")
	if err := catalog.BuildRepo(t.Context(), task, "../../remind",
		dir); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, task
}

func TestH1(t *testing.T) {
	r, task := h1(t)
	want := task.Groups["A"].IntroducedBy
	ctx := t.Context()

	log, err := r.Log(ctx, "store/store.go", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, want) {
		t.Errorf("Log(store/store.go) doesn't name %q:\n%s", want, log)
	}
	if strings.Contains(log, "api: cap page size") {
		t.Errorf("Log(store/store.go) lists a commit to api/api.go:\n%s",
			log)
	}

	fn, err := r.Log(ctx, "store/store.go", "(*Store).List", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fn, "-\tif offset > len(out) {") {
		t.Errorf("Log of (*Store).List doesn't show the clamp removed:\n%s",
			fn)
	}

	blame, err := r.Blame(ctx, "store/store.go", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for l := range strings.Lines(blame) {
		if strings.Contains(l, "end := min(offset+limit") {
			found = true
			if !strings.Contains(l, want) {
				t.Errorf("blame of the paging line: %s, want %q", l, want)
			}
		}
	}
	t.Logf("log:\n%s\nfunction:\n%s", log, fn)
	if !found && !strings.Contains(blame, "output cut") {
		t.Errorf("blame of lines 1 to 200 has no paging line:\n%s", blame)
	}
}

func TestBlameFindsLine(t *testing.T) {
	r, task := h1(t)
	data, err := os.ReadFile(filepath.Join(r.dir, "store", "store.go"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, "end := min(offset+limit") {
			n = i + 1
		}
	}
	got, err := r.Blame(t.Context(), "store/store.go", n, n)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("blame line %d: %s", n, got)
	want := strconv.Itoa(n) + " "
	if !strings.HasPrefix(strings.TrimSpace(got), want) ||
		!strings.Contains(got, task.Groups["A"].IntroducedBy) ||
		strings.Count(got, "\n") != 1 {
		t.Errorf("Blame(line %d) = %q", n, got)
	}
}

func TestLimits(t *testing.T) {
	r, err := Open(newRepo(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Log(t.Context(), "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n != maxCommits {
		t.Errorf("Log with limit 100 showed %d commits, want %d", n,
			maxCommits)
	}

	c := &capped{max: 10}
	_, _ = c.Write([]byte("0123456789")) // never fails
	if c.cut {
		t.Error("capped cut output that fits")
	}
	_, _ = c.Write([]byte("x"))
	if !c.cut || !strings.HasPrefix(c.String(), "0123456789\n[output cut") {
		t.Errorf("capped = %q, cut %v", c.String(), c.cut)
	}
}

func TestBadArguments(t *testing.T) {
	r, err := Open(newRepo(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, p := range []string{"", ".", "../x", "/etc/passwd",
		"--output=pwned", "-p", ".git/config", "a.go:1", "a\x00b"} {
		if _, err := r.Log(ctx, p, "F", 1); err == nil {
			t.Errorf("Log(%q, F) accepted", p)
		}
		if _, err := r.Blame(ctx, p, 1, 1); err == nil {
			t.Errorf("Blame(%q) accepted", p)
		}
	}
	for _, f := range []string{"F(", "F:a.go", "--output=x", "a b",
		"(*S).", "F$"} {
		if _, err := r.Log(ctx, "a.go", f, 1); err == nil {
			t.Errorf("Log(a.go, %q) accepted", f)
		}
	}
	if _, err := r.Log(ctx, "", "F", 1); err == nil {
		t.Error("Log with a function and no file accepted")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "pwned")); err == nil {
		t.Error("an argument wrote a file")
	}
}

// TestHostileConfig gives the workspace a repository configured to run
// commands from diffs, signatures, and filters, as a workspace someone
// else prepared might be: no read runs any of them.
func TestHostileConfig(t *testing.T) {
	dir := newRepo(t, 2)
	mark := filepath.Join(t.TempDir(), "ran")
	cmd := "sh -c 'touch " + mark + "'"
	write(t, dir, ".gitattributes", "* diff=evil filter=evil\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "attributes")
	for k, v := range map[string]string{
		"diff.external":          cmd,
		"diff.evil.textconv":     cmd,
		"diff.evil.command":      cmd,
		"filter.evil.clean":      cmd,
		"filter.evil.smudge":     cmd,
		"log.showSignature":      "true",
		"gpg.program":            cmd,
		"core.pager":             cmd,
		"core.fsmonitor":         cmd,
		"blame.ignoreRevsFile":   "",
		"core.hooksPath":         dir,
		"diff.evil.xfuncname":    "^func",
		"blame.markIgnoredLines": "true",
	} {
		gitIn(t, dir, "config", k, v)
	}
	write(t, dir, "a.go", "package a\n\nfunc F() int { return 99 }\n")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := r.Log(ctx, "a.go", "F", 5); err != nil {
		t.Errorf("Log: %v", err)
	}
	if _, err := r.Log(ctx, "a.go", "", 5); err != nil {
		t.Errorf("Log: %v", err)
	}
	if _, err := r.Blame(ctx, "a.go", 1, 3); err != nil {
		t.Errorf("Blame: %v", err)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("a configured command ran")
	}
}

func TestOpen(t *testing.T) {
	plain := t.TempDir()
	if _, err := Open(plain); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Open(no .git) = %v, want ErrNoHistory", err)
	}
	file := t.TempDir()
	write(t, file, ".git", "gitdir: "+newRepo(t, 1)+"/.git\n")
	if _, err := Open(file); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Open(.git file) = %v, want ErrNoHistory", err)
	}
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(newRepo(t, 1), ".git"),
		filepath.Join(link, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Open(.git link) = %v, want ErrNoHistory", err)
	}
}

// TestIntroducedByField checks the proposed Diagnosis field's JSON
// name against the catalog's: the run's answer reaches the grader by
// JSON (taskrun's convert), so a different spelling would be dropped.
func TestIntroducedByField(t *testing.T) {
	type diagnosis struct { // the field chapter 17 adds to triage.Diagnosis
		IntroducedBy string `json:"introduced_by,omitzero"`
	}
	b, err := json.Marshal(diagnosis{IntroducedBy: "9c41e7a"})
	if err != nil {
		t.Fatal(err)
	}
	var d catalog.Diagnosis
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.IntroducedBy != "9c41e7a" {
		t.Errorf("catalog.Diagnosis.IntroducedBy = %q from %s",
			d.IntroducedBy, b)
	}
}

// TestConfigLeak: repository config can name a file for git to read,
// and git quotes its first line in an error. The tool refuses once the
// watched parts of .git differ from the untouched copy, and its errors
// never quote git.
func TestConfigLeak(t *testing.T) {
	dir := newRepo(t, 2)
	orig := filepath.Join(t.TempDir(), "orig")
	if err := os.CopyFS(orig, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "reviewers")
	write(t, filepath.Dir(secret), "reviewers", "ana review-token-ana-0123\n")
	gitIn(t, dir, "config", "blame.ignoreRevsFile", secret)

	checked, err := OpenChecked(dir, orig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = checked.Blame(t.Context(), "a.go", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("Blame with changed config: %v", err)
	}
	plain, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = plain.Blame(t.Context(), "a.go", 1, 1)
	if err == nil || strings.Contains(err.Error(), "review-token") {
		t.Errorf("git's error reached the model: %v", err)
	}
	if out, err := (&Repo{dir: dir, original: dir}).Log(t.Context(),
		"a.go", "", 1); err != nil || out == "" {
		t.Errorf("unchanged config: %q, %v", out, err)
	}
}
