package safety

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspace builds a small copy of remind's layout: an auth package
// with a test, a store package with a test, and go.mod.
func workspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":              "module example.com/remind\n\ngo 1.27\n",
		"auth/auth.go":        "package auth\n",
		"auth/auth_test.go":   "package auth\n",
		"store/store.go":      "package store\n",
		"store/store_test.go": "package store\n",
		"api/api.go":          "package api\n",
	} {
		write(t, dir, name, body)
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var remindProtected = Protected{Paths: []string{"auth/", "api/api.go",
	"go.mod", "go.sum"}}

// caseInsensitive reports whether dir's file system ignores case, as
// macOS's does by default.
func caseInsensitive(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "GO.MOD"))
	return err == nil
}

func verdict(dir, path string) string {
	v, hit, err := remindProtected.Check(dir, path)
	switch {
	case errors.Is(err, ErrOutside):
		return "outside the workspace"
	case errors.Is(err, ErrIndirect):
		return "indirect"
	case errors.Is(err, ErrNotPlain):
		return "not plain"
	case err != nil:
		return "error: " + err.Error()
	case hit:
		return v.Why
	}
	return "allowed"
}

// TestProtectedTable is the chapter's table of edits and verdicts. The
// workspace has a relative link, store/a, that leads to auth/, as an
// earlier write could have planted, and a hard link, store/h.go, to
// auth/auth.go.
func TestProtectedTable(t *testing.T) {
	dir := workspace(t)
	if err := os.Symlink("../auth", filepath.Join(dir, "store", "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "auth", "auth.go"),
		filepath.Join(dir, "store", "h.go")); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, want string }{
		{"store/store.go", "allowed"},
		{"store/new.go", "allowed"},
		{"auth/regress_test.go", "protected: auth/"},
		{"store/regress_test.go", "allowed"},
		{"auth/auth.go", "protected: auth/"},
		{"auth/new.go", "protected: auth/"},
		{"store/store_test.go", "an existing test"},
		{"auth/auth_test.go", "protected: auth/"},
		{"go.mod", "protected: go.mod"},
		{"go.sum", "protected: go.sum"},
		{"api/api.go", "protected: api/api.go"},
		{"AUTH/auth.go", "protected: auth/"},
		{"Go.Mod", "protected: go.mod"},
		{"store/a/auth.go", "indirect"},
		{"store/a/../auth/auth.go", "indirect"},
		{"store/../auth/auth.go", "indirect"},
		{"store/h.go", "a file with more than one name (a hard link)"},
		{"../outside.go", "outside the workspace"},
		{"/etc/passwd", "outside the workspace"},
		// U+017F folds to s on APFS, so these open go.sum and an
		// existing test there.
		{"go.\u017fum", "not plain"},
		{"store/store_te\u017ft.go", "not plain"},
		{"auth/new_test.go", "protected: auth/"},
	}
	if caseInsensitive(dir) {
		cases = append(cases, struct{ path, want string }{"store/store_test.GO",
			"an existing test"})
	}
	for _, c := range cases {
		got := verdict(dir, c.path)
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.path, got, c.want)
		}
		if testing.Verbose() {
			fmt.Printf("%-26s %s\n", c.path, got)
		}
	}
}

// TestProtectedMissingDir checks a protected directory that doesn't
// exist yet: a file under it is still protected.
func TestProtectedMissingDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module m\n")
	if got := verdict(dir, "auth/new.go"); got != "protected: auth/" {
		t.Errorf("auth/new.go with no auth/: %s", got)
	}
}

func copyTree(t *testing.T, orig string) string {
	t.Helper()
	work := t.TempDir()
	if err := os.CopyFS(work, os.DirFS(orig)); err != nil {
		t.Fatal(err)
	}
	return work
}

// TestDiff checks the after-the-fact check on what changed in the
// workspace, whatever path the change was made through.
func TestDiff(t *testing.T) {
	orig := workspace(t)
	write(t, orig, ".git/config", "[core]\n")
	work := copyTree(t, orig)
	write(t, work, "store/store.go", "package store // fixed\n")
	write(t, work, "auth/regress_test.go", "package auth\n")
	write(t, work, "auth/auth.go", "package auth // no more checks\n")
	write(t, work, "go.mod", "module example.com/remind\n\ngo 1.27\n\nreplace x => ../x\n")
	write(t, work, "store/store_test.go", "package store // skipped\n")
	write(t, work, ".git/config", "[core]\n\tfsmonitor = ./run-me\n")
	write(t, work, ".git/hooks/pre-commit", "#!/bin/sh\n")
	if err := os.Remove(filepath.Join(work, "auth", "auth_test.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../auth", filepath.Join(work, "store", "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(work, "api", "api.go"),
		filepath.Join(work, "store", "h.go")); err != nil {
		t.Fatal(err)
	}
	got, err := remindProtected.Diff(orig, work)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, v := range got {
		lines = append(lines, v.Path+": "+v.Why)
	}
	want := []string{
		".git/config: git hooks or configuration changed",
		".git/hooks/pre-commit: git hooks or configuration changed",
		"api/api.go: a new hard link",
		"auth/auth.go: protected: changed",
		"auth/auth_test.go: deleted",
		"auth/regress_test.go: protected: changed",
		"go.mod: protected: changed",
		"store/a: a new symbolic link",
		"store/h.go: a new hard link",
		"store/store_test.go: an existing test changed",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(lines, "\n"),
			strings.Join(want, "\n"))
	}
}

// TestProtectedOriginal checks that with Original set, a test the agent
// wrote during the run may be changed, and one that was there may not.
func TestProtectedOriginal(t *testing.T) {
	orig, work := workspace(t), workspace(t)
	write(t, work, "store/new_test.go", "package store\n")
	p := remindProtected
	p.Original = orig
	for name, want := range map[string]bool{
		"store/new_test.go": false, "store/store_test.go": true} {
		if _, hit, err := p.Check(work, name); err != nil || hit != want {
			t.Errorf("%s: protected %v, %v; want %v", name, hit, err, want)
		}
	}
}
