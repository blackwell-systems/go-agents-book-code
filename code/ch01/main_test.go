package main

import (
	"bytes"
	"encoding/json/jsontext"
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/transcript.txt")

// TestScriptedRun replays the chapter's run and compares its output
// with testdata/transcript.txt, which the chapter shows. Run with
// -update after changing the script or the output format.
func TestScriptedRun(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	const golden = "testdata/transcript.txt"
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("output differs from %s:\n%s", golden, out.String())
	}
}

// TestExchangeIsJSON checks that the example exchange the chapter shows
// is valid JSON.
func TestExchangeIsJSON(t *testing.T) {
	b, err := os.ReadFile("testdata/exchange.json")
	if err != nil {
		t.Fatal(err)
	}
	if v := jsontext.Value(b); !v.IsValid() {
		t.Error("testdata/exchange.json is not valid JSON")
	}
}
