// Package history reads a workspace's git history for the agent: which
// commits touched a file or a function, and which commit last changed
// each line. It only reads, and it caps what it returns (chapter 17).
package history

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits on one call, so a long history can't flood the context.
const (
	maxCommits = 20       // commits one log shows
	maxLines   = 200      // lines one blame shows, as code_read does
	maxOutput  = 16 << 10 // bytes of output kept
	maxRaw     = 1 << 20  // bytes read from git before formatting
	timeout    = 10 * time.Second
)

// ErrNoHistory means the workspace isn't a git repository of its own.
var ErrNoHistory = errors.New("history: the workspace has no git " +
	"history")

// A Repo is a workspace whose git history the agent may read.
type Repo struct {
	dir string

	// original, if set, is an untouched copy of the workspace: the
	// parts of .git that tell git to read other files must match it.
	original string
}

// tag::open[]

// Open returns the repository at dir. The .git directory must be a
// real directory inside dir: a .git file or a link could point git at
// another repository, and with no .git at all, git would search the
// parent directories and find one there.
func Open(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	fi, err := os.Lstat(filepath.Join(abs, ".git"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ErrNoHistory
	case err != nil:
		return nil, fmt.Errorf("open history: %w", err)
	case !fi.IsDir(): // <1>
		return nil, fmt.Errorf("%w: .git is not a directory",
			ErrNoHistory)
	}
	return &Repo{dir: abs}, nil
}

// end::open[]

// tag::git[]

// git runs one read-only git command in the repository and returns its
// output, cut at limit bytes with a note saying so.
func (r *Repo) git(ctx context.Context, limit int,
	args ...string) (string, error) {
	if err := r.unchanged(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"--no-pager", "-C", r.dir,
		"-c", "core.fsmonitor=false", // <1>
		"-c", "log.showSignature=false",
		"-c", "core.hooksPath=" + os.DevNull,
	}, args...)...)
	cmd.Env = []string{ // <2>
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(r.dir),
		"GIT_OPTIONAL_LOCKS=0", // a read takes no lock
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}

	// end::git[]

	out, stderr := &capped{max: limit}, &capped{max: 2 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", args[0],
				context.Cause(ctx))
		}
		// Not git's own message: it can quote a file that repository
		// config pointed git at (chapter 17's review).
		return "", fmt.Errorf("git %s failed (%v); check the file and "+
			"the range", args[0], exitCode(err))
	}
	return out.String(), nil
}

// tag::log[]

// Log lists the newest commits that changed file, or the whole tree if
// file is empty, newest first: short hash, date, and subject. With a
// function as well, it shows each commit's change to that function.
func (r *Repo) Log(ctx context.Context, file, function string,
	n int) (string, error) {
	n = min(max(n, 1), maxCommits)
	args := []string{"log", "--no-ext-diff", "--no-textconv",
		"--date=short", "--format=%h %ad %s", "-n", strconv.Itoa(n)}

	// end::log[]

	// tag::log-args[]

	switch {
	case function != "" && file == "":
		return "", errors.New("give the file the function is in, " +
			"as well as the function")
	case function != "":
		re, err := funcPattern(function)
		if err != nil {
			return "", err
		}
		p, err := clean(file)
		if err != nil {
			return "", err
		}
		args = append(args, "-L", ":"+re+":"+p) // <1>
	case file != "":
		p, err := clean(file)
		if err != nil {
			return "", err
		}
		args = append(args, "--end-of-options", "HEAD", "--", p)
	}
	out, err := r.git(ctx, maxOutput, args...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "no commits changed " + cmp.Or(file, "the tree"), nil
	}
	return out, nil
}

// end::log-args[]

// tag::blame[]

// Blame shows, for each line from start to end of file as last
// committed, the commit that last changed it: short hash, date, and
// subject. It reads the committed file, never the workspace's copy, so
// no filter configured for the working tree runs.
func (r *Repo) Blame(ctx context.Context, file string, start,
	end int) (string, error) {
	p, err := clean(file)
	if err != nil {
		return "", err
	}
	start = max(start, 1)
	if end < start {
		end = start + maxLines - 1
	}
	end = min(end, start+maxLines-1)
	out, err := r.git(ctx, maxRaw, "blame", "--line-porcelain",
		"--no-textconv", "-L", strconv.Itoa(start)+","+strconv.Itoa(end),
		"HEAD", "--", p) // <1>
	if err != nil {
		return "", err
	}
	return formatBlame(out), nil
}

// end::blame[]

// formatBlame turns git's line-porcelain output into one line per
// source line: number, commit, date, subject, and the code.
func formatBlame(porcelain string) string {
	b := &capped{max: maxOutput}
	var hash, date, subject, line string
	for l := range strings.Lines(porcelain) {
		l = strings.TrimSuffix(l, "\n")
		switch f := strings.Fields(l); {
		case strings.HasPrefix(l, "\t"):
			fmt.Fprintf(b, "%4s %.7s %-10s %-40.40s | %s\n", line, hash,
				date, subject, l[1:])
		case header.MatchString(l):
			hash, line = f[0], f[2]
		case strings.HasPrefix(l, "author-time "):
			date = unixDate(f[1])
		case strings.HasPrefix(l, "summary "):
			subject = strings.TrimPrefix(l, "summary ")
		}
	}
	if strings.HasSuffix(porcelain, cutNote) {
		b.cut = true
	}
	return b.String()
}

// header matches the line that starts each source line's record:
// the commit, the line's number in it, and its number now.
var header = regexp.MustCompile(`^[0-9a-f]{40,64} \d+ \d+`)

func unixDate(s string) string {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.DateOnly)
}

// tag::clean[]

// clean checks a path the model gave: relative to the workspace, inside
// it, not in .git, and not something git would read as an option.
func clean(p string) (string, error) {
	p = path.Clean(strings.TrimPrefix(p, "./"))
	switch {
	case p == "." || p == "":
		return "", errors.New("give a file, such as store/store.go")
	case path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../"):
		return "", fmt.Errorf("%q is outside the workspace; paths "+
			"are relative to the module root, such as "+
			"store/store.go", p)
	case strings.HasPrefix(p, "-"), strings.ContainsAny(p, ":\x00\n"):
		return "", fmt.Errorf("%q is not a file path", p)
	case p == ".git" || strings.HasPrefix(p, ".git/"):
		return "", errors.New("the .git directory can't be read")
	}
	return p, nil
}

// end::clean[]

var goName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// funcPattern turns a Go function name, such as List, Store.List, or
// (*Store).List, into the pattern git log -L uses to find the line
// where the function starts. Brackets stand for parentheses, so the
// pattern means the same as a basic or an extended expression.
func funcPattern(name string) (string, error) {
	name = name[strings.LastIndexByte(name, '.')+1:]
	if !goName.MatchString(name) {
		return "", fmt.Errorf("%q is not a Go function name; give a "+
			"name such as List or (*Store).List", name)
	}
	return "^func.*[ )]" + name + "[[(]", nil
}

const cutNote = "[output cut; ask for fewer commits or a shorter " +
	"range]\n"

// capped keeps the first max bytes written to it and drops the rest,
// adding a note if it dropped anything, so the command can run to the
// end without its output growing.
type capped struct {
	buf bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.cut = true
	}
	c.buf.Write(p[:max(0, min(room, len(p)))])
	return len(p), nil
}

func (c *capped) String() string {
	if c.cut {
		return c.buf.String() + "\n" + cutNote
	}
	return c.buf.String()
}

// OpenChecked is Open, with original as the untouched copy that the
// parts of .git which name other files are checked against before each
// call (chapter 17).
func OpenChecked(dir, original string) (*Repo, error) {
	r, err := Open(dir)
	if err != nil {
		return nil, err
	}
	r.original = original
	return r, nil
}

// tag::unchanged[]

// watched are the parts of .git that make git read files of their
// choosing: config (include.path, blame.ignoreRevsFile), the common
// directory, info/ (attributes, excludes), and alternates.
var watched = []string{"config", "commondir", "info",
	filepath.Join("objects", "info", "alternates")}

// unchanged refuses to run git once any watched part of .git differs
// from the untouched copy's: test code or an approved change could
// have pointed git at a file the model must not see.
func (r *Repo) unchanged() error {
	if r.original == "" {
		return nil
	}
	for _, w := range watched {
		a, aerr := snapshot(filepath.Join(r.dir, ".git", w))
		b, berr := snapshot(filepath.Join(r.original, ".git", w))
		if aerr != nil || berr != nil || a != b {
			return fmt.Errorf("the history tools are off: .git/%s was "+
				"changed in the workspace", filepath.ToSlash(w))
		}
	}
	return nil
}

// end::unchanged[]

// snapshot is a file's or directory's contents as one string, names
// included, or "" if it doesn't exist.
func snapshot(p string) (string, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err //nolint:wrapcheck // unchanged refuses on any error
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(p)
		return fi.Mode().String() + "\n" + string(b), err //nolint:wrapcheck // as above
	}
	var b strings.Builder
	err = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(q)
		fmt.Fprintf(&b, "%s\n%s\n", strings.TrimPrefix(q, p), data)
		return err //nolint:wrapcheck // unchanged refuses on any error
	})
	return b.String(), err //nolint:wrapcheck // as above
}

// exitCode is git's exit status, or the error's kind when it didn't
// exit, such as a timeout.
func exitCode(err error) string {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	return "it didn't finish"
}
