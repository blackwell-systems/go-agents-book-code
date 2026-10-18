package main

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"os"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

var update = flag.Bool("update", false, "rewrite the golden table")

// TestTable checks the comparison against testdata/table.adoc, which
// chapter 16 includes. Run with -update after a change to any version
// or a framework upgrade, and read the diff.
func TestTable(t *testing.T) {
	var b bytes.Buffer
	if err := table(context.Background(), &b, ".."); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/table.adoc"
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
		t.Errorf("table changed; rerun with -update and read the "+
			"diff\ngot:\n%s", b.Bytes())
	}
}

// TestLive replays the live run each version recorded with -live, so
// the versions are checked against a real model's replies too.
func TestLive(t *testing.T) {
	for _, v := range versions {
		t.Run(v.Dir, func(t *testing.T) {
			want, _, _, err := recorded("testdata/live", v.Dir)
			if err != nil {
				t.Fatal(err)
			}
			w := &task.Wire{Next: &replay.Transport{
				Files: os.DirFS("testdata/live")}}
			c := task.Config{BaseURL: "https://openrouter.ai/api/v1",
				APIKey: "replay", HTTP: &http.Client{Transport: w}}
			env := &task.Env{}
			ctx := replay.WithScript(context.Background(), v.Dir)
			if _, err := v.Run(ctx, c, env); err != nil {
				t.Fatalf("Run: %v", err)
			}
			r := env.Report()
			if r.Diagnosis == nil || r.Asked != 1 || r.Issues != 1 {
				t.Errorf("asked %d, filed %d, diagnosis %v; want 1, 1, "+
					"and a diagnosis", r.Asked, r.Issues, r.Diagnosis != nil)
			}
			if n := len(w.Bodies()); n != want {
				t.Errorf("%d calls, want the %d recorded", n, want)
			}
		})
	}
}
