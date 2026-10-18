// Package safety holds chapter 14's defenses while the chapter is being
// written: protected paths, the approval gate, the audit log, checks on
// text that leaves the run, marking untrusted text, and the sandboxed
// test runner. They are folded into the agent and triage packages once
// chapter 13's code is copied forward.
package safety

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// tag::protected[]

// Protected names the parts of a workspace the agent may not change
// without a person's approval: directories, written with a trailing
// slash, and files, slash-separated and relative to the workspace root.
// Everything in a protected directory is protected, new tests too;
// elsewhere, existing tests are protected and new test files are
// allowed, since the reproducer gate needs them.
type Protected struct {
	Paths []string

	// Original, if set, is the workspace as it was before the agent
	// changed it: a test is existing if it is there, so the agent can
	// still edit a test it wrote itself. Empty means the workspace.
	Original string
}

// A Violation is one change to a protected part of the workspace.
type Violation struct {
	Path string // relative to the workspace, slash-separated
	Why  string
}

// end::protected[]

// ErrOutside means a path leads out of the workspace, ErrIndirect that
// it gets there indirectly, through ".." or a symbolic link, and
// ErrNotPlain that it has letters a file system might fold onto
// others. A write tool refuses all three: no fix needs them, and each
// is a way to reach a file under another name.
var (
	ErrOutside  = errors.New("path is outside the workspace")
	ErrIndirect = errors.New(`path uses ".." or goes through a symbolic link`)
	ErrNotPlain = errors.New("path has characters other than ASCII " +
		"letters, digits, and ._-/")
)

// tag::check[]

// Check reports whether writing name in the workspace dir would change
// a protected part of it. It refuses names that could reach a file
// under another name, so the name it judges is where the write lands.
func (p Protected) Check(dir, name string) (Violation, bool, error) {
	info, err := direct(dir, name) // <1>
	if err != nil {
		return Violation{}, false, err
	}
	v := Violation{Path: filepath.ToSlash(filepath.Clean(name))}
	switch {
	case p.matches(v.Path) != "":
		v.Why = "protected: " + p.matches(v.Path)
	case isTest(v.Path) && p.existed(v.Path, info):
		v.Why = "an existing test" // <2>
	case isTest(v.Path):
		return Violation{}, false, nil // a new test is allowed
	case info != nil && links(info) > 1:
		v.Why = "a file with more than one name (a hard link)" // <3>
	default:
		return Violation{}, false, nil
	}
	return v, true, nil
}

// end::check[]

// existed reports whether a test was in the workspace before the agent
// started: in Original, if set, or else now, in info.
func (p Protected) existed(name string, info fs.FileInfo) bool {
	if p.Original == "" {
		return info != nil
	}
	_, err := os.Lstat(filepath.Join(p.Original, filepath.FromSlash(name)))
	return err == nil
}

// tag::direct[]

// direct checks that name reaches its file directly: plain, relative,
// with no ".." part and no symbolic link anywhere along it. It returns
// the file's information, or nil if the file doesn't exist yet.
func direct(dir, name string) (fs.FileInfo, error) {
	if err := plain(name); err != nil { // <1>
		return nil, err
	}
	parts := strings.Split(name, "/")
	if slices.Contains(parts, "..") {
		return nil, fmt.Errorf("%s: %w", name, ErrIndirect)
	}
	at := dir
	for i, part := range parts {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil // the rest doesn't exist yet
		case err != nil:
			return nil, fmt.Errorf("check %s: %w", name, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("%s: %w", name, ErrIndirect)
		case i == len(parts)-1:
			return info, nil
		}
	}
	return nil, nil
}

// end::direct[]

// tag::plain[]

// plain refuses a name that isn't relative and made of ASCII letters,
// digits, and "._-/" alone. No fix needs anything else, and a file
// system that folds case may fold other letters too: on macOS, "ſ"
// (U+017F) opens a file named with "s", so go.ſum is go.sum.
func plain(name string) error {
	local := filepath.IsLocal(filepath.FromSlash(name))
	if filepath.IsAbs(name) || !local {
		return fmt.Errorf("%s: %w", DisplayPath(name), ErrOutside)
	}
	for _, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) ||
			strings.ContainsRune("._-/", r)
		if r > unicode.MaxASCII || !ok {
			return fmt.Errorf("%s: %w", DisplayPath(name), ErrNotPlain)
		}
	}
	return nil
}

// end::plain[]

// tag::shadows[]

// Shadows says why new test source could keep a package's other tests
// from running, or returns "": a TestMain decides whether they run at
// all, and an init or an exit can end the test binary before they do.
// It looks for the text only, so a person decides the rare honest case.
func Shadows(src string) string {
	for _, s := range []string{"func TestMain(", "func init(",
		"os.Exit(", "syscall.Exit("} {
		if strings.Contains(src, s) {
			return "a test with " + strings.TrimSuffix(s, "(") +
				", which can stop the package's other tests"
		}
	}
	return ""
}

// end::shadows[]

// Covers reports whether a slash-separated path is protected, as a
// check of what the policy keeps a run from changing (chapter 17's
// grading).
func (p Protected) Covers(name string) bool { return p.matches(name) != "" }

// matches returns the protected entry a slash-separated path falls
// under, or "". It ignores case: on a file system that ignores case,
// AUTH/x.go is auth/x.go, and on one that doesn't, refusing it costs a
// fix nothing.
func (p Protected) matches(name string) string {
	lower := strings.ToLower(name)
	for _, e := range p.Paths {
		le := strings.ToLower(e)
		if strings.HasSuffix(le, "/") && strings.HasPrefix(lower, le) || lower == le {
			return e
		}
	}
	return ""
}

func isTest(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "_test.go")
}

// tag::diff[]

// Diff compares the workspace with the original and returns each change
// to a protected part of it, however the agent made it: protected files
// changed, existing tests changed or deleted, new links of either kind,
// special files, and changes to git's hooks or configuration, which run
// code on the next git command.
func (p Protected) Diff(orig, work string) ([]Violation, error) {
	before, err := snapshot(orig)
	if err != nil {
		return nil, err
	}
	after, err := snapshot(work)
	if err != nil {
		return nil, err
	}
	return p.compare(before, after), nil
}

// end::diff[]

// compare returns the violations between two snapshots, sorted.
func (p Protected) compare(before, after map[string]entry) []Violation {
	var out []Violation
	for name, a := range after {
		b, existed := before[name]
		if why := p.changed(name, a, b, existed); why != "" {
			out = append(out, Violation{name, why})
		}
	}
	out = append(out, p.deleted(before, after)...)
	slices.SortFunc(out, func(x, y Violation) int {
		return strings.Compare(x.Path, y.Path)
	})
	return out
}

// tag::changed[]

// changed says why one file's change is a violation, or "".
func (p Protected) changed(name string, a, b entry,
	existed bool) string {
	same := existed && a == b
	switch {
	case same:
		return ""
	case strings.HasPrefix(name, ".git/"):
		return "git hooks or configuration changed"
	case a.kind == "link" && (!existed || b.kind != "link"):
		return "a new symbolic link"
	case a.kind == "special":
		return "a special file (a FIFO, socket, or device)"
	case a.links > 1 && (!existed || b.links == 1):
		return "a new hard link"
	case p.matches(name) != "":
		return "protected: changed"
	case existed && isTest(name):
		return "an existing test changed"
	}
	return ""
}

// end::changed[]

// deleted returns the protected files and existing tests that are gone.
func (p Protected) deleted(before, after map[string]entry) []Violation {
	var out []Violation
	for name := range before {
		if _, ok := after[name]; !ok && (p.matches(name) != "" || isTest(name)) {
			out = append(out, Violation{name, "deleted"})
		}
	}
	return out
}

// entry is one file in a snapshot: its kind, the hash of its content or
// the target of a link, its permission bits, and how many names it has.
type entry struct {
	kind  string // "file", "link", or "special"
	sum   string
	mode  fs.FileMode
	links uint64
}

// gitWatched is what snapshot reads inside .git: the parts that make
// git run code or change what it checks out, not its objects.
func gitWatched(rel string) bool {
	return rel == ".git" || rel == ".git/config" || rel == ".git/hooks" ||
		strings.HasPrefix(rel, ".git/hooks/") || rel == ".git/info" ||
		rel == ".git/info/attributes"
}

// snapshot hashes every file under dir, and the watched parts of .git.
func snapshot(dir string) (map[string]entry, error) {
	out := map[string]entry{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git") && !gitWatched(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		e, err := describe(path, d)
		if err != nil {
			return err
		}
		out[rel] = e
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", dir, err)
	}
	return out, nil
}

// describe makes one snapshot entry, hashing a file's content as it
// reads it, so a large file doesn't have to fit in memory.
func describe(path string, d fs.DirEntry) (entry, error) {
	info, err := d.Info()
	if err != nil {
		return entry{}, fmt.Errorf("stat: %w", err)
	}
	e := entry{mode: info.Mode().Perm(), links: links(info)}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return entry{}, fmt.Errorf("read link: %w", err)
		}
		e.kind, e.sum = "link", target
	case !info.Mode().IsRegular():
		e.kind = "special"
	default:
		f, err := os.Open(path)
		if err != nil {
			return entry{}, fmt.Errorf("open: %w", err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return entry{}, fmt.Errorf("read: %w", err)
		}
		e.kind, e.sum = "file", fmt.Sprintf("%x", h.Sum(nil))
	}
	return e, nil
}
