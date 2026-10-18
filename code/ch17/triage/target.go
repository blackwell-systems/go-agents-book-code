package triage

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/safety"
)

// tag::target[]

// A Target is the service the agent triages (chapter 17). The zero
// Target is Remind, the book's practice service.
type Target struct {
	// Name is the service's name in the prompts and in the tools'
	// descriptions, in place of remind's.
	Name string `json:"name"`

	// Module is its Go module path. A stack line that starts with it
	// is one of the service's own frames: fingerprints, the gate's
	// panic check, and crash_read read only those.
	Module string `json:"module"`

	// Protected is what the agent may not change without a person.
	Protected safety.Protected `json:"-"`
}

// Remind is the book's practice service, with the values every earlier
// chapter used.
var Remind = Target{Name: "remind", Module: "example.com/remind",
	Protected: Protected}

// end::target[]

// AlwaysProtected is what every target protects, whatever its own list
// says: the files that decide what the build pulls in, and git's, which
// the history tools read.
var AlwaysProtected = []string{"go.mod", "go.sum", "go.work",
	"go.work.sum", "vendor/", ".git/"}

// tag::load-target[]

// LoadTarget reads a target from a JSON file: its module path, its
// name (the module path's last element if left out), and the paths it
// protects besides AlwaysProtected, such as
//
//	{"name": "billing", "module": "example.com/billing",
//	 "protected": ["auth/", "internal/payments/"]}
func LoadTarget(file string) (Target, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Target{}, fmt.Errorf("load target: %w", err)
	}
	var t struct {
		Target
		Protected []string `json:"protected"`
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return Target{}, fmt.Errorf("load target: %s: %w", file, err)
	}
	t.Name = cmp.Or(t.Name, path.Base(t.Module))
	t.Target.Protected = safety.Protected{Paths: slices.Concat(
		t.Protected, AlwaysProtected)}
	if err := t.check(); err != nil {
		return Target{}, fmt.Errorf("load target: %s: %w", file, err)
	}
	return t.Target, nil
}

// end::load-target[]

var (
	// What a name and a module path may hold: they go into prompts,
	// tool schemas, and regular expressions.
	plainName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	plainModule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~/-]*$`)
)

// check reports what is wrong with a target loaded from a file.
func (t Target) check() error {
	if !plainModule.MatchString(t.Module) || path.Clean(t.Module) !=
		t.Module {
		return fmt.Errorf("module %q: give the module path from go.mod",
			t.Module)
	}
	if !plainName.MatchString(t.Name) {
		return fmt.Errorf("name %q: give a short name, such as billing",
			t.Name)
	}
	for _, p := range t.Protected.Paths {
		c := path.Clean(p)
		if c == "." || c == ".." || path.IsAbs(c) ||
			strings.HasPrefix(c, "../") || p != c && p != c+"/" {
			return fmt.Errorf("protected %q: give a path relative to "+
				"the module root, with a trailing slash for a "+
				"directory", p)
		}
	}
	return nil
}

// orRemind returns t, or Remind for the zero Target.
func (t Target) orRemind() Target {
	if t.Module == "" {
		return Remind
	}
	return t
}

// target returns the service the run triages: Remind, if Target is
// unset, as in an Env built without NewEnv.
func (e *Env) target() Target { return e.Target.orRemind() }

// prefix starts every stack line of the service's own code.
func (t Target) prefix() string { return t.orRemind().Module + "/" }

// remindWord is how the prompts and descriptions name the service.
var remindWord = regexp.MustCompile(`\bremind\b`)

// Text returns text written for remind, such as a prompt, with the
// target's name in place of remind's. For Remind it returns text
// unchanged, so remind's runs and their recordings stay the same.
func (t Target) Text(text string) string {
	t = t.orRemind()
	if t.Name == Remind.Name {
		return text
	}
	return remindWord.ReplaceAllLiteralString(text, t.Name)
}

// named returns tl with the target's name in its description and in
// its schema's descriptions.
func (t Target) named(tl agent.Tool) agent.Tool {
	tl.Def.Description = t.Text(tl.Def.Description)
	tl.Def.Parameters = []byte(t.Text(string(tl.Def.Parameters)))
	return tl
}
