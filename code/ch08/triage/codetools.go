package triage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
)

// Limits on how much one call returns, so a big file or a broad search
// can't fill the context window. Each says how to ask for the rest.
const (
	maxLines   = 200 // code_read
	maxMatches = 40  // code_search, crash_search
	maxFiles   = 300 // code_list
)

// clean checks a path from the model and returns it in the form the
// workspace uses: relative to remind's module root, with slashes.
func clean(p string) (string, error) {
	p = path.Clean(strings.TrimPrefix(p, "./"))
	if path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%q is outside the workspace; "+
			"paths are relative to remind's module root, such as store/store.go", p)
	}
	return p, nil
}

type listArgs struct {
	Dir string `json:"dir,omitzero" desc:"a directory such as \"store\"; empty lists everything"`
}

func codeList(e *Env) agent.Tool {
	return agent.NewTool("code_list",
		"List the files under a directory of remind's source, with how "+
			"many lines each has. Use it to find your way around; use "+
			"code_search to find where something is defined.",
		func(_ context.Context, a listArgs) (string, error) {
			dir, err := clean(a.Dir)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			n := 0
			err = fs.WalkDir(e.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() && d.Name() == ".git" {
					return fs.SkipDir
				}
				if d.IsDir() {
					return nil
				}
				if n++; n <= maxFiles {
					data, err := e.root.ReadFile(p)
					if err != nil {
						return fmt.Errorf("read file: %w", err)
					}
					fmt.Fprintf(&b, "%s (%d lines)\n", p, strings.Count(string(data), "\n"))
				}
				return nil
			})
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("no directory %q; try an empty dir to list everything", dir)
			}
			if err != nil {
				return "", fmt.Errorf("code_list: %w", err)
			}
			if n > maxFiles {
				fmt.Fprintf(&b, "... and %d more files; list a smaller directory\n", n-maxFiles)
			}
			return b.String(), nil
		})
}

// tag::read-args[]

type readArgs struct {
	Path  string `json:"path" desc:"a file path, such as store/store.go"`
	Start int    `json:"start,omitzero" desc:"the first line to show; default 1"`
	End   int    `json:"end,omitzero" desc:"the last line to show; default 200 lines after start"`
}

// end::read-args[]

func codeRead(e *Env) agent.Tool {
	return agent.NewTool("code_read",
		"Read part of a file of remind's source, with line numbers. "+
			"Shows at most 200 lines at a time; ask for a range with "+
			"start and end. Read a file before you change it.",
		func(_ context.Context, a readArgs) (string, error) {
			p, err := clean(a.Path)
			if err != nil {
				return "", err
			}
			data, err := e.root.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("no file %q; use code_list or "+
					"code_search to find it", p)
			}
			// tag::code-read[]
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			start := max(a.Start, 1)
			end := min(len(lines), start+maxLines-1)
			if a.End > 0 {
				end = min(end, a.End)
			}
			if start > end {
				return "", fmt.Errorf("%s has %d lines; start is %d",
					p, len(lines), start)
			}
			e.markRead(p) // <1>
			var b strings.Builder
			fmt.Fprintf(&b, "%s, lines %d-%d of %d:\n", p, start, end, len(lines))
			for i := start; i <= end; i++ {
				fmt.Fprintf(&b, "%5d\t%s\n", i, lines[i-1])
			}
			if end < len(lines) { // <2>
				fmt.Fprintf(&b, "... %d more lines; read on with start=%d\n",
					len(lines)-end, end+1)
			}
			return b.String(), nil
			// end::code-read[]
		})
}

type searchArgs struct {
	Pattern string `json:"pattern" desc:"a Go regular expression, such as func \\(s \\*Store\\) Get"`
	Dir     string `json:"dir,omitzero" desc:"limit the search to this directory"`
}

func codeSearch(e *Env) agent.Tool {
	return agent.NewTool("code_search",
		"Search remind's Go files for a regular expression and list the "+
			"matching lines with file and line number. Use it to find "+
			"where a function is defined or called.",
		func(_ context.Context, a searchArgs) (string, error) {
			re, err := regexp.Compile(a.Pattern)
			if err != nil {
				return "", fmt.Errorf("bad pattern: %v; it is a Go regular "+
					"expression, so escape ( ) [ ] * . with a backslash", err)
			}
			dir, err := clean(a.Dir)
			if err != nil {
				return "", err
			}
			var hits []string
			n := 0
			err = fs.WalkDir(e.root.FS(), dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
					return err
				}
				data, err := e.root.ReadFile(p)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}
				for i, line := range strings.Split(string(data), "\n") {
					if re.MatchString(line) {
						if n++; n <= maxMatches {
							hits = append(hits, fmt.Sprintf("%s:%d: %s", p, i+1,
								strings.TrimSpace(line)))
						}
					}
				}
				return nil
			})
			if err != nil {
				return "", fmt.Errorf("code_search: %w", err)
			}
			if n == 0 {
				return "no matches", nil
			}
			out := strings.Join(hits, "\n")
			if n > maxMatches {
				out += fmt.Sprintf("\n... %d more "+
					"matches; narrow the pattern or give a dir", n-maxMatches)
			}
			return out, nil
		})
}
