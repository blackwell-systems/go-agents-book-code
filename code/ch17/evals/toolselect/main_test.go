package main

import (
	"io/fs"
	"strings"
	"testing"
)

// TestScores replays the recorded runs and checks that each model's
// score is in range. Chapter 8's tools and cases differ from chapter
// 7's, so the smoke test is recorded again for this chapter; until it
// is, the test is skipped (see testdata/replies/README.md).
func TestScores(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	model := "anthropic/claude-haiku-5.5"
	if _, err := fs.Stat(saved, "testdata/replies/"+strings.ReplaceAll(model, "/", "_")); err != nil {
		t.Skip("no recorded smoke test for chapter 8 yet")
	}
	defs, err := toolDefs(false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client(model, "")
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := smoke(t.Context(), c, defs, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if most := float64(len(cases) * 3); got < 0 || got > most {
		t.Errorf("score %.1f out of range 0 to %.0f", got, most)
	}
}
