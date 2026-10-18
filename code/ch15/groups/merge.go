package groups

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// A file is one file's content in a tree; nil means it doesn't exist.
type file = []byte

// changed returns the files that differ between the original tree and
// a group's workspace, with the workspace's content.
func changed(original, workspace string) (map[string]file, error) {
	paths := map[string]bool{}
	for _, dir := range []string{original, workspace} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && d.Name() == ".git":
				return filepath.SkipDir
			case d.IsDir():
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return fmt.Errorf("compare trees: %w", err)
			}
			paths[rel] = true
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("compare trees: %w", err)
		}
	}
	out := map[string]file{}
	for rel := range paths {
		before, err := read(original, rel)
		if err != nil {
			return nil, err
		}
		after, err := read(workspace, rel)
		if err != nil {
			return nil, err
		}
		// An empty file and a missing one are both empty bytes, so
		// check that the file exists in both trees too.
		if !bytes.Equal(before, after) || (before == nil) != (after == nil) {
			out[rel] = after
		}
	}
	return out, nil
}

// apply merges one group's changes into the tree at dir, which starts
// as a copy of the original and gathers every group's changes. A file
// no earlier group changed is copied; a file an earlier group changed
// too is merged three ways. If any file conflicts, or the tree doesn't
// compile with the result, none of this group's changes are applied,
// and the reason is returned.
// tag::apply-merge[]
func apply(ctx context.Context, dir, original string,
	changes map[string]file) error {
	if len(changes) == 0 {
		return nil
	}
	merged := map[string]file{}
	for _, rel := range slices.Sorted(maps.Keys(changes)) {
		base, err := read(original, rel)
		if err != nil {
			return err
		}
		ours, err := read(dir, rel)
		if err != nil {
			return err
		}
		theirs := changes[rel]
		switch {
		case bytes.Equal(ours, base) || bytes.Equal(ours, theirs):
			merged[rel] = theirs // no one else changed it, or the same way
		case ours == nil || theirs == nil:
			return fmt.Errorf("%s: %w", rel, errConflict)
		default:
			if merged[rel], err = mergeFile(ctx, ours, base, theirs); err != nil { // <1>
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
	}
	// end::apply-merge[]
	// tag::apply-write[]
	before := map[string]file{}
	for _, rel := range slices.Sorted(maps.Keys(merged)) { // <1>
		var err error
		if before[rel], err = read(dir, rel); err != nil {
			return err
		}
		if err := write(dir, rel, merged[rel]); err != nil {
			return errors.Join(err, restore(dir, before))
		}
	}
	if err := builds(ctx, dir); err != nil { // <2>
		return errors.Join(err, restore(dir, before))
	}
	return nil
}

// end::apply-write[]

// tag::builds[]

// errNoBuild means a group's changes merged as text, but the tree no
// longer compiles with them: two groups each declared a test or helper
// with the same name in one package, say.
var errNoBuild = errors.New("the merged code doesn't compile with these changes")

// builds compiles every package's tests in the tree, running none.
func builds(ctx context.Context, dir string) error {
	out, err := agent.GoTest(ctx, dir, 3*time.Minute, "-run", "^$", "./...")
	if err != nil {
		return fmt.Errorf("build the merged tree: %w", err)
	}
	if strings.Contains(out, "[build failed]") ||
		strings.Contains(out, "[setup failed]") {
		return fmt.Errorf("%w:\n%s", errNoBuild, out)
	}
	return nil
}

// end::builds[]

// restore puts files back as they were before a group's changes.
func restore(dir string, before map[string]file) error {
	var errs []error
	for rel, content := range before {
		errs = append(errs, write(dir, rel, content))
	}
	return errors.Join(errs...)
}

// errConflict means two groups changed the same lines of a file.
var errConflict = errors.New("another group changed the same lines")

// tag::merge-file[]

// mergeFile merges theirs into ours, given the base both started from,
// with git merge-file, which exits with the number of conflicts.
func mergeFile(ctx context.Context, ours, base, theirs file) (file, error) {
	dir, err := os.MkdirTemp("", "merge-")
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	defer os.RemoveAll(dir) // a temporary directory; nothing to report
	var paths []string
	for i, b := range []file{ours, base, theirs} {
		p := filepath.Join(dir, fmt.Sprint(i))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return nil, fmt.Errorf("merge: %w", err)
		}
		paths = append(paths, p)
	}
	out, err := exec.CommandContext(ctx, "git",
		append([]string{"merge-file", "-p"}, paths...)...).Output()
	if e, ok := errors.AsType[*exec.ExitError](err); ok &&
		e.ExitCode() > 0 && e.ExitCode() < 128 {
		return nil, errConflict
	}
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	return out, nil
}

// end::merge-file[]

// read returns a file's content in a tree, or nil if it isn't there.
func read(dir, rel string) (file, error) {
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	return b, nil
}

// write sets a file's content in a tree; nil content removes it.
func write(dir, rel string, content file) error {
	p := filepath.Join(dir, rel)
	if content == nil {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("merge: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return fmt.Errorf("merge: %w", err)
	}
	return nil
}
