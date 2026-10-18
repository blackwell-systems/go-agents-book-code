// Package workspace makes a run's copy of a repository at the commit
// that crashed, not the latest one: an "already fixed" crash happened
// on an old build, and only that build's code shows it (chapter 17).
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var hexCommit = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// tag::at[]

// At makes dst a clone of the repository at repo with commit checked
// out, detached. The clone copies the objects rather than hard-linking
// them, since test code in the workspace could otherwise change the
// source repository's files; it gets no hooks (an empty template, hooks
// off); and the commit is resolved as a commit, never as a branch or
// tag with the same name.
func At(ctx context.Context, repo, commit, dst string) error {
	if !hexCommit.MatchString(commit) {
		return fmt.Errorf("workspace: %q is not a commit hash", commit)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: %s already exists", dst)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := checkout(ctx, repo, commit, dst)
	if err != nil {
		_ = os.RemoveAll(dst) // half made: nothing to keep
	}
	return err
}

// end::at[]

// tag::checkout[]

// checkout clones repo into dst without hard links and checks out the
// commit, refusing a name that is also a branch or tag.
func checkout(ctx context.Context, repo, commit, dst string) error {
	if _, err := git(ctx, "clone", "--quiet", "--no-checkout",
		"--no-hardlinks", "--template=", "--", repo, dst); err != nil {
		return err
	}
	refs, err := git(ctx, "-C", dst, "show-ref")
	if ee, ok := errors.AsType[*exec.ExitError](err); err != nil &&
		(!ok || ee.ExitCode() != 1) { // 1 is "no refs"
		return err
	}
	for line := range strings.Lines(refs) {
		if strings.HasSuffix(strings.TrimSpace(line), "/"+commit) {
			return fmt.Errorf("workspace: %s is also a branch or tag "+
				"name; give the full commit hash", commit)
		}
	}
	full, err := git(ctx, "-C", dst, "rev-parse", "--verify",
		"--end-of-options", commit+"^{commit}")
	if err != nil {
		return err
	}
	_, err = git(ctx, "-C", dst, "checkout", "--quiet", "--detach",
		strings.TrimSpace(full))
	return err
}

// end::checkout[]

// git runs git with hooks off and no user or system configuration,
// and returns its standard output.
func git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
	}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	cmd.WaitDelay = time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		what := args[0]
		if what == "-C" && len(args) > 2 {
			what = args[2]
		}
		return "", fmt.Errorf("workspace: git %s: %w: %s", what, err,
			strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Commit returns the commit dst has checked out, for a check after a
// resume that the workspace is still at the crash's commit.
func Commit(ctx context.Context, dst string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dst, "rev-parse", "HEAD")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(dst)}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
