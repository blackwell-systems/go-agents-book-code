package main

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// target is the platform every size is measured for, so the numbers
// don't depend on the machine that runs compare: the code a package
// compiles, and the binary it makes, differ by GOOS and GOARCH.
var target = []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}

// shared are the packages every version compiles, left out of the
// counts so that they measure what each version adds.
var shared = []string{
	"github.com/blackwell-systems/go-agents-book/code/ch16/task",
	"github.com/blackwell-systems/go-agents-book/code/ch12/replay",
}

// book are the book's own modules.
var book = []string{
	"github.com/blackwell-systems/go-agents-book/code",
	"github.com/blackwell-systems/go-agents-book/code/ch16",
}

// goCommand runs the go command for target, in root. It is the one in
// $GOROOT/bin when GOROOT is set; otherwise the go on PATH, which under
// go test is the toolchain running the test, since go test puts its
// own GOROOT/bin first.
func goCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	tool := "go"
	if g := os.Getenv("GOROOT"); g != "" {
		tool = filepath.Join(g, "bin", "go")
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), target...)
	return cmd
}

// A module is one module's share of what a version compiles.
type module struct {
	Path  string
	Lines int
}

// A footprint is what a version compiles beneath its own package.
type footprint struct {
	Lines     int      // lines of code, as count counts them
	Generated int      // of them, in generated files
	Modules   []module // the modules other than the book's, largest first
}

// tag::compiled[]

// compiled counts the code that pkg compiles for target beneath its own
// package: every package outside the standard library, except pkg and
// the shared ones, grouped by module.
func compiled(ctx context.Context, root, pkg string) (footprint, error) {
	var fp footprint
	out, err := goCommand(ctx, root, "list", "-deps", "-f",
		"{{if not .Standard}}{{.ImportPath}} {{.Module.Path}} {{.Dir}} "+
			"{{join .GoFiles \",\"}}{{end}}", pkg).Output()
	if err != nil {
		return fp, fmt.Errorf("list packages of %s: %w", pkg, err)
	}
	self, err := filepath.Abs(filepath.Join(root, pkg))
	if err != nil {
		return fp, fmt.Errorf("find %s: %w", pkg, err)
	}
	byModule := map[string]int{}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] == self || slices.Contains(shared, f[0]) {
			continue
		}
		for name := range strings.SplitSeq(f[3], ",") {
			n, gen, err := count(filepath.Join(f[2], name))
			if err != nil {
				return fp, err
			}
			fp.Lines += n
			if gen {
				fp.Generated += n
			}
			byModule[f[1]] += n
		}
	}
	for path, n := range byModule {
		if !slices.Contains(book, path) {
			fp.Modules = append(fp.Modules, module{path, n})
		}
	}
	slices.SortFunc(fp.Modules, func(a, b module) int {
		return cmp.Or(b.Lines-a.Lines, strings.Compare(a.Path, b.Path))
	})
	return fp, nil
}

// end::compiled[]

// lines counts the code in the non-test Go files of dir.
func lines(dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return 0, fmt.Errorf("count lines: %w", err)
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		c, _, err := count(f)
		if err != nil {
			return 0, err
		}
		n += c
	}
	return n, nil
}

// generated matches the comment that marks a generated Go file.
var generated = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// count counts a file's lines of code, leaving out blank lines and
// lines that are only a comment, and reports whether the file says it
// was generated.
func count(file string) (n int, gen bool, err error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, false, fmt.Errorf("count lines: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if generated.MatchString(line) {
			gen = true
		}
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		n++
	}
	if err := s.Err(); err != nil {
		return 0, false, fmt.Errorf("count lines of %s: %w", file, err)
	}
	return n, gen, nil
}

// binary builds pkg for target and returns the size of the binary.
func binary(ctx context.Context, root, pkg string) (int64, error) {
	dir, err := os.MkdirTemp("", "ch16-size-")
	if err != nil {
		return 0, fmt.Errorf("measure binary: %w", err)
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "bin")
	cmd := goCommand(ctx, root, "build", "-trimpath", "-o", exe, pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("build %s: %w\n%s", pkg, err, out)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return 0, fmt.Errorf("measure binary: %w", err)
	}
	return info.Size(), nil
}

// sizes builds each version's command for target and prints the sizes
// as an AsciiDoc table row, headed by the Go version and platform they
// were measured with. A binary's size depends on the Go release, so
// the row is recorded in testdata/sizes.adoc rather than checked by a
// test.
func sizes(ctx context.Context, out io.Writer, root string) error {
	ver, err := goCommand(ctx, root, "env", "GOVERSION").Output()
	if err != nil {
		return fmt.Errorf("find the go version: %w", err)
	}
	goVersion := strings.TrimSpace(string(ver))
	var b strings.Builder
	fmt.Fprintf(&b, "// Recorded by go run ./compare -sizes with %s,\n"+
		"// for %s, with -trimpath.\n"+
		"// Rerun it after a Go or framework upgrade.\n\n"+
		"| Binary size, linux/amd64 (%s)", goVersion,
		strings.Join(target, " "), goVersion)
	for _, v := range versions {
		n, err := binary(ctx, root, "./cmd/"+v.Dir)
		if err != nil {
			return fmt.Errorf("measure %s: %w", v.Name, err)
		}
		fmt.Fprintf(&b, "\n| %.1f MB", float64(n)/1e6)
	}
	b.WriteString("\n")
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("write sizes: %w", err)
	}
	return nil
}
