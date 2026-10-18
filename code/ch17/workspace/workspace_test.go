package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T",
		"GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T",
		"GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo returns a repository with two commits of a.txt, "old" then
// "new", and the first commit's short hash.
func repo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	for _, text := range []string{"old", "new"} {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"),
			[]byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "-q", "-m", text)
	}
	return dir, run(t, dir, "rev-parse", "--short", "HEAD~1")
}

func TestAt(t *testing.T) {
	src, old := repo(t)
	mark := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(src, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+mark+"\n"),
		0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "ws")
	if err := At(t.Context(), src, old, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil || string(b) != "old" {
		t.Errorf("a.txt = %q, %v; want the crash's commit, old", b, err)
	}
	if fi, err := os.Lstat(filepath.Join(dst, ".git")); err != nil ||
		!fi.IsDir() {
		t.Errorf(".git: %v, want a directory of its own", err)
	}
	got, err := Commit(t.Context(), dst)
	if err != nil || !strings.HasPrefix(got, old) {
		t.Errorf("Commit = %q, %v; want %s...", got, err, old)
	}
	if _, err := os.Stat(mark); !errors.Is(err, os.ErrNotExist) {
		t.Error("the source repository's hook ran")
	}
	if err := At(t.Context(), src, old, dst); err == nil {
		t.Error("At over an existing directory succeeded")
	}
}

func TestAtRefuses(t *testing.T) {
	src, _ := repo(t)
	for _, commit := range []string{"HEAD", "-x", "main", "--upload-pack=x",
		"0123456"} {
		dst := filepath.Join(t.TempDir(), "ws")
		if err := At(t.Context(), src, commit, dst); err == nil {
			t.Errorf("At(%q) succeeded", commit)
		}
		if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("At(%q) left %s behind", commit, dst)
		}
	}
}

// TestAtRefName: a hash that is also a tag's name is refused, not
// taken as the tag.
func TestAtRefName(t *testing.T) {
	src, old := repo(t)
	run(t, src, "tag", old, "HEAD") // a tag named like the old commit
	dst := filepath.Join(t.TempDir(), "ws")
	if err := At(t.Context(), src, old, dst); err == nil {
		got, _ := Commit(t.Context(), dst)
		t.Errorf("At(%s) with a tag of that name checked out %s", old, got)
	}
}
