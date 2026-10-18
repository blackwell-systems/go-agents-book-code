package triage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// tag::notes[]

type noteArgs struct {
	Group string `json:"group" desc:"the crash group the note is about: its fingerprint, such as fp_3ac19e"`
	Note  string `json:"note" desc:"one finding, decision, or next step, with the IDs and paths it concerns"`
}

func notesWrite(e *Env) agent.Tool {
	return agent.NewTool("notes_write",
		"Add a note to a crash group's notes: what you found, what you "+
			"ruled out, or what is left to do, with the report IDs, file "+
			"paths, and line numbers it concerns. Notes are kept for the "+
			"whole run, even when older turns are left out of what you "+
			"see, so write down anything you'll need later.",
		func(_ context.Context, a noteArgs) (string, error) {
			note := strings.TrimSpace(a.Note)
			if strings.TrimSpace(a.Group) == "" || note == "" {
				return "", errors.New("give the group the note is " +
					"about, its fingerprint, and a note that isn't blank")
			}
			n := e.addNote(a.Group, note)
			return fmt.Sprintf("noted; %s has %d notes", a.Group, n), nil
		})
}

// end::notes[]

type notesReadArgs struct {
	Group string `json:"group,omitzero" desc:"a fingerprint; leave out to read every group's notes"`
}

func notesRead(e *Env) agent.Tool {
	return agent.NewTool("notes_read",
		"Read your notes on one crash group, or on all of them.",
		func(_ context.Context, a notesReadArgs) (string, error) {
			return e.Notes(a.Group), nil
		})
}

// addNote appends a note to a group and returns how many it has.
func (e *Env) addNote(group, note string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notes[group] = append(e.notes[group], note)
	return len(e.notes[group])
}

// Notes returns the notes on one group, or on all of them when group is
// empty, as text: what a handoff to a fresh context would start from.
func (e *Env) Notes(group string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	groups := slices.Sorted(maps.Keys(e.notes))
	if group != "" {
		groups = []string{group}
	}
	var b strings.Builder
	for _, g := range groups {
		for i, n := range e.notes[g] {
			fmt.Fprintf(&b, "%s %d. %s\n", g, i+1, n)
		}
	}
	if b.Len() == 0 {
		return "no notes yet"
	}
	return b.String()
}
