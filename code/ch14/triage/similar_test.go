package triage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
)

// TestNotes checks that notes are kept per group, in order, and read
// back one group or all.
func TestNotes(t *testing.T) {
	_, tools := newTestEnv(t, "m1-done-after-delete")
	if got, _ := call(t, tools, "notes_read", map[string]any{}); got != "no notes yet" {
		t.Errorf("empty notes = %q", got)
	}
	for _, n := range []map[string]any{
		{"group": "fp_aaaaaa", "note": "cr_1e2ea0 panics in api.owned"},
		{"group": "fp_aaaaaa", "note": "store.Get returns nil for a tombstone"},
		{"group": "fp_bbbbbb", "note": "duplicate of #101"},
	} {
		if _, err := call(t, tools, "notes_write", n); err != nil {
			t.Fatal(err)
		}
	}
	got, err := call(t, tools, "notes_read", map[string]any{"group": "fp_aaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	want := "fp_aaaaaa 1. cr_1e2ea0 panics in api.owned\n" +
		"fp_aaaaaa 2. store.Get returns nil for a tombstone\n"
	if got != want {
		t.Errorf("notes_read = %q, want %q", got, want)
	}
	all, _ := call(t, tools, "notes_read", map[string]any{})
	mustContain(t, all, "fp_bbbbbb 1. duplicate of #101")
}

// TestSimilarity checks the score's edges, and that crash_similar finds
// a report that shares a cause under another fingerprint.
func TestSimilarity(t *testing.T) {
	stack := func(fns ...string) string {
		var b strings.Builder
		b.WriteString("goroutine 1 [running]:\n")
		for _, f := range fns {
			b.WriteString("example.com/remind/" + f + "(0x1)\n\t/app/remind/x.go:1 +0x1\n")
		}
		return b.String()
	}
	a := Report{Stack: stack("api.(*API).stats", "server.accessLog", "metrics.Instrument")}
	b := Report{Stack: stack("api.(*API).export", "server.accessLog", "metrics.Instrument")}
	if got := Similarity(a, a); got != 1 {
		t.Errorf("a report with itself = %v", got)
	}
	if got := Similarity(a, b); got != 0.5 {
		t.Errorf("two of four functions shared = %v, want 0.5", got)
	}
	if got := Similarity(a, Report{}); got != 0 {
		t.Errorf("no frames = %v", got)
	}
	// A function a stack passes through twice counts once, so the
	// score stays at most 1 and is the same both ways.
	loop := Report{Stack: stack("api.(*API).stats", "server.accessLog",
		"api.(*API).stats")}
	one := Report{Stack: stack("api.(*API).stats")}
	if x, y := Similarity(loop, one), Similarity(one, loop); x != 0.5 || y != 0.5 {
		t.Errorf("repeated frame: %v and %v, want 0.5 both ways", x, y)
	}

	_, tools := newTestEnv(t, "g2-missing-auth") // one cause, two crash sites
	got, err := call(t, tools, "crash_similar", map[string]any{"id": "cr_a0e237"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, got, "cr_2a3c3c")
}

// TestSimilarCatalog compares crash_similar's score with fingerprints
// on every report in the catalog at once: for each threshold, of the
// pairs it puts together, how many share a cause (precision), and of
// the pairs that share a cause, how many it finds (recall). Run with
// -v to see the numbers the chapter quotes.
func TestSimilarCatalog(t *testing.T) {
	tasks, err := catalog.Load(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := catalog.Batch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	reports := convertReports(batch.Reports)
	cause := map[string]string{}
	for c, ids := range batch.Expected {
		for _, id := range ids {
			cause[id] = c
		}
	}
	score := func(together func(a, b Report) bool) string {
		p, r := PairScore(reports, cause, together)
		return fmt.Sprintf("precision %.2f, recall %.2f", p, r)
	}
	// The numbers the chapter quotes: a change to the catalog or to
	// either score changes them, and the prose with them.
	got := score(func(a, b Report) bool { return Fingerprint(a) == Fingerprint(b) })
	t.Logf("fingerprints: %s", got)
	if want := "precision 0.99, recall 0.99"; got != want {
		t.Errorf("fingerprints: %s, want %s", got, want)
	}
	for _, tc := range []struct {
		th   float64
		want string
	}{
		{0.9, "precision 0.99, recall 0.98"},
		{0.7, "precision 0.96, recall 0.98"},
		{0.5, "precision 0.55, recall 1.00"},
		{0.3, "precision 0.14, recall 1.00"},
	} {
		got := score(func(a, b Report) bool { return Similarity(a, b) >= tc.th })
		t.Logf("similarity >= %.1f: %s", tc.th, got)
		if got != tc.want {
			t.Errorf("similarity >= %.1f: %s, want %s", tc.th, got, tc.want)
		}
	}
	if p, r := PairScore(reports, cause, func(a, b Report) bool {
		return Similarity(a, b) >= 1
	}); p <= 0 || r <= 0 {
		t.Errorf("identical stacks: precision %.2f, recall %.2f", p, r)
	}
}

// convertReports copies the catalog's reports into this package's type.
func convertReports(in []catalog.Report) []Report {
	var out []Report
	for _, cr := range in {
		out = append(out, Report{ID: cr.ID, Version: cr.Version,
			Commit: cr.Commit, Kind: cr.Kind, Panic: cr.Panic, Stack: cr.Stack,
			Logs: cr.Logs, FirstSeen: cr.FirstSeen, LastSeen: cr.LastSeen,
			Count: cr.Count})
	}
	return out
}
