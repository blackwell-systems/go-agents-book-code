package main

import (
	"bytes"
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the summary golden file")

// TestSummary checks the tables the book quotes against the saved
// results; after new runs, regenerate with go test ./evidence -update.
func TestSummary(t *testing.T) {
	var b bytes.Buffer
	if err := summary(&b, "testdata"); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/out/summary.txt"
	if *update {
		if err := os.WriteFile(golden, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), want) {
		t.Errorf("summary changed; run go test ./evidence -update and "+
			"check the numbers the book quotes\ngot:\n%s", b.String())
	}
}
