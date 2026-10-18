package durable

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

func sample() agent.State {
	st := agent.NewState("run-1", []agent.Message{
		{Role: "system", Content: "You triage crashes."},
		{Role: "user", Content: "Triage these reports."},
	})
	st.Messages = append(st.Messages, agent.Message{Role: "assistant",
		ToolCalls: []agent.ToolCall{
			{ID: "c1", Name: "issue_file",
				Arguments: jsontext.Value(`{"title":"Panic"}`)},
			// A model can write arguments that aren't JSON; the
			// checkpoint must still save them as written.
			{ID: "c2", Name: "issue_file",
				Arguments: jsontext.Value(`{"title": "cut off`)},
		},
		ReasoningDetails: jsontext.Value(`[{"type":"reasoning.text"}]`)})
	st.Phase = agent.PhaseTools
	st.Usage = agent.Usage{PromptTokens: 1200, Cost: 0.004}
	st.Turns = 1
	st.Attempts = []agent.Attempt{{CallID: "c1", Tool: "issue_file",
		Key:     agent.Key("run-1", "c1"),
		Started: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}}
	return st
}

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	c, err := Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Load(); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("Load before any save: %v, want ErrNoCheckpoint", err)
	}
	want := sample()
	if err := c.Save(t.Context(), want); err != nil { // real syncs
		t.Fatal(err)
	}
	got, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Saved.IsZero() {
		t.Error("Saved not set")
	}
	got.Saved = time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v\nwant %+v", got, want)
	}
	for name, perm := range map[string]os.FileMode{
		dir: 0o700, c.path: 0o600} {
		fi, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != perm {
			t.Errorf("%s: mode %v, want %v", name, fi.Mode().Perm(), perm)
		}
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		file    string
		wantErr bool
		is      error // the error's family, if it has one
	}{
		{`{"Version":1,"RunID":"r","Phase":"model"}`, false, nil},
		{`{"Version":2,"RunID":"r"}`, true, ErrVersion},
		{`{"RunID":"r"}`, true, ErrVersion},
		{`{"Version":1,"RunID":"r","Extra":true}`, true, nil},
		{`{"Version":1,"Messages":[{"Role":"user","ToolCalls":[` +
			`{"ID":"c","Arguments":{"a":1}}]}]}`, true, nil}, // not a string
	}
	for _, tt := range tests {
		_, err := decode([]byte(tt.file))
		if (err != nil) != tt.wantErr ||
			tt.is != nil && !errors.Is(err, tt.is) {
			t.Errorf("decode(%s): %v", tt.file, err)
		}
	}
}

func TestTornSave(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	c.noSync = true
	old := sample()
	if err := c.Save(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	for _, point := range []string{"mid-save", "after rename"} {
		c.crash = func(p string) {
			if p == point {
				panic(p)
			}
		}
		next := old
		next.Turns = 2
		func() {
			defer func() { _ = recover() }()
			_ = c.Save(t.Context(), next)
		}()
		got, err := c.Load()
		if err != nil {
			t.Fatalf("after a crash at %s: %v", point, err)
		}
		if wantTurns := map[string]int{"mid-save": 1,
			"after rename": 2}[point]; got.Turns != wantTurns {
			t.Errorf("crash at %s: loaded turn %d, want %d", point,
				got.Turns, wantTurns)
		}
		if err := os.WriteFile(c.path, mustJSON(t, old), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	// What a real crash leaves (no deferred cleanup runs): a partly
	// written temporary file. Load ignores it and Open removes it.
	tmp := filepath.Join(dir, ".run-1-123.tmp")
	if err := os.WriteFile(tmp, []byte(`{"Vers`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open left %s: %v", tmp, err)
	}
	if _, err := c.Load(); err != nil {
		t.Errorf("Load after a torn save: %v", err)
	}
}

func mustJSON(t *testing.T, st agent.State) []byte {
	t.Helper()
	dir := t.TempDir()
	c, err := Open(dir, st.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.noSync = true
	if err := c.Save(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "run-1"); !errors.Is(err, ErrLocked) {
		t.Errorf("second Open: %v, want ErrLocked", err)
	}
	other, err := Open(dir, "run-2")
	if err != nil {
		t.Errorf("Open of another run: %v", err)
	} else {
		other.Close()
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir, "run-1")
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	b.Close()
	for _, bad := range []string{"", ".hidden", "../up", "a/b"} {
		if _, err := Open(dir, bad); err == nil {
			t.Errorf("Open(%q) accepted a bad run ID", bad)
		}
	}
}

func TestRestore(t *testing.T) {
	dir := t.TempDir()
	const limit = 2
	for i := range limit + 2 {
		c, err := Open(dir, "run-1")
		if err != nil {
			t.Fatal(err)
		}
		c.noSync = true
		if i == 0 {
			if _, err := c.Restore(t.Context(), limit); !errors.Is(err,
				ErrNoCheckpoint) {
				t.Fatalf("Restore of a new run: %v", err)
			}
			if err := c.Save(t.Context(), sample()); err != nil {
				t.Fatal(err)
			}
			c.Close()
			continue
		}
		st, err := c.Restore(t.Context(), limit)
		c.Close()
		switch {
		case i <= limit && (err != nil || st.Resumes != i):
			t.Errorf("restart %d: Resumes %d, %v", i, st.Resumes, err)
		case i > limit && !errors.Is(err, ErrMaxResumes):
			t.Errorf("restart %d: %v, want ErrMaxResumes", i, err)
		case i > limit && !errors.Is(err, agent.ErrBudget):
			t.Errorf("ErrMaxResumes is not in the ErrBudget family")
		}
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	save := func(run string, phase agent.Phase, age time.Duration) {
		c, err := Open(dir, run)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.noSync = true
		c.now = func() time.Time { return now.Add(-age) }
		st := agent.NewState(run, nil)
		st.Phase = phase
		if err := c.Save(t.Context(), st); err != nil {
			t.Fatal(err)
		}
	}
	save("old-done", agent.PhaseDone, 72*time.Hour)
	save("new-done", agent.PhaseDone, time.Hour)
	save("old-running", agent.PhaseTools, 72*time.Hour)
	save("old-open", agent.PhaseDone, 72*time.Hour)
	held, err := Open(dir, "old-open")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	n, err := Prune(dir, 24*time.Hour, now)
	if err != nil || n != 1 {
		t.Errorf("Prune = %d, %v; want 1", n, err)
	}
	for run, kept := range map[string]bool{"old-done": false,
		"new-done": true, "old-running": true, "old-open": true} {
		_, err := os.Stat(filepath.Join(dir, run+".json"))
		if (err == nil) != kept {
			t.Errorf("%s: kept %v, want %v", run, err == nil, kept)
		}
	}
}

func TestWorkspace(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	write := func(name, body string) {
		if err := root.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a")
	write("b.go", "package b")
	var st agent.State
	for _, name := range []string{"a.go", "b.go", "a.go"} {
		if err := st.Wrote(root, name); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.Files) != 2 {
		t.Errorf("%d files recorded, want 2", len(st.Files))
	}
	if err := st.CheckWorkspace(root); err != nil {
		t.Errorf("unchanged workspace: %v", err)
	}
	write("a.go", "package a // edited")
	if err := root.Remove("b.go"); err != nil {
		t.Fatal(err)
	}
	err = st.CheckWorkspace(root)
	if !errors.Is(err, agent.ErrWorkspaceChanged) {
		t.Errorf("changed workspace: %v", err)
	}
	t.Log(err)
}
