package service

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The state directory's layout. Everything the service knows is in it,
// so a restart finds the same queue, runs, and decisions.
const (
	reportsDir = "reports" // <id>.json: each report, redacted
	runsDir    = "runs"    // <run>.json: one per fingerprint group's run
	reviewDir  = "review"  // <item>.json: approvals and unknown outcomes
	spendFile  = "spend.json"
)

// tag::write-json[]

// writeJSON saves v as name, so that after a crash name holds either
// the old value or the new one: a temporary file in the same
// directory, synced, renamed over name, and the directory synced, as
// chapter 15's checkpoint does.
func writeJSON(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(name), err)
	}
	dir := filepath.Dir(name)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(name), err)
	}
	defer os.Remove(f.Name()) // fails once renamed, as intended
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err == nil {
		err = syncDir(dir)
	}
	if err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(name), err)
	}
	return nil
}

// end::write-json[]

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err //nolint:wrapcheck // writeJSON wraps it
	}
	defer d.Close() // opened to sync, not to write
	return d.Sync() //nolint:wrapcheck // writeJSON wraps it
}

func readJSON(name string, v any) error {
	b, err := os.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(name), err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(name), err)
	}
	return nil
}

// readAll reads every .json file in dir into a new T, skipping the
// temporary files a crash may have left.
func readAll[T any](dir string) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(dir), err)
	}
	var out []T
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			_ = os.Remove(filepath.Join(dir, name)) // a crash's leftover
			continue
		}
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		var v T
		if err := readJSON(filepath.Join(dir, name), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}
