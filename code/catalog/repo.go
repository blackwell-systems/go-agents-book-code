package catalog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
)

// BuildRepo makes dst a git repository of remind whose history is the
// task's: it starts from remind with every commit undone, commits that
// as the starting point, then replays the task's commits, so the last
// commit is today's code. Hashes are the same on every machine.
func BuildRepo(ctx context.Context, t Task, remindDir, dst string) error {
	if err := os.CopyFS(dst, os.DirFS(remindDir)); err != nil {
		return fmt.Errorf("build repo: %w", err)
	}
	for _, c := range slices.Backward(t.History) {
		for _, e := range slices.Backward(c.Edits) {
			if err := Apply(dst, Edit{File: e.File, Old: e.New, New: e.Old}); err != nil {
				return fmt.Errorf("build repo: undo %q: %w", c.Message, err)
			}
		}
	}
	git := func(date string, args ...string) error {
		// No background maintenance: it would add and remove files in dst
		// while a caller copies it.
		cmd := exec.CommandContext(ctx, "git", append([]string{
			"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Remind Team", "GIT_AUTHOR_EMAIL=team@remind.example",
			"GIT_COMMITTER_NAME=Remind Team", "GIT_COMMITTER_EMAIL=team@remind.example",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build repo: git %v: %w: %s", args, err, out)
		}
		return nil
	}
	start := "2026-07-01T10:00:00Z"
	if len(t.History) > 0 {
		start = "2026-08-04T10:00:00Z"
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"commit", "-q", "-m", "remind baseline for the " + t.Version + " cycle"},
	}
	for _, s := range steps {
		if err := git(start, s...); err != nil {
			return err
		}
	}
	for _, c := range t.History {
		for _, e := range c.Edits {
			if err := Apply(dst, e); err != nil {
				return fmt.Errorf("build repo: replay %q: %w", c.Message, err)
			}
		}
		if err := git(c.Date, "add", "-A"); err != nil {
			return err
		}
		if err := git(c.Date, "commit", "-q", "-m", c.Message); err != nil {
			return err
		}
	}
	return nil
}
