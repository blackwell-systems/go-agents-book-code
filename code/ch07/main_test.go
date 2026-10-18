package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden output files")

// TestRecordedRun replays the recorded run on m1-done-after-delete,
// with every tool running for real against a fresh workspace, and
// checks what the agent achieved, not what it said: the right root
// cause fixed with a reproducer, and the catalog's grade, which fails
// it on the two crashes already filed: it fixed them as well, instead
// of marking them duplicates. The output is the chapter's
// transcript.
func TestRecordedRun(t *testing.T) {
	var out bytes.Buffer
	getenv := func(string) string { return "" }
	err := run(t.Context(), getenv, &out, config{task: "m1-done-after-delete",
		repo: "..", maxTurns: 40, maxCost: 0.25})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"fixed, root cause store/store.go (*Store).Get, issue #103",
		"graded against the catalog: 12 of 16 checks passed",
		`failed: status N1A: answered "fixed", want "duplicate"`,
		"filed #103: store.Get",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	golden := filepath.Join("testdata", "out", "m1.txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err == nil && !bytes.Equal(out.Bytes(), want) {
		t.Errorf("output differs from %s; run go test -update to rewrite it", golden)
	}
}
