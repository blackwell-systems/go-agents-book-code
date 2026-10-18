package triage

import (
	"context"
	"fmt"
	"go/format"
	"path"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch07/agent"
)

// tag::patch-args[]

type patchArgs struct {
	Path string `json:"path" desc:"the file to change, such as store/store.go"`
	Old  string `json:"old" desc:"text copied exactly from the file, appearing in it once; empty to create a new file"`
	New  string `json:"new" desc:"the text to put in its place, or the new file's contents"`
}

// end::patch-args[]

// tag::code-patch[]

func codePatch(e *Env) agent.Tool {
	return agent.NewTool("code_patch",
		"Change a file by replacing one piece of text, or "+
			"create a new file, such as a test, by leaving old "+
			"empty. Read a file with code_read before changing it, "+
			"and copy old exactly from what you read. After a "+
			"change, the package and its tests are compiled, and "+
			"you see the result.",
		func(ctx context.Context, a patchArgs) (string, error) {
			p, err := clean(a.Path)
			if err != nil {
				return "", err
			}
			if a.Old == "" {
				return e.create(ctx, p, a.New)
			}
			if !e.hasRead(p) { // <1>
				return "", fmt.Errorf("read %s with code_read before "+
					"changing it, so you change "+
					"what is really there", p)
			}
			// end::code-patch[]
			// tag::code-patch-match[]
			data, err := e.root.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("no file %q", p)
			}
			src := string(data)
			switch n := strings.Count(src, a.Old); {
			case n == 0:
				return "", noMatch(p, src, a.Old) // <1>
			case n > 1:
				return "", fmt.Errorf("the old text appears "+
					"%d times in "+
					"%s; include more of the "+
					"lines around it, so it appears once", n, p)
			}
			at := strings.Index(src, a.Old)
			edited := src[:at] + a.New + src[at+len(a.Old):]
			return e.write(ctx, p, edited, at, len(a.New))
		})
}

// end::code-patch-match[]

// tag::write[]

// write saves a changed file and reports on it: the changed lines, and
// whether the package and its tests still compile. A Go file that
// wouldn't parse isn't written at all; one that parses is formatted
// with gofmt's rules first.
func (e *Env) write(ctx context.Context, p, src string, at, n int) (
	string, error) {
	first := strings.Count(src[:at], "\n") // the changed lines
	last := strings.Count(src[:at+n], "\n")
	if strings.HasSuffix(p, ".go") {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			return "", fmt.Errorf("the change would leave %s "+
				"unable to "+
				"parse, so it wasn't made: %v", p, err)
		}
		src = string(formatted)
	}
	if err := e.root.WriteFile(p, []byte(src), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", p, err)
	}
	// end::write[]
	// tag::write-report[]
	var b strings.Builder
	fmt.Fprintf(&b, "changed %s; it now reads:\n%s\n",
		p, region(src, first, last))
	report, err := agent.GoTest(ctx, e.Dir, 2*time.Minute,
		"-run", "^$", "./"+path.Dir(p)) // <1>
	if err != nil {
		return "", fmt.Errorf("compile %s: %w", path.Dir(p), err)
	}
	fmt.Fprintf(&b, "compiling %s and its tests:\n%s",
		path.Dir(p), report)
	return b.String(), nil
}

// end::write-report[]

// create makes a new file, such as a reproducer test. It refuses to
// overwrite a file, which would lose what is there; code_patch changes
// existing files.
func (e *Env) create(ctx context.Context, p, content string) (string, error) {
	if _, err := e.root.Stat(p); err == nil {
		return "", fmt.Errorf("%s already exists; to change it, read it "+
			"and give the text to replace as old", p)
	}
	dir := path.Dir(p)
	if info, err := e.root.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("no directory %q; new files go in an "+
			"existing package's directory", dir)
	}
	e.markCreated(p)
	return e.write(ctx, p, content, 0, len(content))
}

// region shows lines first to last of src (counting from 0), with two
// lines either side, numbered. Formatting rarely moves the lines of an
// edit, so the numbers counted before it are close enough.
func region(src string, first, last int) string {
	lines := strings.Split(src, "\n")
	last = min(last, len(lines)-1)
	var b strings.Builder
	for i := max(first-2, 0); i <= min(last+2, len(lines)-1); i++ {
		fmt.Fprintf(&b, "%5d\t%s\n", i+1, lines[i])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// tag::no-match[]

// noMatch explains a failed match. Most misses are whitespace, such as
// spaces where the file has a tab, so it finds the lines that match
// once whitespace is ignored and shows both versions with tabs and
// spaces made visible.
func noMatch(p, src, old string) error {
	want := strings.Split(old, "\n")
	lines := strings.Split(src, "\n")
	for i := range lines {
		if i+len(want) > len(lines) {
			break
		}
		if !sameIgnoringSpace(lines[i:i+len(want)], want) {
			continue
		}
		return fmt.Errorf("the old text isn't in %s exactly, but lines "+
			"%d-%d match it except for whitespace (tabs shown as →, "+
			"spaces at the start and end of lines as ·):\nfile:\n%s\nyours:\n%s\n"+
			"copy the text exactly as code_read shows it",
			p, i+1, i+len(want), visible(lines[i:i+len(want)]), visible(want))
	}
	return fmt.Errorf("the old text isn't in %s; read the file again "+
		"and copy the lines you want to change exactly", p)
}

// end::no-match[]

func sameIgnoringSpace(a, b []string) bool {
	for i := range a {
		if strings.Join(strings.Fields(a[i]), " ") !=
			strings.Join(strings.Fields(b[i]), " ") {
			return false
		}
	}
	return true
}

// visible shows the whitespace at the ends of each line.
func visible(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		body := strings.TrimSpace(l)
		lead := l[:strings.Index(l+"x", strings.TrimLeft(l+"x", " \t"))]
		trail := l[len(lead)+len(body):]
		show := strings.NewReplacer("\t", "→", " ", "·")
		fmt.Fprintf(&b, "  %s%s%s\n", show.Replace(lead), body, show.Replace(trail))
	}
	return strings.TrimSuffix(b.String(), "\n")
}
