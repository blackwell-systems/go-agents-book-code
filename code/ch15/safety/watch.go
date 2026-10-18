package safety

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// tag::watch[]

// Watch returns middleware for the named tools, which run test code:
// it compares the workspace dir before and after each call and stops
// the run if the call changed a protected part of it, other than the
// files the call itself targets, whose change went through the gate.
// It catches what the sandbox's read-only rules miss, as at rung 2.
func (p Protected) Watch(dir string, names ...string) agent.ToolWare {
	return func(t agent.Tool) agent.Tool {
		if !slices.Contains(names, t.Def.Name) {
			return t
		}
		run, targets := t.Run, t.Targets
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			before, err := snapshot(dir)
			if err != nil {
				return "", fmt.Errorf("%w: %w", agent.ErrToolFatal, err)
			}
			out, runErr := run(ctx, args)
			if err := p.since(dir, before, targets, args); err != nil {
				return "", err
			}
			return out, runErr
		}
		return t
	}
}

// end::watch[]

// since returns an error wrapping agent.ErrToolFatal if dir has a
// change to a protected part since before that isn't one of the call's
// own targets.
func (p Protected) since(dir string, before map[string]entry,
	targets func(jsontext.Value) ([]string, error),
	args jsontext.Value) error {
	after, err := snapshot(dir)
	if err != nil {
		return fmt.Errorf("%w: %w", agent.ErrToolFatal, err)
	}
	var own []string
	if targets != nil {
		own, _ = targets(args) // a call whose targets fail never ran
	}
	for _, v := range p.compare(before, after) {
		if !slices.Contains(own, v.Path) {
			return fmt.Errorf("%w: test code changed %s: %s",
				agent.ErrToolFatal, DisplayPath(v.Path), v.Why)
		}
	}
	return nil
}

// tag::fixed[]

// Fixed lists what test code may not write in a copy of the workspace
// orig: every protected path, and every test that is there. It is the
// list a Sandbox makes read-only.
func (p Protected) Fixed(orig string) ([]string, error) {
	out := slices.Clone(p.Paths)
	err := filepath.WalkDir(orig, func(path string, d fs.DirEntry,
		err error) error {
		if err != nil || d.IsDir() || !isTest(d.Name()) {
			return err
		}
		rel, err := filepath.Rel(orig, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if p.matches(filepath.ToSlash(rel)) == "" {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list protected paths: %w", err)
	}
	return out, nil
}

// end::fixed[]
