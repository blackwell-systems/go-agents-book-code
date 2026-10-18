package triage

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// envState is the tool state a run keeps across a restart (chapter 15):
// the gate's verdicts, which files the model has read or made, and the
// notes. The conversation says the model read a file and wrote a note;
// a resumed run whose tools forgot both would refuse an edit the model
// has every reason to make, and read back no notes.
type envState struct {
	Refused  map[string]bool     `json:"refused,omitzero"`
	Events   []GateEvent         `json:"events,omitzero"`
	Read     map[string]bool     `json:"read,omitzero"`
	Created  map[string]bool     `json:"created,omitzero"`
	Notes    map[string][]string `json:"notes,omitzero"`
	NoteKeys map[string]bool     `json:"note_keys,omitzero"`
	Filed    map[string][]filing `json:"filed,omitzero"`
}

// Keep makes the Env keep its tool state in the file at path: it loads
// what an earlier process saved there, if anything, and saves after
// every change. Local file operations can't be cancelled, so it takes
// no context.
func (e *Env) Keep(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("keep tool state: %w", err)
	default:
		var st envState
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("keep tool state %s: %w", path, err)
		}
		e.refusedGroups, e.gateEvents = st.Refused, st.Events
		e.read, e.created = orEmpty(st.Read), orEmpty(st.Created)
		e.notes, e.noteKeys = st.Notes, orEmpty(st.NoteKeys)
		e.filed = st.Filed
		if e.notes == nil {
			e.notes = map[string][]string{}
		}
	}
	e.keep = path
	return nil
}

func orEmpty(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}

// persist saves the tool state, if the Env keeps it, with e.mu held:
// a temporary file renamed over the old one, as a checkpoint is.
func (e *Env) persist() error {
	if e.keep == "" {
		return nil
	}
	b, err := json.Marshal(envState{Refused: e.refusedGroups,
		Events: e.gateEvents, Read: e.read, Created: e.created,
		Notes: e.notes, NoteKeys: e.noteKeys, Filed: e.filed})
	if err != nil {
		return fmt.Errorf("save tool state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.keep), ".env-*.tmp")
	if err != nil {
		return fmt.Errorf("save tool state: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), e.keep)
	}
	if err != nil {
		return fmt.Errorf("save tool state: %w", err)
	}
	return nil
}
